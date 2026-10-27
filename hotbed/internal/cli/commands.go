package cli

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"hotbed/internal/api"
	"hotbed/internal/attach"
	"hotbed/internal/audit"
	"hotbed/internal/cfgsync"
	"hotbed/internal/config"
	"hotbed/internal/dnsfence"
	"hotbed/internal/engine"
	"hotbed/internal/env"
	"hotbed/internal/keys"
	"hotbed/internal/ledger"
	"hotbed/internal/proxy"
	"hotbed/internal/sandbox"
)

// ensureTunnelsLoop keeps per-sandbox ssh -R tunnels alive for synced VMs
// (racing death on daemon restarts / ssh drops). Runs only for providers>0.
func ensureTunnelsLoop(cfg *config.Config) {
	revive := func() {
		for _, name := range cfgsync.SyncedNames() {
			if tg, ok := cfgsync.Lookup(name); !ok || !strings.Contains(tg.BaseURL, "127.0.0.1:"+cfg.Proxy.Port) {
				continue // not a tunnel-based target
			}
			if attach.TunnelAlive(name) {
				continue
			}
			if sb, found := (&sandbox.Collector{}).Find("", name); found && sb.State == "running" {
				if _, _, err := attach.EnsureTunnel(sb, cfg.Proxy.Port); err == nil {
					fmt.Printf("\033[36;1mhotbedd:\033[0m tunnel revived for %s\n", name)
				}
			}
		}
	}
	revive()
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for range t.C {
		revive()
	}
}

// --------------------------------------------------------------- serve ---

// loopbackHost reports whether a bind address is loopback — by default the
// dashboard is only reachable from this machine (NOT an auth boundary by
// itself: HOTBEDD_BIND=0.0.0.0 opts into network exposure, gated by the loud
// warning above and by the user's firewall position).
func loopbackHost(h string) bool {
	s := strings.ToLower(strings.TrimSpace(h))
	if s == "" || s == "localhost" {
		return true
	}
	if ip, err := netip.ParseAddr(s); err == nil {
		return ip.IsLoopback()
	}
	return false
}

