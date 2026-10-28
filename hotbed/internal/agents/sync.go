package agents

import (
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"hotbed/internal/audit"
	"hotbed/internal/config"
	"hotbed/internal/keys"
)

// EnvPath is the per-agent env file a launch sources (sandbox side).
func EnvPath(home, id string) string { return home + "/.config/hotbed/agents/" + id + ".env" }

// WorkspaceState is where agents keep what must outlive the sandbox
// (conversations, session databases): inside the project mount, so a new
// sandbox on the same folder resumes them. Git-ignored by its own file.
const WorkspaceState = "/workspace/.hotbed"

// StateDir is one agent's persistent folder ({{state}} in manifests).
func StateDir(id string) string { return WorkspaceState + "/agents/" + id }

// SyncResult reports one agent's wiring.
type SyncResult struct {
	Agent     string `json:"agent"`
	Provider  string `json:"provider,omitempty"`
	Model     string `json:"model,omitempty"`
	Persisted string `json:"persisted,omitempty"` // state folder in the workspace
	Skipped   string `json:"skipped,omitempty"`   // why no proxy wiring
}

// Sync prepares one installed agent: sessions persisted into the
// workspace (manifest `persist`), then — when a provider of the module's
// style exists and base is known — proxy wiring with the agent's own
// virtual key: rendered env file, config files and the setup command (run
// as the sandbox user, env loaded). base is the tested-reachable proxy URL
// (…/v1) from attach.ResolveProxyURL; "" skips the proxy part.
func Sync(b Box, m Module, cfg *config.Config, ks *keys.Store, base string) (SyncResult, error) {
	sandbox, home := b.Name(), b.Home()
	res := SyncResult{Agent: m.ID}

	vars := map[string]string{"{{home}}": home, "{{state}}": StateDir(m.ID)}
	// quote=true substitutes shell-quoted values (setup commands)
	renderQ := func(s string, quote bool) (string, bool) {
		for k, v := range vars {
			if v == "" && strings.Contains(s, k) {
				return "", false // e.g. no model known: leave the agent's default
			}
		}
		pairs := make([]string, 0, 2*len(vars))
		for k, v := range vars {
			if quote {
				v = shQuote(v)
			}
			pairs = append(pairs, k, v)
		}
		return strings.NewReplacer(pairs...).Replace(s), true
	}
	render := func(s string) (string, bool) { return renderQ(s, false) }

	var envFile strings.Builder
	envFile.WriteString("# written by hotbedd sync - " + m.Name + " (persistence + proxy wiring)\n")
	writeEnv := func(env map[string]string) {
		names := make([]string, 0, len(env))
		for k := range env {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			if v, ok := render(env[k]); ok {
				fmt.Fprintf(&envFile, "export %s=%s\n", k, shQuote(v))
			}
		}
	}
	flushEnv := func() error {
		if err := b.Write(EnvPath(home, m.ID), []byte(envFile.String())); err != nil {
			return fmt.Errorf("%s: writing env: %w", m.ID, err)
		}
		return nil
	}

	if m.Persist != nil {
		if err := ApplyPersist(b, m); err != nil {
			return res, err
		}
		res.Persisted = StateDir(m.ID)
		writeEnv(m.Persist.Env)
	}

	var pid string
	var prov config.Provider
	ok := false
	switch {
	case m.Proxy == nil:
		res.Skipped = "no proxy wiring (the agent uses its own login)"
	case base == "":
		res.Skipped = "no LLM provider configured - proxy wiring skipped"
	default:
		if pid, prov, ok = pickProvider(cfg, m.Proxy.Style); !ok {
			res.Skipped = fmt.Sprintf("no %s-style provider in config.json — add one (style: %q) or log in inside the sandbox",
				m.Proxy.Style, m.Proxy.Style)
		}
	}
	if !ok {
		if m.Persist == nil {
			return res, nil
		}
		return res, flushEnv()
	}

	key, _, err := ks.EnsureWorkspaceKey(sandbox, m.ID, pid, WorkspaceID(b))
	if err != nil {
		return res, err
	}
	model := ""
	if pid == cfg.DefaultProvider() && cfg.Opencode.DefaultModel != "" {
		model = cfg.Opencode.DefaultModel
	} else if len(prov.Models) > 0 {
		model = prov.Models[0]
	}
	// every model of the provider, default first: agents with a model
	// picker (Hermes, OpenClaw) get the whole list, not just the default
	models := []string{}
	if model != "" {
		models = append(models, model)
	}
	for _, m := range prov.Models {
		if m != "" && m != model {
			models = append(models, m)
		}
	}
	objs := make([]map[string]string, 0, len(models))
	for _, m := range models {
		objs = append(objs, map[string]string{"id": m, "name": m})
	}
	modelsJSON, _ := json.Marshal(models)
	objsJSON, _ := json.Marshal(objs)
	vars["{{base}}"] = strings.TrimRight(base, "/")
	vars["{{base_root}}"] = strings.TrimSuffix(strings.TrimRight(base, "/"), "/v1")
	vars["{{key}}"] = key.Token
	vars["{{model}}"] = model
	vars["{{models_json}}"] = string(modelsJSON)  // ["a","b"]
	vars["{{models_objects}}"] = string(objsJSON) // [{"id":"a","name":"a"},…]
	vars["{{provider}}"] = pid
	if len(models) == 0 { // unknown models: templates using the lists are skipped
		vars["{{models_json}}"], vars["{{models_objects}}"] = "", ""
	}

	writeEnv(m.Proxy.Env)
	if err := flushEnv(); err != nil {
		return res, err
	}
	for p, tmpl := range m.Proxy.Files {
		content, ok := render(tmpl)
		if !ok {
			continue
		}
		dst, _ := render(p)
		if strings.HasPrefix(dst, "~/") {
			dst = path.Join(home, strings.TrimPrefix(dst, "~/"))
		}
		if err := b.Write(dst, []byte(content)); err != nil {
			return res, fmt.Errorf("%s: writing %s: %w", m.ID, p, err)
		}
	}
	if m.Proxy.Setup != "" {
		if cmd, ok := renderQ(m.Proxy.Setup, true); ok {
			script := ". " + shQuote(EnvPath(home, m.ID)) + "\n" + cmd
			if out, err := b.RunUser(script, 3*time.Minute); err != nil {
				return res, fmt.Errorf("%s: setup: %w: %s", m.ID, err, tail(out, 4))
			}
		}
	}
	res.Provider, res.Model = pid, model
	audit.Record("agent.sync", sandbox, fmt.Sprintf("%s via %s", m.ID, pid))
	return res, nil
}

