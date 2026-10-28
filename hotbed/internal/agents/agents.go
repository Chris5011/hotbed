// Package agents implements coding-agent modules: OpenCode, Claude Code,
// Codex, … as drop-in folders instead of hard-wired code.
//
// A module is a directory agents/<id>/ (kit tree; a same-named directory
// under the config dir overrides it) holding:
//
//	agent.json   manifest (name, command, proxy wiring, egress hints)
//	install.sh   optional; runs as root inside the container (bash, with
//	             agents/_lib.sh sourced first for shared helpers)
//
// Modules are toggled in config.json ("agents": {"<id>": true}); enabled
// ones are offered at sandbox creation. Installs leave a marker in the
// container (/etc/hotbed/agents/<id>), so "installed" is the container's
// own state and dies with it. Proxy wiring renders per-sandbox env/files
// from the manifest templates with a minted hbk- key, so every agent's
// traffic lands in the same burn ledger as OpenCode's.
package agents

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"hotbed/internal/config"
	"hotbed/internal/env"
)

// Module is one parsed agent manifest.
type Module struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Description    string   `json:"description,omitempty"`
	Homepage       string   `json:"homepage,omitempty"`
	Command        string   `json:"command"`                   // what the launch runs
	Builtin        bool     `json:"builtin,omitempty"`         // baked into the stack images (no install)
	DefaultEnabled bool     `json:"default_enabled,omitempty"` // when config.json does not say
	Resume         string   `json:"resume,omitempty"`          // command continuing the last conversation
	Proxy          *Proxy   `json:"proxy,omitempty"`           // nil = agent brings its own auth
	Persist        *Persist `json:"persist,omitempty"`         // sessions kept in the workspace
	EgressDomains  []string `json:"egress_domains,omitempty"`  // added to allowlist sandboxes at create

	Dir        string `json:"-"`
	HasInstall bool   `json:"has_install"`
}

// Proxy describes how the agent talks to the hotbed LLM proxy.
type Proxy struct {
	Style string            `json:"style"`           // openai | anthropic (provider wire style)
	Env   map[string]string `json:"env,omitempty"`   // templated env for the launch
	Files map[string]string `json:"files,omitempty"` // "~/path" -> templated content
	// Setup runs after env + files are written, as the sandbox user with
	// the env file loaded; template values are substituted shell-quoted.
	// For agents configured through their own CLI (hermes config set …).
	Setup string `json:"setup,omitempty"`
}

// Persist moves an agent's conversations into the workspace
// ({{state}} = /workspace/.hotbed/agents/<id>), so tearing the sandbox
// down keeps them and a new sandbox on the same folder resumes them.
type Persist struct {
	Env   map[string]string `json:"env,omitempty"`   // relocation vars, e.g. CLAUDE_CONFIG_DIR={{state}}
	Links map[string]string `json:"links,omitempty"` // "~/path" -> "rel/" (dir) | "rel" (file) under {{state}}
}

// MarkerDir holds one file per installed module inside a container.
const MarkerDir = "/etc/hotbed/agents"

var (
	idRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)
	envRe = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
)

// Dirs lists module search roots, lowest priority first.
func Dirs() []string {
	return []string{
		filepath.Join(env.Root(), "agents"),
		filepath.Join(env.ConfigDir(), "agents"),
	}
}

// Load reads every module; later roots override earlier ones by id.
// Broken manifests are skipped and reported in the error list (non-fatal).
func Load() ([]Module, []error) {
	byID := map[string]Module{}
	var errs []error
	for _, root := range Dirs() {
		ents, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range ents {
			if !e.IsDir() || !idRe.MatchString(e.Name()) {
				continue
			}
			m, err := loadOne(filepath.Join(root, e.Name()))
			if err != nil {
				errs = append(errs, err)
				continue
			}
			byID[m.ID] = m
		}
	}
	out := make([]Module, 0, len(byID))
	for _, m := range byID {
		out = append(out, m)
	}
	// alphabetical by display name everywhere (UI, CLI, wizard)
	sort.Slice(out, func(i, j int) bool {
		a, b := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name)
		if a != b {
			return a < b
		}
		return out[i].ID < out[j].ID
	})
	return out, errs
}