func serve(args []string) error {
	port := env.DefaultPort
	bind := env.DefaultBind
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--port":
			if i+1 < len(args) {
				port = args[i+1]
				i++
			}
		case "--bind":
			if i+1 < len(args) {
				bind = strings.TrimSpace(args[i+1])
				i++
			}
		}
	}
	if bind == env.DefaultBind {
		if v := strings.TrimSpace(os.Getenv("HOTBEDD_BIND")); v != "" {
			bind = v
		}
	}
	tlsOn := false
	for i := 0; i < len(args); i++ {
		if args[i] == "--tls" {
			tlsOn = true
		}
	}
	if !tlsOn {
		v := strings.ToLower(strings.TrimSpace(os.Getenv("HOTBEDD_TLS")))
		tlsOn = v == "1" || v == "on" || v == "true" || v == "yes"
	}

	if n, err := config.MigrateFromKeys(); err != nil {
		return fmt.Errorf("budgets migration: %w", err)
	} else if n > 0 {
		fmt.Printf("\033[32;1mhotbedd:\033[0m migrated %d budget wallet(s) from keys.json into config.json\n", n)
	}
	cfg, err := config.Open()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	ks, err := keys.Open()
	if err != nil {
		return fmt.Errorf("keys: %w", err)
	}
	led := ledger.Open(cfg.Prices)
	px := proxy.New(cfg, ks, led)

	c := &sandbox.Collector{}
	srv := api.New(c, cfg, ks, led, px)

	// warm + refresh loop
	go func() {
		c.Scan()
		api.BroadcastSnapshot(c.Snapshot())
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for range t.C {
			c.Scan()
			api.BroadcastSnapshot(c.Snapshot())
		}
	}()

	// relay restore: EnsureRelay only ran on the first sync path, so a lost
	// relay (Docker Desktop VM restart, historical name collisions) left
	// sandboxed opencode clients retrying against a dead endpoint until a
	// re-sync. On boot, best-effort bring this state set's relay back.
	go func() {
		if env.RelayDisabled() || !engine.IsDockerDesktop() {
			return
		}
		if _, err := engine.EnsureRelay(cfg.Proxy.Port); err != nil {
			fmt.Fprintf(os.Stderr, "\033[33;1mhotbedd:\033[0m proxy relay not restored at boot: %v\n", err)
		}
	}()

	// egress keeper: installs per-IP ACCEPTs for fresh allowlist-fence DNS
	// answers and refreshes the fence's ip→sandbox attribution snapshot.
	engine.StartEgressKeeper()

	// dns fence listener (allowlist egress). On Docker Desktop the real
	// fence runs inside the engine's relay container — a host listener
	// cannot be reached by sandboxes — but we still serve one for native
	// engines and for loopback testing; a busy port just logs and moves on.
	go func() {
		if env.DNSFenceDisabled() {
			return
		}
		if err := dnsfence.Run("", cfg.Proxy.DNSPort); err != nil {
			fmt.Fprintf(os.Stderr, "\033[33;1mhotbedd:\033[0m dns fence not started here: %v\n", err)
		}
	}()

	// state-dir hygiene: tunnel pid/log files whose process is gone are
	// dead weight from rm'd sandboxes, suites or crashes - drop them.
	attach.SweepStaleTunnels()

	// the v0.5 rename: pre-rename tmux sessions ("sbx-…") are re-parented
	// under the new "hotbed-…" prefix so attach keeps finding them.
	attach.MigrateSessions()

	// idle sweeper: auto-stop kit containers whose CPU stays near zero for
	// `sweeper.idle_stop_minutes` (0 = disabled). Re-reads config each cycle
	// so `hotbedd config set` from another process applies live. Container-only
	// (labeled dev.hotbed, not hotbedvmd-* VM containers); stop-only; audit-logged.
	go func() {
		type low struct{ count int }
		lows := map[string]*low{}
		const sampleEvery = 30 * time.Second
		dbg := os.Getenv("HOTBEDD_SWEEPER_DEBUG") != ""
		for {
			time.Sleep(sampleEvery)
			scfg, err := config.Open()
			if err != nil || scfg.IdleStopMinutes() <= 0 {
				if dbg {
					fmt.Fprintf(os.Stderr, "sweeper: disabled (err=%v)\n", err)
				}
				continue
			}
			labels, lerr := engine.ListSandboxContainers()
			if lerr != nil || len(labels) == 0 {
				if dbg {
					fmt.Fprintf(os.Stderr, "sweeper: no labeled containers (err=%v)\n", lerr)
				}
				continue
			}
			st := engine.DockerStats()
			seen := map[string]bool{}
			for _, r := range labels {
				if r.State != "running" || strings.HasPrefix(r.Names, env.VMDockerPrefix) {
					continue
				}
				if env.ForeignOwner(r.Labels) {
					continue // sandbox owned by another daemon (hermetic suites)
				}
				seen[r.Names] = true
				s := st[r.Names]
				cpu := parseCPUPercent(s.CPUPerc)
				lo := lows[r.Names]
				if lo == nil {
					lo = &low{}
					lows[r.Names] = lo
				}
				if cpu < 1.0 {
					lo.count++
				} else {
					lo.count = 0
				}
				need := scfg.IdleStopMinutes() * 60 / int(sampleEvery.Seconds())
				if dbg {
					fmt.Fprintf(os.Stderr, "sweeper: %s cpu=%.2f%% low=%d need=%d\n", r.Names, cpu, lo.count, need)
				}
				if lo.count >= need {
					if err := engine.StopContainer(r.Names, 20); err == nil {
						audit.Record("sweeper.stop", r.Names,
							fmt.Sprintf("cpu<1%% for %dm", scfg.IdleStopMinutes()))
						ok("sweeper stopped idle sandbox %s (CPU < 1%% for %s)\n",
							r.Names, time.Duration(scfg.IdleStopMinutes())*time.Minute)
					}
					lo.count = 0
				}
			}
			for name := range lows { // forget containers that vanished
				if !seen[name] {
					delete(lows, name)
				}
			}
		}
	}()
	if cfg.IdleStopMinutes() > 0 {
		fmt.Fprintf(os.Stderr, "\033[36;1mhotbedd:\033[0m sweeper active: stops kit sandboxes idle ≥%dm (config sweeper.idle_stop_minutes)\n", cfg.IdleStopMinutes())
	}

	// LLM proxy listener (reachable from sandboxes by design)
	if len(cfg.Opencode.Providers) > 0 {
		go func() {
			httpSrv := &http.Server{Addr: px.Addr(), Handler: px, ReadHeaderTimeout: 15 * time.Second}
			if e := httpSrv.ListenAndServe(); e != nil && e != http.ErrServerClosed {
				fmt.Fprintf(os.Stderr, "\033[31;1mhotbedd:\033[0m proxy disabled: %v\n", e)
			}
		}()
		go ensureTunnelsLoop(cfg)
	}

	addr := bind + ":" + port
	if !loopbackHost(bind) {
		fmt.Fprintf(os.Stderr, "\033[33;1mhotbedd:\033[0m WARNING: dashboard/API listening on %s — UNAUTHENTICATED on the network; the CSRF guard protects browsers only, native clients pass — gate it (firewall) or use an ssh tunnel\n", addr)
	}
	scheme := "http"
	httpSrv := &http.Server{Addr: addr, Handler: srv.Handler(), IdleTimeout: 90 * time.Second, ReadHeaderTimeout: 15 * time.Second}
	if tlsOn {
		crt, tlsPath, fp, err := api.SnakeoilTLS(bind)
		if err != nil {
			return fmt.Errorf("TLS: %w", err)
		}
		scheme = "https"
		fmt.Printf("\033[33;1mhotbedd:\033[0m snakeoil TLS — self-signed (browser warns once); cert pinned at %s (delete to regenerate)\n", tlsPath)
		fmt.Printf("\033[33;1mhotbedd:\033[0m cert sha256 fingerprint: %s\n", fp)
		httpSrv.TLSConfig = &tls.Config{Certificates: []tls.Certificate{crt}}
	}

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(ctx)
	}()

	if loopbackHost(bind) {
		fmt.Printf("\033[36;1mhotbedd:\033[0m dashboard %s://%s  (Ctrl-C to stop)\n", scheme, addr)
	} else {
		fmt.Printf("\033[36;1mhotbedd:\033[0m dashboard %s://<this-host>:%s  (listening on %s; Ctrl-C to stop)\n", scheme, port, addr)
	}
	if len(cfg.Opencode.Providers) > 0 {
		fmt.Printf("\033[36;1mhotbedd:\033[0m LLM proxy    :%s  (%d upstream provider(s), wallets from config.json)\n",
			cfg.Proxy.Port, len(cfg.Opencode.Providers))
	} else {
		fmt.Printf("\033[33;1mhotbedd:\033[0m LLM proxy idle — no providers yet: run `hotbedd config init`\n")
	}
	if tlsOn {
		if err := httpSrv.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
			return err
		}
		return nil
	}
	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// ------------------------------------------------------- idle sweeper ----

