package api

// Coding-agent modules (see internal/agents).
//
//	GET  /api/agents                          every module + enabled flag
//	POST /api/agents {"id":"codex","enabled":true}
//	GET  /api/sandboxes/container/<n>/agents  installed modules
//	POST /api/sandboxes/container/<n>/agents {"id":"codex"}   install (+ sync)

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"hotbed/internal/agents"
	"hotbed/internal/attach"
	"hotbed/internal/audit"
	"hotbed/internal/sandbox"
)

type agentView struct {
	agents.Module
	Enabled bool   `json:"enabled"`
	Wired   string `json:"wired"` // proxy readiness hint for the modules page
}

func (s *Server) agentsRoute(w http.ResponseWriter, r *http.Request) {
	s.reloadCfg()
	switch r.Method {
	case http.MethodGet:
		mods, errs := agents.Load()
		out := make([]agentView, 0, len(mods))
		for _, m := range mods {
			out = append(out, agentView{Module: m, Enabled: agents.Enabled(s.Cfg, m), Wired: s.wiredHint(m)})
		}
		var bad []string
		for _, e := range errs {
			bad = append(bad, e.Error())
		}
		okJSON(w, map[string]any{"agents": out, "errors": bad, "dirs": agents.Dirs()})
	case http.MethodPost:
		var in struct {
			ID      string `json:"id"`
			Enabled bool   `json:"enabled"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in); err != nil {
			httpError(w, 400, "invalid JSON: "+err.Error())
			return
		}
		if _, err := agents.Get(in.ID); err != nil {
			httpError(w, 404, err.Error())
			return
		}
		if err := s.Cfg.SetAgentEnabled(in.ID, in.Enabled); err != nil {
			httpError(w, 500, err.Error())
			return
		}
		okJSON(w, map[string]any{"ok": true, "id": in.ID, "enabled": in.Enabled})
	default:
		httpError(w, 405, "GET or POST")
	}
}

// wiredHint says how a module reaches a model with the current config.
func (s *Server) wiredHint(m agents.Module) string {
	if m.Proxy == nil {
		if m.Builtin {
			return "proxy via sync"
		}
		return "own login inside the sandbox"
	}
	for _, p := range s.Cfg.Opencode.Providers {
		if p.StyleWire() == m.Proxy.Style && p.BaseURL != "" {
			return "proxy via a " + m.Proxy.Style + "-style provider"
		}
	}
	return "needs a " + m.Proxy.Style + "-style provider in config (else own login)"
}

// sandboxAgentsRoute lists / installs modules in one sandbox (container
// or VM of either backend).
func (s *Server) sandboxAgentsRoute(w http.ResponseWriter, r *http.Request, sb sandbox.Sandbox) {
	if sb.State != "running" {
		httpError(w, 409, sb.Name+" is not running")
		return
	}
	box, err := attach.BoxFor(sb, sshPort(sb))
	if err != nil {
		httpError(w, 500, err.Error())
		return
	}
	if r.Method == http.MethodGet {
		okJSON(w, map[string]any{"installed": installedViews(box)})
		return
	}
	if r.Method != http.MethodPost {
		httpError(w, 405, "GET or POST")
		return
	}
	var in struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in); err != nil {
		httpError(w, 400, "invalid JSON: "+err.Error())
		return
	}
	m, err := agents.Get(in.ID)
	if err != nil {
		httpError(w, 404, err.Error())
		return
	}
	if err := agents.Install(box, m); err != nil {
		httpError(w, 500, err.Error())
		return
	}
	audit.Record("agent.install", sb.Name, m.ID)
	warn := ""
	if _, e := s.syncAgents(sb, m.ID); e != nil { // persistence + proxy wiring
		warn = e.Error()
	}
	okJSON(w, map[string]any{"ok": true, "installed": installedViews(box), "warn": warn})
}

// sshPort is the ssh port override of a docker-backend VM ("" otherwise).
func sshPort(sb sandbox.Sandbox) string {
	if sb.Kind == sandbox.KindVM && len(sb.Ports) > 0 {
		return strings.TrimPrefix(sb.Ports[0], "127.0.0.1:")
	}
	return ""
}

type installedView struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Builtin bool   `json:"builtin,omitempty"`
}

func installedViews(box agents.Box) []installedView {
	out := []installedView{}
	for _, id := range agents.Installed(box) {
		v := installedView{ID: id, Name: id}
		if m, err := agents.Get(id); err == nil {
			v.Name, v.Builtin = m.Name, m.Builtin
		}
		out = append(out, v)
	}
	return out
}

// installAgents installs modules into a sandbox and wires them (create
// wizard: containers right after spawn, VMs after their post-boot).
func (s *Server) installAgents(sb sandbox.Sandbox, mods []agents.Module) (installed []string, warn string) {
	box, err := attach.BoxFor(sb, sshPort(sb))
	if err != nil {
		return nil, "agents: " + err.Error()
	}
	var warns []string
	for _, m := range mods {
		if err := agents.Install(box, m); err != nil {
			warns = append(warns, "agent "+m.ID+": "+err.Error())
			continue
		}
		audit.Record("agent.install", sb.Name, m.ID)
		installed = append(installed, m.ID)
	}
	return installed, strings.Join(warns, "; ")
}

// syncAgents wires installed proxy-capable modules (all, or only the given
// ids) of a sandbox to the hotbed proxy. OpenCode keeps its own sync path.
func (s *Server) syncAgents(sb sandbox.Sandbox, only ...string) ([]agents.SyncResult, error) {
	want := map[string]bool{}
	for _, id := range only {
		want[id] = true
	}
	s.reloadCfg()
	box, err := attach.BoxFor(sb, sshPort(sb))
	if err != nil {
		return nil, err
	}
	var base string
	var res []agents.SyncResult
	var errs []string
	for _, id := range agents.Installed(box) {
		if len(want) > 0 && !want[id] {
			continue
		}
		m, err := agents.Get(id)
		if err != nil {
			continue
		}
		if m.Builtin { // classic wiring (cfgsync); only its session folder here
			if e := agents.ApplyPersist(box, m); e != nil {
				errs = append(errs, e.Error())
			}
			continue
		}
		if m.Proxy != nil && base == "" && len(s.Cfg.Opencode.Providers) > 0 {
			b, _, e := attach.ResolveProxyURL(sb, s.Cfg.Proxy.Port)
			if e != nil {
				return res, e
			}
			base = b
		}
		r, e := agents.Sync(box, m, s.Cfg, s.Keys, base)
		if e != nil {
			errs = append(errs, e.Error())
			continue
		}
		res = append(res, r)
	}
	if len(errs) > 0 {
		return res, fmt.Errorf("agent sync: %v", errs)
	}
	return res, nil
}
