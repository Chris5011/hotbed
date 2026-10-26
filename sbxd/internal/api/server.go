// Package api serves the dashboard + REST/SSE endpoints of sbxd.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"sbxd/internal/attach"
	"sbxd/internal/audit"
	"sbxd/internal/cfgsync"
	"sbxd/internal/config"
	"sbxd/internal/engine"
	"sbxd/internal/env"
	"sbxd/internal/keys"
	"sbxd/internal/ledger"
	"sbxd/internal/proxy"
	"sbxd/internal/sandbox"
)

// Server bundles the shared state for handlers.
type Server struct {
	Collector *sandbox.Collector
	Cfg       *config.Config
	Keys      *keys.Store
	Led       *ledger.Ledger
	Proxy     *proxy.Service
}

// New builds the server.
func New(c *sandbox.Collector, cfg *config.Config, ks *keys.Store, led *ledger.Ledger, px *proxy.Service) *Server {
	return &Server{Collector: c, Cfg: cfg, Keys: ks, Led: led, Proxy: px}
}

// --------------------------------------------------------------- SSE hub ---
type subscriber struct {
	ch chan []byte
}

type hub struct {
	mu   sync.Mutex
	subs map[*subscriber]struct{}
}

func newHub() *hub { return &hub{subs: map[*subscriber]struct{}{}} }

func (h *hub) subscribe() *subscriber {
	s := &subscriber{ch: make(chan []byte, 8)}
	h.mu.Lock()
	h.subs[s] = struct{}{}
	h.mu.Unlock()
	return s
}

func (h *hub) unsubscribe(s *subscriber) {
	h.mu.Lock()
	delete(h.subs, s)
	h.mu.Unlock()
	close(s.ch)
}

func (h *hub) broadcast(b []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs {
		select {
		case s.ch <- b:
		default: // slow client: drop frame
		}
	}
}

var globalHub = newHub()

// ------------------------------------------------------------- handlers ---

// Handler returns the full HTTP mux.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/health", s.health)
	mux.HandleFunc("/api/stats", s.statsRoute)
	mux.HandleFunc("/api/sandboxes", s.listSandboxes)
	mux.HandleFunc("/api/sandboxes/", s.sandboxRoute)
	mux.HandleFunc("/api/events", s.eventsSSE)
	mux.HandleFunc("/api/terminal/open", s.terminalOpen)
	mux.HandleFunc("/api/actions/start", s.actionStart)
	mux.HandleFunc("/api/actions/stop", s.actionStop)
	mux.HandleFunc("/api/actions/delete", s.actionDelete)
	mux.HandleFunc("/api/usage", s.usageRoute)
	mux.HandleFunc("/api/usage/budget", s.budgetRoute)
	mux.HandleFunc("/api/usage/purge", s.usagePurge)
	mux.HandleFunc("/api/usage/pin", s.usagePin)
	mux.HandleFunc("/api/create", s.createRoute)
	mux.HandleFunc("/api/vm/boot", s.vmBootRoute)
	mux.HandleFunc("/api/fs", s.fsRoute)
	mux.HandleFunc("/api/config", s.configRoute)
	mux.HandleFunc("/api/config/import", s.configImport)
	mux.HandleFunc("/", s.index)
	mux.HandleFunc("/static/", s.static)

	return withOriginGuard(mux)
}

// withOriginGuard blocks cross-origin mutating requests (CSRF): any web page
// the user visits can silently POST to a localhost daemon otherwise (the
// classic "drive-by localhost" attack). GET/HEAD are pass-through (they are
// side-effect free here); native clients (the sbxd CLI, curl) send no Origin
// header and are unaffected; same-origin browser requests always match the
// request Host and pass. One rule, no token ceremony on the local UI.
func withOriginGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}
		o := strings.TrimSpace(r.Header.Get("Origin"))
		if o == "" {
			next.ServeHTTP(w, r)
			return
		}
		u, err := url.Parse(o)
		if err == nil && u.Host != "" && u.Host != "null" && strings.EqualFold(u.Host, r.Host) {
			next.ServeHTTP(w, r)
			return
		}
		httpError(w, 403, "cross-origin request rejected (browser CSRF guard)")
	})
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		httpError(w, 404, "not found")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(indexHTML))
}

func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/static/")
	var body []byte
	var ctype string
	switch {
	case name == "app.js":
		body, ctype = []byte(appJS), "text/javascript; charset=utf-8"
	case name == "style.css":
		body, ctype = []byte(styleCSS), "text/css; charset=utf-8"
	default:
		httpError(w, 404, "not found")
		return
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(body)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	okJSON(w, map[string]any{
		"version": env.Version,
		"docker":  engine.DockerUp(),
		"libvirt": engine.LibvirtUp(),
		"tmux":    attach.TmuxPresent(),
	})
}

func (s *Server) listSandboxes(w http.ResponseWriter, r *http.Request) {
	okJSON(w, map[string]any{"sandboxes": s.Collector.Snapshot(), "ts": nowUnix()})
}

// sandboxRoute parses "/api/sandboxes/<kind>/<name>[/<action>]".
func (s *Server) sandboxRoute(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/sandboxes/")
	parts := strings.Split(rest, "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		httpError(w, 400, "expected /api/sandboxes/<kind>/<name>")
		return
	}
	kind := parts[0]
	name := parts[1]
	action := ""
	if len(parts) >= 3 {
		action = parts[2]
	}
	sb, found := s.Collector.Find(kind, name)
	if !found {
		httpError(w, 404, fmt.Sprintf("sandbox %s/%s not found", kind, name))
		return
	}

	switch action {
	case "":
		s.detailRoute(w, r, sb)
	case "logs":
		s.logsSSE(w, r, sb)
	case "attach":
		s.attachRoute(w, r, sb)
	case "connect":
		s.connectRoute(w, r, sb)
	case "sync":
		s.syncRoute(w, r, sb)
	case "start":
		s.actStart(w, sb)
	case "stop":
		s.actStop(w, sb)
	default:
		httpError(w, 404, "unknown action "+action)
	}
}