// parseCPUPercent converts docker stats' CPU "0.15%" to a float.
func parseCPUPercent(s string) float64 {
	v, _ := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(s), "%"), 64)
	return v
}

// ------------------------------------------------------------ daemon client ---

func daemonUp() bool {
	c := http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := c.Get(daemonBase() + "/api/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == 200
}

func daemonBase() string {
	port := os.Getenv("HOTBEDD_PORT")
	if port == "" {
		port = env.DefaultPort
	}
	return "http://127.0.0.1:" + port
}

func daemonJSON(path string, into any) bool {
	c := http.Client{Timeout: 4 * time.Second}
	resp, err := c.Get(daemonBase() + path)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return false
	}
	return json.NewDecoder(resp.Body).Decode(into) == nil
}

// ------------------------------------------------------------------- ps ---

func ps(_ []string) error       { return psStats(false) }
func statsCmd(_ []string) error { return psStats(true) }

func psStats(withStats bool) error {
	var list []sandbox.Sandbox
	if daemonUp() {
		var resp struct {
			Sandboxes []sandbox.Sandbox `json:"sandboxes"`
		}
		if !daemonJSON("/api/sandboxes", &resp) {
			return fmt.Errorf("daemon responded oddly")
		}
		list = resp.Sandboxes
	} else {
		sc := &sandbox.Collector{}
		list = sc.Scan()
		// one-shot CLI: take a second sample so live CPU% has a delta to measure
		if withStats && anyRunningVM(list) {
			time.Sleep(1200 * time.Millisecond)
			list = sc.Scan()
		}
	}
	if len(list) == 0 {
		fmt.Println("no sandboxes (hbd | hotbedvm create …)")
		return nil
	}
	w := func(s string, n int) string {
		if len(s) >= n {
			return s
		}
		return s + strings.Repeat(" ", n-len(s))
	}
	fmt.Printf("%-40s %-9s %-9s %-10s %s\n", "NAME", "KIND", "STATE", "CPU/MEM", "PROJECT")
	for _, s := range list {
		nice := wsKind(s)
		res := ""
		if withStats && (s.CPUPct > 0 || s.MemUsage != "") {
			cpu := "—"
			if s.CPUPct > 0 {
				cpu = fmt.Sprintf("%.0f%%", s.CPUPct)
			}
			res = fmt.Sprintf("%s %s", cpu, one(s.MemUsage))
		}
		fmt.Printf("%-40s %-9s %-9s %-10s %s\n", w(s.Name, 40), w(nice, 9),
			w(s.State, 9), w(res, 10), homeShort(s.Project))
	}
	return nil
}