// ApplyPersist creates the agent's state folder in the workspace (plus the
// .gitignore that keeps it out of the project's commits) and points the
// manifest's links there. First run: an existing home folder/file is moved
// into the workspace; a later sandbox on the same workspace finds the
// state already there — the workspace copy wins, the fresh empty home
// copy is dropped. SQLite sidecars (-wal/-shm/-journal) move with a file.
func ApplyPersist(b Box, m Module) error {
	if m.Persist == nil {
		return nil
	}
	state := StateDir(m.ID)
	var sc strings.Builder
	fmt.Fprintf(&sc, `set -e
mkdir -p %[1]s
g=%[2]s/.gitignore; [ -f "$g" ] || printf '# hotbed agent state (sessions, history) - not project code\n*\n' > "$g"
link_dir() {  # SRC DST
  mkdir -p "$2"
  if [ -d "$1" ] && [ ! -L "$1" ]; then cp -a --update=none "$1"/. "$2"/ 2>/dev/null || true; rm -rf "$1"; fi
  mkdir -p "$(dirname "$1")"; ln -sfn "$2" "$1"
}
link_file() {  # SRC DST
  mkdir -p "$(dirname "$2")"
  if [ -f "$1" ] && [ ! -L "$1" ]; then
    if [ -e "$2" ]; then rm -f "$1" "$1-wal" "$1-shm" "$1-journal"
    else for x in "" -wal -shm -journal; do [ -e "$1$x" ] && mv "$1$x" "$2$x"; done; fi
  fi
  mkdir -p "$(dirname "$1")"; ln -sfn "$2" "$1"
}
`, shQuote(state), shQuote(WorkspaceState))
	srcs := make([]string, 0, len(m.Persist.Links))
	for s := range m.Persist.Links {
		srcs = append(srcs, s)
	}
	sort.Strings(srcs)
	for _, s := range srcs {
		rel := m.Persist.Links[s]
		src := b.Home() + "/" + strings.TrimPrefix(s, "~/")
		dst := state + "/" + strings.TrimSuffix(rel, "/")
		fn := "link_file"
		if strings.HasSuffix(rel, "/") {
			fn = "link_dir"
		}
		fmt.Fprintf(&sc, "%s %s %s\n", fn, shQuote(src), shQuote(dst))
	}
	if out, err := b.RunUser(sc.String(), time.Minute); err != nil {
		return fmt.Errorf("%s: persisting into %s: %w: %s", m.ID, state, err, tail(out, 3))
	}
	return nil
}