// detail returns inspect-ish JSON for a sandbox.
func (s *Server) detailRoute(w http.ResponseWriter, r *http.Request, sb sandbox.Sandbox) {
	resp := map[string]any{"sandbox": sb}
	switch sb.Kind {
	case sandbox.KindContainer:
		if d, err := engine.InspectContainer(sb.Name); err == nil {
			resp["detail"] = d
		}
	case sandbox.KindVM:
		port := ""
		if len(sb.Ports) > 0 {
			port = strings.TrimPrefix(sb.Ports[0], "127.0.0.1:")
		}
		resp["conn"] = attach.ConnInfo(sb, "shell", port)
		// snapshot timeline — read-only listing from the kit's own storage
		if s.Collector != nil {
			if rows, e := engine.VMSnapshots(sb.Name, sb.Backend); e == nil {
				resp["snapshots"] = rows
			}
		}
	}
	// proxy wiring info (masked) so the UI can show base_url/key state
	if s.Proxy != nil {
		if tg, ok := cfgsync.LookupMasked(sb.Name); ok {
			resp["proxy"] = tg
		} else {
			resp["proxy"] = map[string]any{
				"base_url":   cfgsync.BaseURLFor(sb.Kind, sb.Backend, s.Proxy.Port()),
				"key_masked": "",
				"key":        "",
				"provider":   "",
			}
		}
	}
	okJSON(w, resp)
}

// connect rolls all connection variants (no side effects).
func (s *Server) connectRoute(w http.ResponseWriter, r *http.Request, sb sandbox.Sandbox) {
	port := ""
	if len(sb.Ports) > 0 {
		port = strings.TrimPrefix(sb.Ports[0], "127.0.0.1:")
	}
	mode := r.URL.Query().Get("mode")
	okJSON(w, attach.ConnInfo(sb, mode, port))
}

// attachRoute creates/refreshes the persistent tmux session; optionally
// spawns a Windows Terminal tab ({spawn:true}) that attaches to it.
func (s *Server) attachRoute(w http.ResponseWriter, r *http.Request, sb sandbox.Sandbox) {
	var in struct {
		Mode  string `json:"mode"`  // shell|opencode
		Spawn bool   `json:"spawn"` // open wt tab via interop
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in)
	if in.Mode == "" {
		in.Mode = "shell"
	}
	port := ""
	if len(sb.Ports) > 0 {
		port = strings.TrimPrefix(sb.Ports[0], "127.0.0.1:")
	}
	sess, err := attach.EnsureSession(sb, in.Mode, port)
	if err != nil {
		httpError(w, 500, err.Error())
		return
	}
	audit.Record("sandbox.attach", sb.Name, "mode="+in.Mode)
	ci := attach.ConnInfo(sb, in.Mode, port)
	ci.TmuxSession = sess

	spawnErr := ""
	if in.Spawn {
		var inside string
		if sb.Kind == sandbox.KindVM {
			ssh := attach.SSHCommand(sb, port)
			if in.Mode == "opencode" {
				inside = ssh + " -- 'cd /workspace 2>/dev/null; opencode'"
			} else {
				inside = ssh
			}
		} else if in.Mode == "opencode" {
			inside = ci.BackendCmd
		} else {
			inside = ci.ShellCmd
		}
		if err := attach.SpawnTerminal(inside); err != nil {
			spawnErr = err.Error()
		}
	}
	ciMap := map[string]any{
		"tmuxSession": ci.TmuxSession,
		"tmuxAttach":  ci.TmuxAttach,
		"shellCmd":    ci.ShellCmd,
		"backendCmd":  ci.BackendCmd,
		"wtCommand":   ci.WTCommand,
		"attachHttp":  ci.AttachHTTP,
		"spawnError":  spawnErr,
	}
	okJSON(w, map[string]any{"conn": ciMap, "mode": in.Mode})
}

// logsSSE streams logs/serial for a sandbox as server-sent events.
func (s *Server) logsSSE(w http.ResponseWriter, r *http.Request, sb sandbox.Sandbox) {
	flusher, ok := s.sseHeaders(w)
	if !ok {
		return
	}
	sendCmd := func(payload string) {
		fmt.Fprintf(w, "event: log\ndata: %s\n\n", jsonEscape(payload))
		flusher.Flush()
	}
	// every stream source's bytes pass through line hygiene before they
	// become SSE frames (ANSI/CR strip + blank collapse + line capping —
	// a serial firehose must not be able to poison or crush the tab)
	hyg := newLineFlusher(sendCmd)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	switch sb.Kind {
	case sandbox.KindContainer:
		// A healthy kit sandbox is silent forever: the entrypoint drops
		// privileges and sleeps, and the TUI (opencode) runs in the USER's
		// own exec PTY — docker never sees those bytes, so the raw stream
		// read as an eternal blank and looked like a broken stream. Say the
		// source out loud, prefill the sandbox's control-plane trail, then
		// keep following the raw stdout/stderr (init lines and crashes do
		// land here).
		sendCmd("(streaming docker stdout/stderr -- a healthy sandbox sleeps silently; the opencode TUI lives in your terminal: sbx attach " + sb.Name + " / docker exec -it " + sb.Name + " opencode)")
		if trail := audit.Tail(400); trail != nil {
			for _, e := range trail {
				if e.Sandbox != sb.Name {
					continue
				}
				sendCmd(e.TS + "  " + e.Kind + ": " + e.Detail)
			}
		}
		_ = engine.LogsStream(ctx, sb.Name, 300, hyg)
	case sandbox.KindVM:
		if sb.Backend == sandbox.BackendDocker {
			_ = engine.SerialStream(ctx, "sbxvmd-"+sb.Name, sb.Name, hyg)
			return
		}
		// libvirt: prefer a serial capture the daemon can actually read;
		// hosts running qemu privileged produce root:0600 captures (kit
		// pre-touch ownership is flipped by libvirt at domain start), so the
		// general fallback is the guest journal over the kit's own ssh
		// channel — retreating with backoff while the guest boots.
		if f := libvirtSerialFile(sb.Name); f != "" && engine.LibvirtSerialCapture(sb.Name) && fileReadable(f) {
			_ = engine.FileTailStream(ctx, f, hyg)
			return
		}
		first := true
		for ctx.Err() == nil {
			_ = engine.VMJournalStream(ctx, sb.Backend, sb.Name, hyg)
			if first && ctx.Err() == nil {
				sendCmd("(guest journal via ssh - streams once the guest answers; auto-retrying every few seconds while it boots)")
				first = false
			}
			select {
			case <-ctx.Done():
			case <-time.After(5 * time.Second):
			}
		}
	}
	// ctx canceled by client → handler ends the SSE stream
}