func one(s string) string {
	if i := strings.Index(s, " / "); i > 0 {
		return s[:i]
	}
	return s
}

func wsKind(s sandbox.Sandbox) string {
	switch s.Kind {
	case sandbox.KindContainer:
		return "container"
	case sandbox.KindVM:
		return "vm:" + s.Backend
	}
	return s.Kind
}

// --------------------------------------------------------------- events ---

func eventsCmd(_ []string) error {
	resp, err := http.Get(daemonBase() + "/api/events")
	if err != nil {
		return fmt.Errorf("daemon not reachable (hotbedd serve): %w", err)
	}
	defer resp.Body.Close()
	r := bufioRead(resp.Body)
	for line := range r {
		if strings.HasPrefix(line, "data: ") {
			fmt.Println(line[6:])
		}
	}
	return nil
}

// ----------------------------------------------------------------- logs ---

func logsCmd(args []string) error {
	name := firstArg(args)
	if name == "" {
		return fmt.Errorf("usage: hotbedd logs NAME")
	}
	sb := findLocal(name)
	if sb == nil {
		return fmt.Errorf("sandbox %q not found", name)
	}
	if daemonUp() {
		resp, err := http.Get(daemonBase() + "/api/sandboxes/" + sb.Kind + "/" + sb.Name + "/logs")
		if err == nil && resp.StatusCode == 200 {
			defer resp.Body.Close()
			for line := range bufioRead(resp.Body) {
				if strings.HasPrefix(line, "data: ") {
					fmt.Println(line[6:])
				}
			}
			return nil
		}
	}
	// standalone fallbacks
	switch sb.Kind {
	case sandbox.KindContainer:
		c := exec.Command("docker", "logs", sb.Name)
		c.Stdout, c.Stderr = os.Stdout, os.Stdout
		return c.Run()
	case sandbox.KindVM:
		if sb.Backend == sandbox.BackendDocker {
			port := engine.DockerPort("hotbedvmd-" + sb.Name)
			if port == "" {
				return fmt.Errorf("VM stopped — start it first")
			}
			c := exec.Command("ssh", "-i", os.Getenv("HOME")+"/.ssh/hotbedvm_key",
				"-o", "StrictHostKeyChecking=accept-new", "-p", port, "dev@127.0.0.1", "echo connected")
			c.Stdout, c.Stderr = os.Stdout, os.Stdout
			return c.Run()
		}
		return fmt.Errorf("libvirt VM: use `virsh console hotbedvm-%s` or HOTBEDVM_SERIAL_FILE mode", sb.Name)
	}
	return nil
}

// --------------------------------------------------------------- attach ---

func attachCmd(args []string) error {
	mode := "shell"
	if hasFlag(args, "--oc") || hasFlag(args, "--opencode") {
		mode = "opencode"
	}
	name := firstArg(args)
	if name == "" {
		return fmt.Errorf("usage: hotbedd attach NAME [--oc]")
	}
	sb := findLocal(name)
	if sb == nil {
		return fmt.Errorf("sandbox %q not found", name)
	}
	port := ""
	if len(sb.Ports) > 0 {
		port = strings.TrimPrefix(sb.Ports[0], "127.0.0.1:")
	}
	sess, err := attach.EnsureSession(*sb, mode, port)
	if err != nil {
		return err
	}
	ok("tmux session %s ready (mode=%s). Detach with Ctrl-b d.\n", sess, mode)
	tmux := exec.Command("tmux", "attach", "-t", sess)
	tmux.Stdin, tmux.Stdout, tmux.Stderr = os.Stdin, os.Stdout, os.Stderr
	return tmux.Run()
}

// -------------------------------------------------------------- connect ---