// WorkspaceID returns (creating it once) the id of the mounted project
// folder, /workspace/.hotbed/id — "" when the workspace is not writable.
// It ties virtual keys to the folder rather than to one sandbox.
func WorkspaceID(b Box) string {
	out, err := b.RunUser(`set -e; d=`+WorkspaceState+`; mkdir -p "$d"
[ -f "$d/.gitignore" ] || printf '# hotbed agent state (sessions, history) - not project code\n*\n' > "$d/.gitignore"
[ -s "$d/id" ] || cat /proc/sys/kernel/random/uuid > "$d/id"
cat "$d/id"`, 20*time.Second)
	if err != nil {
		return ""
	}
	id := strings.TrimSpace(out)
	if i := strings.LastIndex(id, "\n"); i >= 0 {
		id = id[i+1:]
	}
	if len(id) < 16 || strings.ContainsAny(id, " \t/") {
		return ""
	}
	return id
}

// PersistEnv renders a module's persist env for launches that do not
// source an env file (the builtin OpenCode's classic attach path).
func PersistEnv(m Module) map[string]string {
	out := map[string]string{}
	if m.Persist == nil {
		return out
	}
	for k, v := range m.Persist.Env {
		out[k] = strings.ReplaceAll(v, "{{state}}", StateDir(m.ID))
	}
	return out
}

// pickProvider prefers the default provider when its style fits, else the
// first (by id) provider of that style.
func pickProvider(cfg *config.Config, style string) (string, config.Provider, bool) {
	if id := cfg.DefaultProvider(); id != "" {
		if p, ok := cfg.Provider(id); ok && p.StyleWire() == style && p.BaseURL != "" {
			return id, p, true
		}
	}
	ids := make([]string, 0, len(cfg.Opencode.Providers))
	for id := range cfg.Opencode.Providers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if p := cfg.Opencode.Providers[id]; p.StyleWire() == style && p.BaseURL != "" {
			return id, p, true
		}
	}
	return "", config.Provider{}, false
}

// LaunchScript is the in-sandbox bash snippet that starts an agent with
// its synced env (when present) from /workspace — resume=true runs the
// manifest's resume command (continue the last conversation) instead.
// When the agent exits the pane drops into a shell (env kept) instead of
// vanishing: a startup error stays readable, the agent can be re-run.
func LaunchScript(home string, m Module, resume bool) string {
	env := EnvPath(home, m.ID)
	cmd := m.Command
	if resume && m.Resume != "" {
		cmd = m.Resume
	}
	return fmt.Sprintf("[ -f %s ] && . %s; cd /workspace 2>/dev/null; %s; "+
		"printf '\\n[hotbed] %%s exited (%%s) - shell in the sandbox, exit closes this session\\n' %s \"$?\"; exec bash",
		shQuote(env), shQuote(env), cmd, shQuote(m.Name))
}

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