// fileReadable verifies the daemon can actually open a host file —
// ownership of libvirt-written captures is whatever the host's qemu.conf
// makes of it (root:0600 on privileged-qemu hosts like this one).
func fileReadable(p string) bool {
	fp, err := os.Open(p)
	if err != nil {
		return false
	}
	_ = fp.Close()
	return true
}

// actStart / actStop perform engine actions.
func (s *Server) actStart(w http.ResponseWriter, sb sandbox.Sandbox, _ ...any) {
	s.actGeneric(w, sb, "start")
}

func (s *Server) actStop(w http.ResponseWriter, sb sandbox.Sandbox) { s.actGeneric(w, sb, "stop") }

func (s *Server) actGeneric(w http.ResponseWriter, sb sandbox.Sandbox, what string) {
	var err error
	switch sb.Kind {
	case sandbox.KindContainer:
		if what == "stop" {
			err = engine.StopContainer(sb.Name, 20)
		} else {
			err = engine.StartContainer(sb.Name)
		}
	case sandbox.KindVM:
		backend := sb.Backend
		if what == "stop" {
			err = engine.VMStop(sb.Name, backend)
		} else {
			err = engine.VMStart(sb.Name, backend)
		}
	}
	if err != nil {
		httpError(w, 500, err.Error())
		return
	}
	// refresh promptly so the UI reflects the change
	go s.Collector.Scan()
	okJSON(w, map[string]any{"ok": true, "action": what, "name": sb.Name})
}

func (s *Server) actionStart(w http.ResponseWriter, r *http.Request) { s.actNamed(w, r, "start") }
func (s *Server) actionStop(w http.ResponseWriter, r *http.Request)  { s.actNamed(w, r, "stop") }

func (s *Server) actNamed(w http.ResponseWriter, r *http.Request, what string) {
	if r.Method != http.MethodPost {
		httpError(w, 405, "POST only")
		return
	}
	var in struct {
		Name string `json:"name"`
		Kind string `json:"kind"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in)
	if in.Name == "" {
		httpError(w, 400, "name required")
		return
	}
	sb, found := s.Collector.Find(in.Kind, in.Name)
	if !found {
		httpError(w, 404, "sandbox not found: "+in.Name)
		return
	}
	if what == "stop" {
		s.actStop(w, sb)
	} else {
		s.actStart(w, sb)
	}
}

// actionDelete: POST /api/actions/delete {"name","kind"(opt),"confirm"} —
// fully removes a sandbox: container/VM (via kit destroy semantics for VMs),
// its tmux attach session, minted keys, sync target and wallets. Container
// deletes are immediate (docker lexicon); VM deletes also wipe disk +
// snapshots, so they require {"confirm":true}.
func (s *Server) actionDelete(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name    string `json:"name"`
		Kind    string `json:"kind"`
		Confirm bool   `json:"confirm"`
	}
	if r.Method != http.MethodPost {
		httpError(w, 405, "POST only")
		return
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in)
	if in.Name == "" {
		httpError(w, 400, "name required")
		return
	}
	sb, found := s.Collector.Find(in.Kind, in.Name)
	if !found {
		httpError(w, 404, "sandbox not found: "+in.Name)
		return
	}
	// honest double-delete: the collector row can be a refresh-cycle stale
	// and `docker rm -f` treats "already gone" as success — verify the
	// engine still holds it before claiming a destructive act happened.
	alive, aerr := engine.EngineAlive(sb.Kind, sb.Name, sb.Backend)
	if aerr != nil {
		httpError(w, 500, "engine check failed: "+aerr.Error())
		return
	}
	if !alive {
		httpError(w, 404, "sandbox not found (already deleted): "+sb.Name)
		return
	}
	if sb.Kind == sandbox.KindVM && !in.Confirm {
		httpError(w, 400, "refusing to delete VM "+sb.Name+" without {\"confirm\":true} - fully wipes the VM including disk and snapshots")
		return
	}

	// credential/session/config lifecycle first (cheap, reversible state)
	notes := []string{}
	if _, err := s.Keys.Revoke(sb.Name); err == nil {
		// (Revoke audits key.revoke itself; count is visible in the CLI key ls)
	} else {
		notes = append(notes, "key revoke failed: "+err.Error())
	}
	attach.KillSession(attach.SessionName(sb.Name)) // no tmux session = no-op
	s.reloadCfg()
	if _, err := s.Cfg.RemoveWallets(sb.Name); err != nil {
		notes = append(notes, "wallet removal failed: "+err.Error())
	}
	if err := cfgsync.RemoveTarget(sb.Name); err != nil {
		notes = append(notes, "sync target removal failed: "+err.Error())
	}
	attach.KillTunnel(sb.Name)

	var err error
	switch sb.Kind {
	case sandbox.KindContainer:
		err = engine.RemoveContainer(sb.Name)
	case sandbox.KindVM:
		err = engine.VMRemove(sb.Name, sb.Backend)
	}
	if err != nil {
		httpError(w, 500, "delete failed: "+err.Error())
		return
	}
	// refresh promptly so the UI reflects the removal
	go s.Collector.Scan()
	okJSON(w, map[string]any{"ok": true, "action": "delete", "name": sb.Name, "kind": sb.Kind, "notes": notes})
}

// terminalOpen spawns a terminal from an explicit request.
func (s *Server) terminalOpen(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpError(w, 405, "POST only")
		return
	}
	var in struct {
		Name string `json:"name"`
		Kind string `json:"kind"`
		Mode string `json:"mode"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in)
	sb, found := s.Collector.Find(in.Kind, in.Name)
	if !found {
		httpError(w, 404, "sandbox not found: "+in.Name)
		return
	}
	port := ""
	if len(sb.Ports) > 0 {
		port = strings.TrimPrefix(sb.Ports[0], "127.0.0.1:")
	}
	var inside string
	ci := attach.ConnInfo(sb, in.Mode, port)
	if in.Mode == "opencode" {
		inside = ci.BackendCmd
	} else {
		inside = ci.ShellCmd
	}
	if err := attach.SpawnTerminal(inside); err != nil {
		httpError(w, 500, err.Error())
		return
	}
	audit.Record("terminal.open", sb.Name, "mode="+in.Mode)
	okJSON(w, map[string]any{"ok": true, "spawned": inside})
}

// --------------------------------------------------------- P2: proxy APIs ---

// reloadCfg re-reads the config file so CLI edits made after daemon start are
// visible immediately (stores are stateless file views).
func (s *Server) reloadCfg() {
	if cfg, err := config.Load(); err == nil {
		s.Cfg = cfg
	}
}