func connectCmd(args []string) error {
	name := firstArg(args)
	sb := findLocal(name)
	if sb == nil {
		return fmt.Errorf("sandbox %q not found", name)
	}
	port := ""
	if len(sb.Ports) > 0 {
		port = strings.TrimPrefix(sb.Ports[0], "127.0.0.1:")
	}
	ci := attach.ConnInfo(*sb, "shell", port)
	ws := func(k, v string) {
		if strings.TrimSpace(v) != "" {
			fmt.Printf("%-20s %s\n", k+":", v)
		}
	}
	if sb.Kind == sandbox.KindVM {
		ws("ssh", ci.ShellCmd)
	} else {
		ws("shell", ci.ShellCmd)
	}
	ws("opencode", ci.BackendCmd)
	ws("windows", ci.WTCommand)
	ws("attach-tmux", ci.TmuxAttach)
	return nil
}

// ----------------------------------------------------------- start/stop ---

func lifeCmd(args []string, what string) error {
	name := firstArg(args)
	if name == "" {
		return fmt.Errorf("usage: hotbedd %s NAME", what)
	}
	if daemonUp() {
		body := strings.NewReader(`{"name":"` + name + `","kind":""}`)
		resp, err := http.Post(daemonBase()+"/api/actions/"+what, "application/json", body)
		if err == nil {
			defer resp.Body.Close()
			var j struct {
				Error, Name string
			}
			err := json.NewDecoder(resp.Body).Decode(&j)
			if err == nil && resp.StatusCode == 200 && j.Error == "" {
				ok("%s %s\n", what, j.Name)
				return nil
			}
			if j.Error != "" {
				ok("engine %s: %s (falling back to local)\n", what, j.Error)
			}
		}
		// fall through to local path on any daemon hiccup
	}
	sb := findLocal(name)
	if sb == nil {
		return fmt.Errorf("sandbox %q not found", name)
	}
	var err error
	switch sb.Kind {
	case sandbox.KindContainer:
		if what == "stop" {
			err = engine.StopContainer(sb.Name, 20)
		} else {
			err = engine.StartContainer(sb.Name)
		}
	case sandbox.KindVM:
		if what == "stop" {
			err = engine.VMStop(sb.Name, sb.Backend)
		} else {
			err = engine.VMStart(sb.Name, sb.Backend)
		}
	}
	if err != nil {
		return err
	}
	ok("%s %s\n", what+"ed", sb.Name)
	return nil
}