func loadOne(dir string) (Module, error) {
	id := filepath.Base(dir)
	b, err := os.ReadFile(filepath.Join(dir, "agent.json"))
	if err != nil {
		return Module{}, fmt.Errorf("agent %s: %w", id, err)
	}
	var m Module
	if err := json.Unmarshal(b, &m); err != nil {
		return Module{}, fmt.Errorf("agent %s: agent.json: %w", id, err)
	}
	m.ID, m.Dir = id, dir
	if m.Name == "" {
		m.Name = id
	}
	if strings.TrimSpace(m.Command) == "" {
		return Module{}, fmt.Errorf("agent %s: agent.json needs a command", id)
	}
	if p := m.Proxy; p != nil {
		if p.Style != "openai" && p.Style != "anthropic" {
			return Module{}, fmt.Errorf("agent %s: proxy.style must be openai or anthropic", id)
		}
		for k := range p.Env {
			if !envRe.MatchString(k) {
				return Module{}, fmt.Errorf("agent %s: proxy.env %q is not a variable name", id, k)
			}
		}
		for path := range p.Files {
			if !(strings.HasPrefix(path, "~/") || strings.HasPrefix(path, "{{state}}/")) || strings.Contains(path, "..") {
				return Module{}, fmt.Errorf("agent %s: proxy.files %q must be a ~/ or {{state}}/ path", id, path)
			}
		}
	}
	if p := m.Persist; p != nil {
		for k := range p.Env {
			if !envRe.MatchString(k) {
				return Module{}, fmt.Errorf("agent %s: persist.env %q is not a variable name", id, k)
			}
		}
		for src, rel := range p.Links {
			if !strings.HasPrefix(src, "~/") || strings.Contains(src, "..") ||
				rel == "" || strings.HasPrefix(rel, "/") || strings.Contains(rel, "..") {
				return Module{}, fmt.Errorf("agent %s: persist.links %q -> %q must map a ~/ path to a relative name", id, src, rel)
			}
		}
	}
	_, err = os.Stat(filepath.Join(dir, "install.sh"))
	m.HasInstall = err == nil
	if !m.Builtin && !m.HasInstall {
		return Module{}, fmt.Errorf("agent %s: non-builtin module needs an install.sh", id)
	}
	return m, nil
}

// Get loads one module by id.
func Get(id string) (Module, error) {
	mods, _ := Load()
	for _, m := range mods {
		if m.ID == id {
			return m, nil
		}
	}
	return Module{}, fmt.Errorf("unknown agent module %q (see `hotbedd agents ls`)", id)
}

// Enabled reports whether a module is switched on.
func Enabled(cfg *config.Config, m Module) bool {
	if v, ok := cfg.Agents[m.ID]; ok {
		return v
	}
	return m.DefaultEnabled
}

// ------------------------------------------------------------------ install ---

// Box is one sandbox an agent lives in — a container (docker exec) or a VM
// guest of either backend (ssh); internal/attach provides both. Scripts are
// bash; RunUser runs as the sandbox's login user in a login shell.
type Box interface {
	Name() string
	Home() string
	RunRoot(script []byte, env map[string]string, to time.Duration) (string, error)
	RunUser(script string, to time.Duration) (string, error)
	Write(path string, data []byte) error
}

// Install runs the module's install.sh inside the sandbox (root, bash) and
// leaves the marker. Builtin modules are a no-op. Needs outbound network —
// callers install before any egress fence goes up.
func Install(b Box, m Module) error {
	if m.Builtin {
		return nil
	}
	script, err := os.ReadFile(filepath.Join(m.Dir, "install.sh"))
	if err != nil {
		return err
	}
	var full []byte
	// shared helpers: the module's own root first, then the kit's
	for _, root := range []string{filepath.Dir(m.Dir), filepath.Join(env.Root(), "agents")} {
		if lib, e := os.ReadFile(filepath.Join(root, "_lib.sh")); e == nil {
			full = append(full, lib...)
			full = append(full, '\n')
			break
		}
	}
	// the whole script arrives on stdin (bash -s): the module body runs with
	// stdin closed, or an installer reading stdin would swallow the rest
	full = append(full, "{\n"...)
	full = append(full, script...)
	full = append(full, "\n} </dev/null\n"...)
	full = append(full, []byte(fmt.Sprintf("mkdir -p %s && date -u +%%FT%%TZ > %s/%s\n", MarkerDir, MarkerDir, m.ID))...)
	out, err := b.RunRoot(full, map[string]string{"HOTBED_AGENT": m.ID, "HOTBED_HOME": b.Home()}, 20*time.Minute)
	if err != nil {
		return fmt.Errorf("install %s in %s: %w: %s", m.ID, b.Name(), err, tail(out, 6))
	}
	return nil
}

// Installed lists the module ids installed in a sandbox (builtins
// included — they ship with every stack image and VM).
func Installed(b Box) []string {
	ids := map[string]bool{}
	mods, _ := Load()
	for _, m := range mods {
		if m.Builtin {
			ids[m.ID] = true
		}
	}
	if out, err := b.RunUser("ls -1 "+MarkerDir+" 2>/dev/null; true", 20*time.Second); err == nil {
		for _, l := range strings.Split(out, "\n") {
			if l = strings.TrimSpace(l); idRe.MatchString(l) {
				ids[l] = true
			}
		}
	}
	list := make([]string, 0, len(ids))
	for id := range ids {
		list = append(list, id)
	}
	sort.Strings(list)
	return list
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}