// syncRoute pushes the current global OpenCode settings (via the proxy)
// into a sandbox: POST /api/sandboxes/<kind>/<name>/sync {"provider": "..."}.
func (s *Server) syncRoute(w http.ResponseWriter, r *http.Request, sb sandbox.Sandbox) {
	var in struct {
		Provider string `json:"provider"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in)
	tg, path, err := s.syncSandbox(sb, in.Provider)
	if err != nil {
		httpError(w, 500, err.Error())
		return
	}
	tg.Key = "" // never echo the raw key to the dashboard
	tg.ConfigPath = path
	okJSON(w, map[string]any{"ok": true, "target": tg})
}

// syncSandbox wires a sandbox's opencode config to the sbx proxy (mints the
// key, writes the transport into the sandbox). Used by the sync REST route
// and by the create wizard (sync:true at create time).
// Callers must blank tg.Key before echoing it anywhere.
func (s *Server) syncSandbox(sb sandbox.Sandbox, provider string) (cfgsync.Target, string, error) {
	s.reloadCfg()
	// Fail before any probing/mounting/spawning: a config without providers
	// can never produce a working target, and the probe side-effect would
	// otherwise spawn an in-engine relay that exits again instantly.
	if len(s.Cfg.Opencode.Providers) == 0 {
		return cfgsync.Target{}, "", fmt.Errorf("no providers configured - run `sbxd config init` first")
	}
	port := s.Cfg.Proxy.Port
	tr, path, err := attach.WriteTransport(sb, port)
	if err != nil {
		return cfgsync.Target{}, "", err
	}
	base, via, err := attach.ResolveProxyURL(sb, s.Cfg.Proxy.Port)
	if err != nil {
		return cfgsync.Target{}, "", err
	}
	tg, err := cfgsync.Apply(sb, s.Cfg, s.Keys, tr, base, via, provider)
	if err != nil {
		return cfgsync.Target{}, "", err
	}
	return tg, path, nil
}

// usageRoute: GET /api/usage[?name=<sandbox>] — burn dashboard data.
// Overview rows carry two extra flags the UI needs:
//
//	exists — the sandbox is in the live inventory right now
//	         (stale rows = historical, greyled/hidden in the UI)
//	pinned — the user pinned it (stays visible, immune to bulk purge)
func (s *Server) usageRoute(w http.ResponseWriter, r *http.Request) {
	s.reloadCfg() // wallets live in config.json now — keep the view fresh
	name := r.URL.Query().Get("name")
	if name != "" {
		okJSON(w, s.Led.Summarize(name))
		return
	}
	resp := map[string]any{"rows": s.Led.Overview()}
	type rowBudget struct {
		ledger.Row
		Budget       float64 `json:"budget_usd"`
		BudgetTokens int     `json:"budget_tokens"`
		Exists       bool    `json:"exists"`
		Pinned       bool    `json:"pinned"`
	}
	live := map[string]bool{}
	for _, sb := range s.Collector.Snapshot() {
		live[sb.Name] = true
	}
	pinned := map[string]bool{}
	for _, p := range s.Cfg.Pinned {
		pinned[p] = true
	}
	mk := func(name string) rowBudget {
		return rowBudget{
			Budget:       s.Cfg.USDWallet(name),
			BudgetTokens: s.Cfg.TokenWallet(name),
			Exists:       live[name],
			Pinned:       pinned[name],
		}
	}
	var rows []rowBudget
	seen := map[string]bool{}
	for _, r0 := range s.Led.Overview() {
		rb := mk(r0.Sandbox)
		rb.Row = r0
		rows = append(rows, rb)
		seen[r0.Sandbox] = true
	}
	// Sandboxes that hold a wallet but have not called the proxy yet still
	// belong in the burn table (that is where wallets are set before use).
	s.Cfg.EachWallet(func(name string) {
		if !seen[name] {
			rb := mk(name)
			rb.Row = ledger.Row{Sandbox: name}
			rows = append(rows, rb)
			seen[name] = true
		}
	})
	// Sandboxes pointed at the proxy (synced) belong in the burn table the
	// moment they sync, even before their first call ($0 rows). This is
	// what makes a freshly created container "tracked" immediately.
	for _, name := range cfgsync.SyncedNames() {
		if !seen[name] {
			rb := mk(name)
			rb.Row = ledger.Row{Sandbox: name}
			rows = append(rows, rb)
			seen[name] = true
		}
	}
	// Fixed ordering, so the 5 s auto-refresh never reshuffles the table:
	// live sandboxes first (alphabetical), pasted/wallet-only extras after
	// (alphabetical), stale history rows last (alphabetical). Within a
	// group the key set changes only when a sandbox appears/vanishes.
	sort.SliceStable(rows, func(i, j int) bool {
		ai, aj := rows[i], rows[j]
		gi, gj := 2, 2 // 0 live, 1 wallet-only/synced-extra, 2 stale
		if aj.Exists {
			gj = 0
		} else if aj.Row.Calls == 0 {
			gj = 1
		}
		if ai.Exists {
			gi = 0
		} else if ai.Row.Calls == 0 {
			gi = 1
		}
		if gi != gj {
			return gi < gj
		}
		return ai.Sandbox < aj.Sandbox
	})
	resp["rows"] = rows
	okJSON(w, resp)
}

// usagePurge: POST /api/usage/purge — delete burn-ledger records.
//
//	{"sandboxes":[...]}  → delete history for exactly these names
//	{"stale_only":true}  → delete history for every UNPINNED name whose
//	                       sandbox nolonger exists (the bulk "clean stale" act)
//	{"force":true}       → override the safety refusals (still requires an
//	                       explicit act; the UI always asks twice)
//
// Refusals without force: names that still exist (live burn accounting is
// not deletable mid-flight) and pinned names (pins are the bulk-delete
// exemption). Every purge lands in the audit journal.
func (s *Server) usagePurge(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpError(w, 405, "POST only")
		return
	}
	var in struct {
		Sandboxes []string `json:"sandboxes"`
		StaleOnly bool     `json:"stale_only"`
		Force     bool     `json:"force"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&in); err != nil {
		httpError(w, 400, "invalid JSON: "+err.Error())
		return
	}
	if !in.StaleOnly && len(in.Sandboxes) == 0 {
		httpError(w, 400, "nothing to purge: {\"sandboxes\":[...]} or {\"stale_only\":true}")
		return
	}
	s.reloadCfg()
	// resolve live inventory + pinned set
	live := map[string]bool{}
	for _, sb := range s.Collector.Snapshot() {
		live[sb.Name] = true
	}
	pinned := map[string]bool{}
	for _, p := range s.Cfg.Pinned {
		pinned[p] = true
	}
	target := map[string]bool{}
	if in.StaleOnly {
		for _, r0 := range s.Led.Overview() {
			if live[r0.Sandbox] || pinned[r0.Sandbox] {
				continue
			}
			target[r0.Sandbox] = true
		}
	} else {
		for _, n := range in.Sandboxes {
			n = strings.TrimSpace(n)
			if n == "" {
				continue
			}
			if pinned[n] && !in.Force {
				httpError(w, 403, "sandbox "+n+" is pinned - unpin it first (pins survive bulk purge)")
				return
			}
			if live[n] && !in.Force {
				httpError(w, 400, "refusing to purge records for existing sandbox "+n+" - write-off history belongs to living sandboxes")
				return
			}
			target[n] = true
		}
	}
	dropped, err := s.Led.Purge(target)
	if err != nil {
		httpError(w, 500, "purge failed: "+err.Error())
		return
	}
	// The burn table also synthesizes rows from config: wallets
	// (EachWallet) and stored sync targets (cfgsync.SyncedNames). A ledger
	// purge alone can never remove such a row - the deleted sandbox would
	// resurrect on the next /api/usage. For names that no longer exist,
	// drop those leftovers as well (this mirrors the rm lifecycle; the
	// confirm dialog covers it. Live sandboxes are untouched.)
	var notes []string
	for n := range target {
		if live[n] {
			continue
		}
		if _, err := s.Cfg.RemoveWallets(n); err != nil {
			notes = append(notes, "wallets of "+n+" not removed: "+err.Error())
		}
		if err := cfgsync.RemoveTarget(n); err != nil {
			notes = append(notes, "sync target of "+n+" not removed: "+err.Error())
		}
	}
	names := make([]string, 0, len(target))
	for n := range target {
		names = append(names, n)
	}
	sort.Strings(names)
	if len(names) > 0 {
		audit.Record("ledger.purge", strings.Join(names, ","), fmt.Sprintf("%d journal rows dropped", dropped))
	}
	okJSON(w, map[string]any{"ok": true, "purged": names, "rows": dropped, "notes": notes})
}

