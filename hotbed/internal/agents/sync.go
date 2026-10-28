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

// EnvPath is the per-agent env file a launch sources (container side).
func EnvPath(home, id string) string { return home + "/.config/hotbed/agents/" + id + ".env" }

// SyncResult reports one agent's proxy wiring.
type SyncResult struct {
	Agent    string `json:"agent"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Skipped  string `json:"skipped,omitempty"` // why nothing was wired
}

// Sync wires one installed agent to the hotbed proxy: picks a provider of
// the module's wire style, mints (or reuses) the sandbox's key for it and
// writes the rendered env file + config files into the sandbox, then runs
// the module's setup command (as the sandbox user, env loaded). base is the
// tested-reachable proxy URL (…/v1) from attach.ResolveProxyURL.
func Sync(b Box, m Module, cfg *config.Config, ks *keys.Store, base string) (SyncResult, error) {
	sandbox, home := b.Name(), b.Home()

	res := SyncResult{Agent: m.ID}
	if m.Proxy == nil {
		res.Skipped = "no proxy wiring (the agent uses its own login)"
		return res, nil
	}
	pid, prov, ok := pickProvider(cfg, m.Proxy.Style)
	if !ok {
		res.Skipped = fmt.Sprintf("no %s-style provider in config.json — add one (style: %q) or log in inside the sandbox",
			m.Proxy.Style, m.Proxy.Style)
		return res, nil
	}
	key, _, err := ks.EnsureAgentKey(sandbox, m.ID, pid)
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
	vars := map[string]string{
		"{{base}}":           strings.TrimRight(base, "/"),
		"{{base_root}}":      strings.TrimSuffix(strings.TrimRight(base, "/"), "/v1"),
		"{{key}}":            key.Token,
		"{{model}}":          model,
		"{{models_json}}":    string(modelsJSON), // ["a","b"]
		"{{models_objects}}": string(objsJSON),   // [{"id":"a","name":"a"},…]
		"{{provider}}":       pid,
		"{{home}}":           home,
	}
	if len(models) == 0 { // unknown models: templates using the lists are skipped
		vars["{{models_json}}"], vars["{{models_objects}}"] = "", ""
	}
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

	names := make([]string, 0, len(m.Proxy.Env))
	for k := range m.Proxy.Env {
		names = append(names, k)
	}
	sort.Strings(names)
	var envFile strings.Builder
	envFile.WriteString("# written by hotbedd sync - per-sandbox proxy wiring for " + m.Name + "\n")
	for _, k := range names {
		if v, ok := render(m.Proxy.Env[k]); ok {
			fmt.Fprintf(&envFile, "export %s=%s\n", k, shQuote(v))
		}
	}
	if err := b.Write(EnvPath(home, m.ID), []byte(envFile.String())); err != nil {
		return res, fmt.Errorf("%s: writing env: %w", m.ID, err)
	}
	for p, tmpl := range m.Proxy.Files {
		content, ok := render(tmpl)
		if !ok {
			continue
		}
		dst := path.Join(home, strings.TrimPrefix(p, "~/"))
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

// LaunchScript is the in-container bash snippet that starts an agent with
// its synced env (when present) from /workspace. When the agent exits the
// pane drops into a shell (env kept) instead of vanishing — a startup
// error stays readable, and the agent can be re-run by hand.
func LaunchScript(home string, m Module) string {
	env := EnvPath(home, m.ID)
	return fmt.Sprintf("[ -f %s ] && . %s; cd /workspace 2>/dev/null; %s; "+
		"printf '\\n[hotbed] %%s exited (%%s) - shell in the sandbox, exit closes this session\\n' %s \"$?\"; exec bash",
		shQuote(env), shQuote(env), m.Command, shQuote(m.Name))
}

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