// rmCmd: `hotbedd rm NAME [--yes|-y]` — fully delete a sandbox. Talks to the
// daemon when it runs (same delete route as the dashboard); otherwise does
// the local engine path directly. VMs wipe disk + snapshots and therefore
// need --yes (or an interactive confirm).
func rmCmd(args []string) error {
	yes := hasFlag(args, "--yes") || hasFlag(args, "-y")
	name := firstArg(args)
	if name == "" {
		return fmt.Errorf("usage: hotbedd rm NAME [--yes]")
	}
	if daemonUp() {
		body := fmt.Sprintf(`{"name":%q,"kind":"","confirm":%v}`, name, yes)
		resp, err := http.Post(daemonBase()+"/api/actions/delete", "application/json", strings.NewReader(body))
		if err == nil {
			defer resp.Body.Close()
			var j struct {
				Error string `json:"error"`
				Name  string `json:"name"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&j); err == nil {
				if j.Error == "" && resp.StatusCode == 200 {
					ok("deleted %s\n", j.Name)
					return nil
				}
				if j.Error != "" && !strings.Contains(j.Error, "sandbox not found") {
					return fmt.Errorf("%s", j.Error)
				}
			}
		}
		// fall through to the local path on daemon hiccup / not-found
	}
	sb := findLocal(name)
	if sb == nil {
		return fmt.Errorf("sandbox %q not found", name)
	}
	if sb.Kind == sandbox.KindVM && !yes {
		return fmt.Errorf("refusing to delete VM %s without --yes — fully wipes the VM including disk and snapshots", sb.Name)
	}
	// lifecycle trimmings (keys, session, wallets, sync target)
	_, _ = keys.New().Revoke(sb.Name)
	attach.KillSession(attach.SessionName(sb.Name))
	cfg, cfgErr := config.Open()
	if cfgErr == nil {
		_, _ = cfg.RemoveWallets(sb.Name)
	}
	_ = cfgsync.RemoveTarget(sb.Name)

	var err error
	switch sb.Kind {
	case sandbox.KindContainer:
		err = engine.RemoveContainer(sb.Name)
	case sandbox.KindVM:
		err = engine.VMRemove(sb.Name, sb.Backend)
	}
	if err != nil {
		return err
	}
	ok("deleted %s\n", sb.Name)
	return nil
}

// ----------------------------------------------------------------- doctor ---

func doctorCmd(_ []string) error {
	okf := func(b bool, name string) {
		mark, col := "✔", "32"
		if !b {
			mark, col = "✘", "31"
		}
		fmt.Printf("  \033[%sm%s\033[0m %s\n", col, mark, name)
	}
	fmt.Println("hotbedd doctor")
	docker := engine.DockerUp()
	libvirt := engine.LibvirtUp()
	tmux := attach.TmuxPresent()
	interop := engine.LookPath("wt.exe") || engine.LookPath("cmd.exe")
	okf(docker, "docker engine")
	okf(libvirt, "libvirt (system)")
	okf(tmux, "tmux (attach sessions)")
	okf(interop, "WSL→Windows interop (wt.exe/cmd.exe)")
	if docker && engine.IsDockerDesktop() {
		if env.RelayDisabled() {
			fmt.Println("  \033[36m=\033[0m proxy relay disabled (HOTBEDD_RELAY_DISABLE=1) — containers must reach the distro natively")
		} else {
			fmt.Printf("  \033[36m=\033[0m proxy relay: %s\n", engine.RelayStatus())
		}
	}

	// LLM proxy (P2): healthy when it answers 401 (auth enforced) on its port
	cfg, _, _, err := p2Stores()
	proxyOK := false
	proxyMsg := "no providers configured (hotbedd config init)"
	if err == nil && cfg != nil && len(cfg.Opencode.Providers) > 0 {
		proxyMsg = fmt.Sprintf(":%s (%d provider(s), wallets from config.json)", cfg.Proxy.Port, len(cfg.Opencode.Providers))
		client := &http.Client{Timeout: 700 * time.Millisecond}
		if resp, e := client.Get("http://127.0.0.1:" + cfg.Proxy.Port + "/v1"); e == nil && resp.StatusCode == http.StatusUnauthorized {
			proxyOK = true
		}
	}
	okf(proxyOK, "LLM proxy "+proxyMsg)

	if daemonUp() {
		okf(true, "hotbedd daemon (dashboard at "+daemonBase()+")")
	} else {
		fmt.Printf("  \033[33m!\033[0m daemon not running — start with: hotbedd serve\n")
	}
	if !(docker || libvirt) {
		return fmt.Errorf("neither docker nor libvirt reachable")
	}
	return nil
}

// ----------------------------------------------------------------- utils ---

// anyRunningVM reports whether any libvirt VM is running (VM CPU% needs
// two domstats samples, unlike instantaneous docker stats).
func anyRunningVM(list []sandbox.Sandbox) bool {
	for _, s := range list {
		if s.Kind == sandbox.KindVM && strings.EqualFold(s.State, "running") {
			return true
		}
	}
	return false
}

// homeShort replaces the user's home prefix with ~ for display.
func homeShort(s string) string {
	if h, err := os.UserHomeDir(); err == nil && h != "" && h != "/" {
		return strings.ReplaceAll(s, h, "~")
	}
	return s
}

// findLocal scans the inventory without needing the daemon.
func findLocal(name string) *sandbox.Sandbox {
	sc := &sandbox.Collector{}
	list := sc.Scan()
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	for i := range list {
		if list[i].Name == name {
			return &list[i]
		}
	}
	return nil
}

// bufioRead reads an SSE body line by line.
func bufioRead(r io.Reader) chan string {
	out := make(chan string)
	go func() {
		buf := make([]byte, 0, 64*1024)
		tmp := make([]byte, 4096)
		for {
			n, err := r.Read(tmp)
			if n > 0 {
				buf = append(buf, tmp[:n]...)
				for {
					i := indexByte(buf, '\n')
					if i < 0 {
						break
					}
					out <- string(buf[:i])
					buf = append(buf[:0], buf[i+1:]...)
				}
			}
			if err != nil {
				break
			}
		}
		close(out)
	}()
	return out
}

func indexByte(b []byte, c byte) int {
	for i := range b {
		if b[i] == c {
			return i
		}
	}
	return -1
}

func ok(format string, args ...any) {
	fmt.Printf("\033[32;1mhotbedd:\033[0m "+format, args...)
}