// usagePin: POST /api/usage/pin — pin/unpin a sandbox's burn history.
// {"name":"...","pinned":true|false} → pinned names stay visible and
// survive the bulk "purge stale records". Responds with the current pin set.
func (s *Server) usagePin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpError(w, 405, "POST only")
		return
	}
	var in struct {
		Name   string `json:"name"`
		Pinned bool   `json:"pinned"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in); err != nil {
		httpError(w, 400, "invalid JSON: "+err.Error())
		return
	}
	s.reloadCfg()
	state, err := s.Cfg.SetPinned(in.Name, in.Pinned)
	if err != nil {
		httpError(w, 400, err.Error())
		return
	}
	s.reloadCfg()
	okJSON(w, map[string]any{"ok": true, "name": in.Name, "pinned": state, "pins": s.Cfg.Pinned})
}

// budgetRoute: POST /api/usage/budget {"name":"...", "usd":5, "tokens":250000}
// — either field alone is fine; a null/absent field leaves that wallet
// unchanged; 0 = unlimited. Wallets persist in config.json.
func (s *Server) budgetRoute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpError(w, 405, "POST only")
		return
	}
	var in struct {
		Name   string   `json:"name"`
		USD    *float64 `json:"usd"`
		Tokens *int     `json:"tokens"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in)
	if in.Name == "" {
		httpError(w, 400, "name required")
		return
	}
	s.reloadCfg()
	if err := s.Cfg.SetWallets(in.Name, in.USD, in.Tokens); err != nil {
		httpError(w, 500, err.Error())
		return
	}
	okJSON(w, map[string]any{
		"ok":            true,
		"name":          in.Name,
		"budget_usd":    s.Cfg.USDWallet(in.Name),
		"budget_tokens": s.Cfg.TokenWallet(in.Name),
	})
}

// createRoute: POST /api/create — the dashboard "create sandbox" wizard.
// Spawns a kit-faithful sandbox container straight through the docker CLI
// (same shape `recipe up` uses: dev.sbx labels, opencode auth mounts,
// per-stack cache volume), then applies budgets, egress and optionally
// syncs opencode to the proxy. Mounts/ports bake in at creation.
// createReq is the create-wizard request (common fields + container and
// VM specific ones; unused fields are ignored by each arm).
type createReq struct {
	Name         string            `json:"name"`
	Stack        string            `json:"stack"`
	Kind         string            `json:"kind"`       // "" | "container" | "vm"
	VMBackend    string            `json:"vm_backend"` // vm only: libvirt | docker ("" = auto)
	VMCPU        int               `json:"vm_cpu"`
	VMMemMB      int               `json:"vm_mem_mb"`
	VMDiskGB     int               `json:"vm_disk_gb"`
	VMShare      string            `json:"vm_share"`
	Mounts       []engine.Bind     `json:"mounts"`
	Ports        []string          `json:"ports"`
	Env          map[string]string `json:"env"`
	Egress       string            `json:"egress"`
	BudgetUSD    *float64          `json:"budget_usd"`
	BudgetTokens *int              `json:"budget_tokens"`
	Provider     string            `json:"provider"`
	Sync         bool              `json:"sync"`
}

func (s *Server) createRoute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpError(w, 405, "POST only")
		return
	}
	var in createReq
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		httpError(w, 400, "invalid JSON: "+err.Error())
		return
	}
	if in.Kind == "vm" {
		s.createVM(w, in)
		return
	}
	if in.Kind != "" && in.Kind != "container" {
		httpError(w, 400, "kind must be \"container\" or \"vm\"")
		return
	}
	in.Name = strings.ToLower(strings.TrimSpace(in.Name))
	if err := validSandboxName("container", in.Name); err != nil {
		httpError(w, 400, err.Error())
		return
	}
	if in.Stack == "" {
		in.Stack = "go"
	}
	if _, isStack := engine.RecipeStacks[in.Stack]; !isStack && in.Stack != "base" {
		httpError(w, 400, "unknown stack "+in.Stack+" (go|node|python|rust|base)")
		return
	}
	if in.Egress != "" && !engine.KnownEgressPreset(in.Egress) {
		httpError(w, 400, "unknown egress preset "+in.Egress)
		return
	}
	seen := map[string]bool{}
	for i := range in.Mounts {
		m := &in.Mounts[i]
		if strings.HasPrefix(m.Source, "~/") {
			home, _ := os.UserHomeDir()
			m.Source = home + strings.TrimPrefix(m.Source, "~")
		}
		if !filepath.IsAbs(m.Source) {
			httpError(w, 400, fmt.Sprintf("mount %d: source must be absolute or ~/", i))
			return
		}
		if !filepath.IsAbs(m.Target) {
			httpError(w, 400, fmt.Sprintf("mount %d: target must be an absolute container path", i))
			return
		}
		if seen[m.Target] {
			httpError(w, 400, fmt.Sprintf("mount %d: duplicate target %s", i, m.Target))
			return
		}
		seen[m.Target] = true
	}
	for _, p := range in.Ports {
		i := strings.IndexByte(p, ':')
		if i <= 0 || i == len(p)-1 {
			httpError(w, 400, fmt.Sprintf("port %q: expected HOST:CT (e.g. 8080:8080)", p))
			return
		}
	}
	cn, imgWarn, err := engine.SpawnRecipeContainer(engine.RecipeSpawn{
		Name:    in.Name,
		Stack:   in.Stack,
		Mounts:  in.Mounts,
		Env:     in.Env,
		Ports:   in.Ports,
		Project: "dashboard",
	})
	if err != nil {
		httpError(w, 500, err.Error())
		return
	}
	s.reloadCfg()
	warn := imgWarn
	if err := s.Cfg.SetWallets(cn, in.BudgetUSD, in.BudgetTokens); err != nil {
		warn = "wallets: " + err.Error()
	}
	if in.Egress != "" && in.Egress != "allow" {
		if e := engine.ApplyEgressContainer(cn, in.Egress); e != nil {
			if warn != "" {
				warn += "; "
			}
			warn += "egress: " + e.Error()
		}
	}
	if in.Sync {
		sb := sandbox.Sandbox{Kind: sandbox.KindContainer, Backend: "docker", Name: cn, State: "running"}
		if _, _, e := s.syncSandbox(sb, in.Provider); e != nil {
			if warn != "" {
				warn += "; "
			}
			warn += "sync: " + e.Error()
		}
	}
	okJSON(w, map[string]any{
		"ok":        true,
		"container": cn,
		"stack":     in.Stack,
		"mounts":    len(in.Mounts),
		"warn":      warn,
		"attach":    "sbx attach " + cn,
	})
}

// createVM: the VM arm of the create wizard — provisions a kit VM
// (libvirt `sbxvm-*` or docker-backend `sbxvmd-*`) through
// `sbxvm.sh create`. Blocks until the guest is up (minutes on first
// boot since the kit seeds, provisions and boots end-to-end); budgets
// apply identically; sync is done after the boot via the usual sync
// route/button (the VM's ssh endpoint exists once it is running).
// validSandboxName is the SINGLE name rule for both kinds (two charsets
// used to drift apart): length cap, per-kind charset, and the first char
// must be alphanumeric — an argv position is never a "--"-flag territory,
// so docker/ssh/virsh calls can't be flag-injected via a crafted name.
func validSandboxName(kind, name string) error {
	switch kind {
	case sandbox.KindContainer:
		if name == "" || len(name) > 40 {
			return errors.New("name required (a-z, 0-9, '-', max 40 chars)")
		}
		for _, c := range name {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return errors.New("name: only a-z, 0-9 and '-'")
			}
		}
		if c := name[0]; !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
			return errors.New("name must start with a letter or digit (never '-': it would parse as an engine flag)")
		}
	default: // vm (both backends)
		if name == "" || len(name) > 40 {
			return errors.New("vm name required (letters, digits, '.', '_', '-', max 40)")
		}
		if c := name[0]; !(c >= 'a' && c <= 'z') {
			return errors.New("vm name must start with a letter (kit rule)")
		}
		for _, c := range name {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '.' || c == '_') {
				return errors.New("vm name: only letters, digits and . _ - (kit rule)")
			}
		}
	}
	return nil
}

func (s *Server) createVM(w http.ResponseWriter, in createReq) {
	name := strings.ToLower(strings.TrimSpace(in.Name))
	if err := validSandboxName("vm", name); err != nil {
		httpError(w, 400, err.Error())
		return
	}
	backend := strings.TrimSpace(in.VMBackend)
	if backend == "" {
		backend = "libvirt" // kit default when both engines exist
		if !engine.LibvirtUp() {
			backend = "docker"
		}
	}
	if backend != "libvirt" && backend != "docker" {
		httpError(w, 400, "vm_backend must be \"libvirt\" or \"docker\" (kit has no other backends)")
		return
	}
	if backend == "libvirt" && !engine.LibvirtUp() {
		httpError(w, 503, "libvirt (qemu:///system) not reachable on this host - use vm_backend:\"docker\" (sbxvmd)")
		return
	}
	if backend == "docker" && !engine.DockerUp() {
		httpError(w, 503, "docker engine not reachable")
		return
	}
	if engine.VMAlive(name, backend) {
		httpError(w, 400, "VM "+name+" already exists ("+backend+" backend)")
		return
	}
	cpu, mem, disk := in.VMCPU, in.VMMemMB, in.VMDiskGB
	if cpu == 0 {
		cpu = 2
	}
	if mem == 0 {
		mem = 4096
	}
	if disk == 0 {
		disk = 20
	}
	if cpu < 1 || cpu > 16 {
		httpError(w, 400, "vm_cpu out of range (1-16)")
		return
	}
	if mem < 512 || mem > 65536 {
		httpError(w, 400, "vm_mem_mb out of range (512-65536)")
		return
	}
	if disk < 10 || disk > 512 {
		httpError(w, 400, "vm_disk_gb out of range (10-512)")
		return
	}
	share := strings.TrimSpace(in.VMShare)
	if share != "" {
		if strings.HasPrefix(share, "~/") {
			home, _ := os.UserHomeDir()
			share = home + strings.TrimPrefix(share, "~")
		}
		if fi, err := os.Stat(share); err != nil || !fi.IsDir() {
			httpError(w, 400, "vm_share must be an existing host directory (empty = VM-local /workspace)")
			return
		}
	}
	if err := engine.VMCreateSpawn(name, backend, cpu, mem, disk, share); err != nil {
		httpError(w, 500, "VM create failed: "+err.Error())
		return
	}
	warn := ""
	if in.BudgetUSD != nil || in.BudgetTokens != nil {
		s.reloadCfg()
		if err := s.Cfg.SetWallets(name, in.BudgetUSD, in.BudgetTokens); err != nil {
			warn = "wallets: " + err.Error()
		}
	}
	okJSON(w, map[string]any{
		"ok":      true,
		"kind":    "vm",
		"vm":      name,
		"backend": backend,
		"cpu":     cpu,
		"mem":     mem,
		"disk":    disk,
		"warn":    warn,
		"attach":  "sbxd attach " + name,
		"booting": true,
		"sync":    "models wire up automatically once the guest finishes booting (manual fallback: the Burn overview sync button or POST /api/sandboxes/vm/" + name + "/sync)",
	})
	// Post-boot continues in the background: the kit's `postboot` verb
	// waits for ssh + cloud-init, stages opencode (host-side, no guest
	// egress) and self-syncs the proxy via its begin/end callbacks — the
	// end callback triggers restoreVMBootSync, which now also wires an
	// unsynced VM's models. The belt-check below covers a kit-side
	// controller miss: if postboot completed, the boot marker is cleared
	// and the sync hook runs even when the kit's own end POST never
	// landed. The handler does NOT block on any of this: a create must
	// return at spawn time, not when the machine finished booting.
	go func() {
		if err := engine.VMPostBoot(name, backend); err != nil {
			audit.Record("vm.boot", name, "post-boot failed: "+err.Error())
			return
		}
		sandbox.NoteBootEnd(name)
		go s.restoreVMBootSync(name)
	}()
}

// vmBootRoute is the kit's self-registration callback: sbxvm.sh curls
// {"name":…,"phase":"begin"} when a guest boot starts and {"phase":"end"}
// once the guest is truly up (ssh + provisioning confirmed). Begin marks the
// VM as pending-boot; the collector then reports an honest "booting" state
// instead of the engine's premature "running". End clears the marker and
// re-establishes the proxy tunnel for synced VMs. The daemon listens on
// loopback only, and the kit's curl sends no Origin header — native-client
// territory the CSRF origin guard passes by design.
func (s *Server) vmBootRoute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpError(w, 405, "POST only")
		return
	}
	var in struct {
		Name  string `json:"name"`
		Phase string `json:"phase"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&in); err != nil {
		httpError(w, 400, "invalid JSON: "+err.Error())
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	// kit rule: [a-zA-Z][a-zA-Z0-9._-]* — case preserved (virsh domains are
	// case-sensitive and the kit never lowercases VM names).
	if in.Name == "" {
		httpError(w, 400, "name required")
		return
	}
	first := in.Name[0]
	if !(first >= 'a' && first <= 'z' || first >= 'A' && first <= 'Z') {
		httpError(w, 400, "bad name (kit rule: first char a letter, then a-zA-Z0-9._-)")
		return
	}
	for _, c := range in.Name[1:] {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '.' || c == '_') {
			httpError(w, 400, "bad name (kit rule: first char a letter, then a-zA-Z0-9._-)")
			return
		}
	}
	switch in.Phase {
	case "begin":
		sandbox.NoteBootBegin(in.Name)
		audit.Record("vm.boot", in.Name, "boot begin signalled (kit self-registration)")
	case "end":
		had := sandbox.NoteBootEnd(in.Name)
		audit.Record("vm.booted", in.Name, "boot confirmed (guest up, kit self-registration)")
		if had {
			go s.restoreVMBootSync(in.Name)
		}
	default:
		httpError(w, 400, `phase must be "begin" or "end"`)
		return
	}
	okJSON(w, map[string]any{"ok": true, "name": in.Name, "phase": in.Phase})
}

// restoreVMBootSync runs on the boot-end callback for exactly one sandbox:
//
//  1. Auto-wire: a VM that reaches boot-end WITHOUT a sync target has never
//     been synced (dashboard-created VMs before v0.2.11 got no sync at all;
//     the kit's own self-sync for CLI creates may have failed or found no
//     controller). One controller-side sync attempt is made — the same work
//     the dashboard sync button does: mint key + write the guest's
//     opencode.json + establish the ssh -R tunnel. Idempotent by design:
//     a CLI-create's own sync POST lands BEFORE its boot-end, so the target
//     already exists here and the attempt is skipped — no double mint.
//  2. Revive (pre-existing behavior, any kind): re-establish the
//     per-sandbox ssh -R tunnel only for synced sandboxes whose proxy
//     target is tunnel-based, and only while the sandbox is actually
//     running — so a freshly booted guest regains the proxy immediately
//     instead of waiting for the 15s ensureTunnelsLoop.
//
// Runs in a goroutine; occupies itself at worst ~25s when the inventory
// cache (5s refresh) has not yet caught up with a just-spawned VM.
func (s *Server) restoreVMBootSync(name string) {
	if s.Cfg == nil || s.Collector == nil {
		return
	}
	if sb, found := s.Collector.Find("", name); found && sb.Kind == sandbox.KindVM {
		if _, ok := cfgsync.Lookup(name); !ok {
			// Unsynced VM at boot-end: try the controller-side sync once.
			// The collector cache refreshes every 5s; give it a couple of
			// ticks if the VM was spawned seconds ago.
			for i := 0; !found && i < 5; i++ {
				time.Sleep(5 * time.Second)
				sb, found = s.Collector.Find("", name)
			}
			if found {
				if _, _, err := s.syncSandbox(sb, ""); err == nil {
					audit.Record("sandbox.sync", name, "models wired automatically at boot-end (VM had no sync target)")
				} else {
					audit.Record("sandbox.sync", name, "boot-end auto-sync failed: "+err.Error())
				}
			}
		}
	}
	tg, ok := cfgsync.Lookup(name)
	if !ok {
		return // still no target (auto-sync failed or absent, or not a VM) — nothing to revive
	}
	if !strings.Contains(tg.BaseURL, "127.0.0.1:"+s.Cfg.Proxy.Port) {
		return // not tunnel-based (direct/URL targets need nothing at boot)
	}
	if attach.TunnelAlive(name) {
		return
	}
	if sb, found := s.Collector.Find("", name); found && sb.State == "running" {
		if _, _, err := attach.EnsureTunnel(sb, s.Cfg.Proxy.Port); err == nil {
			audit.Record("relay.spawn", name, "proxy tunnel re-established after boot-end")
		}
	}
}

// fsRoute backs the dashboard's folder browser (mount/share picker):
// GET /api/fs?path=<dir> → directory entries. The browser STARTS in the
// user's home directory (the response always carries "home" so the UI can
// jump back), but any real host directory is browsable — over /mnt shares
// and other volumes included. Pseudo-filesystems (/proc, /sys, /dev) are
// refused: meaningless to mount, noisy to scroll. Symlink resolution stays
// in front of the read so a home-relative link cannot disguise its target
// — but with targets anywhere, a resolution IS the permission the picker
// needs; the loopback listener + same-origin CSRF guard remain the boundary.
func (s *Server) fsRoute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpError(w, 405, "method not allowed")
		return
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		httpError(w, 500, "home directory unknown")
		return
	}
	cur := home
	if raw := r.URL.Query().Get("path"); strings.TrimSpace(raw) != "" {
		cur = filepath.Clean(strings.TrimSpace(raw))
	}
	// Symlink-resolve BEFORE the read: the picker shows the real tree, so
	// ~/point-to-etc lands on /etc and lists /etc — never an obscured hop.
	resolvedHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		httpError(w, 500, "cannot resolve home: "+err.Error())
		return
	}
	resolved, err := filepath.EvalSymlinks(cur)
	if err != nil {
		httpError(w, 404, "cannot resolve "+cur+": "+err.Error())
		return
	}
	cur = resolved
	for _, p := range []string{"/proc", "/sys", "/dev"} {
		if cur == p || strings.HasPrefix(cur, p+string(filepath.Separator)) {
			httpError(w, 400, "cannot browse "+cur+" (pseudo-filesystem — nothing mountable in there)")
			return
		}
	}
	ents, err := os.ReadDir(cur)
	if err != nil {
		httpError(w, 404, "cannot read "+cur+": "+err.Error())
		return
	}
	type fsEnt struct {
		Name string `json:"name"`
		Dir  bool   `json:"dir"`
	}
	var dirs, files []fsEnt
	for _, e := range ents { // os.ReadDir already sorts by name; keep that order
		if e.IsDir() {
			dirs = append(dirs, fsEnt{Name: e.Name(), Dir: true})
		} else {
			files = append(files, fsEnt{Name: e.Name()})
		}
	}
	out := append(dirs, files...) // folders first for the picker
	parent := ""
	if cur != "/" {
		parent = filepath.Dir(cur)
	}
	okJSON(w, map[string]any{"ok": true, "path": cur, "home": resolvedHome, "parent": parent, "entries": out})
}

func (s *Server) configRoute(w http.ResponseWriter, r *http.Request) {
	s.reloadCfg()
	switch r.Method {
	case http.MethodGet:
		okJSON(w, s.Cfg.MaskedCopy())
	case http.MethodPost:
		var in config.Config
		dec := json.NewDecoder(io.LimitReader(r.Body, 256<<10))
		if err := dec.Decode(&in); err != nil {
			httpError(w, 400, "invalid JSON: "+err.Error())
			return
		}
		err := func() error {
			if in.Proxy.Port != "" && in.Proxy.Port == env.DefaultPort {
				return fmt.Errorf("proxy port must differ from the dashboard port")
			}
			return nil
		}()
		if err != nil {
			httpError(w, 400, err.Error())
			return
		}
		s.Cfg.MergeFrom(&in)
		if err := s.Cfg.Save(); err != nil {
			httpError(w, 500, err.Error())
			return
		}
		okJSON(w, map[string]any{"ok": true})
	default:
		httpError(w, 405, "method not allowed")
	}
}

// configImport pulls providers from the host's ~/.config/opencode/opencode.json.
func (s *Server) configImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpError(w, 405, "POST only")
		return
	}
	s.reloadCfg()
	n, err := s.Cfg.ImportHostOpenCode()
	if err != nil {
		httpError(w, 500, err.Error())
		return
	}
	if err := s.Cfg.Save(); err != nil {
		httpError(w, 500, err.Error())
		return
	}
	okJSON(w, map[string]any{"ok": true, "imported": n})
}

// --------------------------------------------------------------- events ---

// eventsSSE: subscribers get an immediate snapshot, then every
// refresh-tick broadcast until the client disconnects.
func (s *Server) eventsSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := s.sseHeaders(w)
	if !ok {
		return
	}
	sub := globalHub.subscribe()
	defer globalHub.unsubscribe(sub)
	// bare array — the exact shape the browser's snapshot listener (and every
	// later BroadcastSnapshot tick) expects; a wrapped object here would hand
	// JS an object where it wants `state.list.forEach`.
	if b, err := json.Marshal(s.Collector.Snapshot()); err == nil {
		if _, werr := fmt.Fprintf(w, "event: snapshot\ndata: %s\n\n", b); werr != nil {
			return
		}
	}
	flusher.Flush()
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-tick.C: // keepalive comment keeps idle proxies from closing
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case b, open := <-sub.ch:
			if !open {
				return
			}
			if _, err := w.Write(b); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// BroadcastSnapshot is called by the refresher loop after each scan.
// The JSON contract of a snapshot is ALWAYS a bare array — a nil slice
// would marshal to `null` and hand the browser's renderList a crash.
func BroadcastSnapshot(sb []sandbox.Sandbox) {
	if sb == nil {
		sb = []sandbox.Sandbox{}
	}
	b, _ := json.Marshal(sb)
	payload := fmt.Sprintf("event: snapshot\ndata: %s\n\n", b)
	globalHub.broadcast([]byte(payload))
}

// -------------------------------------------------------------- helpers ---

func (s *Server) sseHeaders(w http.ResponseWriter) (http.Flusher, bool) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "no flusher", 500)
		return nil, false
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	return flusher, true
}

func jsonEscape(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func okJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// libvirtSerialFile returns the kit serial log path for a VM when
// it was created in SBXVM_SERIAL_FILE mode (libvirt backend).
func libvirtSerialFile(name string) string {
	p := env.Root() + "/vm/vms/" + name + "/serial.log"
	if st, err := os.Stat(p); err == nil && !st.IsDir() {
		return p
	}
	return ""
}

func nowUnix() int64 { return time.Now().Unix() }

// writerFunc adapts a func(string) to io.Writer.
type writerFunc func(string)

func (f writerFunc) Write(p []byte) (int, error) {
	f(string(p))
	return len(p), nil
}
