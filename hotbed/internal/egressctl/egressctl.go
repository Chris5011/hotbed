// Package egressctl is the one place where egress presets meet config:
// it applies a preset to a container (engine stays config-blind) and
// records preset + DNS tracking in config.json, so the DNS fence can judge
// lookups and the daemon can re-apply fences the engine dropped (iptables
// rules live in the container netns and die on every stop/start).
//
// CLI, REST API and recipes all go through here.
package egressctl

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"hotbed/internal/audit"
	"hotbed/internal/cfgsync"
	"hotbed/internal/config"
	"hotbed/internal/dnsfence"
	"hotbed/internal/engine"
	"hotbed/internal/env"
)

// Apply installs preset on container name and persists the egress state.
// track asks for DNS tracking (honored for allow and allowlist only).
// allowlist uses the domains already in cfg.EgressAllowlist (may be empty).
// Returns the count of pre-resolved IPs (allowlist only).
func Apply(cfg *config.Config, name, preset string, track bool) (int, error) {
	preset = strings.TrimSpace(preset)
	if preset == "" {
		preset = "allow"
	}
	if !engine.KnownEgressPreset(preset) {
		return 0, fmt.Errorf("unknown egress preset %q (allow|proxy-only|none|allowlist|strict)", preset)
	}
	if preset != "allow" && preset != "allowlist" && preset != "strict" {
		track = false
	}
	if preset == "strict" {
		// refuse before anything is recorded: a refused sandbox keeps its preset
		if why := engine.StrictRefusal(name); why != "" {
			return 0, fmt.Errorf("strict %s refused: %s", name, why)
		}
	} else if cfg.EgressMode[name] == "strict" {
		// leaving: back onto the old network first, then the new preset's rules
		if err := engine.LeaveStrict(name); err != nil {
			return 0, err
		}
	}
	// record first: the fence must already know the sandbox's mode when
	// the DNAT lands, or an `allow`+track sandbox's first lookups would
	// be judged against an (empty) allowlist and refused
	if err := cfg.SetEgressState(name, preset, track); err != nil {
		return 0, fmt.Errorf("egress %s: saving config: %w", name, err)
	}
	n := 0
	var err error
	switch {
	case preset == "strict":
		err = applyStrict(cfg, name)
	case preset == "allowlist":
		ensureFence(cfg)
		n, err = engine.ApplyEgressFenceProxies(name, preset, cfg.EgressAllowlistFor(name), proxyEndpoints(cfg, name))
	case preset == "allow" && track:
		ensureFence(cfg)
		err = engine.ApplyDNSTrack(name)
	default:
		err = engine.ApplyEgressContainer(name, preset)
	}
	return n, err
}

// applyStrict brings up the enforcement side (gateway container for
// containers; the daemon's bridge listener serves libvirt guests) and
// isolates the sandbox. The daemon's strict watcher re-syncs its agents
// onto the gateway route (api.StartStrictWatch).
func applyStrict(cfg *config.Config, name string) error {
	kind, err := engine.EgressKind(name)
	if err != nil {
		return err
	}
	if kind == "container" {
		target, err := llmTarget(cfg)
		if err != nil {
			return fmt.Errorf("strict %s: %w", name, err)
		}
		if err := engine.EnsureStrictGateway(cfg.Proxy.Port, target); err != nil {
			return err
		}
	}
	return engine.ApplyStrict(name)
}

// llmTarget is the LLM proxy as the gateway container (default bridge)
// reaches it: the in-engine relay on Docker Desktop, else the daemon on
// the distro's address.
func llmTarget(cfg *config.Config) (string, error) {
	if engine.IsDockerDesktop() && !env.RelayDisabled() {
		gw, err := engine.EnsureRelay(cfg.Proxy.Port)
		if err != nil {
			return "", err
		}
		return net.JoinHostPort(gw, cfg.Proxy.Port), nil
	}
	if ip := cfgsync.HostIP(); ip != "" {
		return net.JoinHostPort(ip, cfg.Proxy.Port), nil
	}
	return "", fmt.Errorf("no address for the LLM proxy outside docker")
}

// EnsureStrictGateway re-establishes the gateway (daemon boot, new build)
// when any container sandbox is recorded strict.
func EnsureStrictGateway(cfg *config.Config) {
	for name, mode := range cfg.EgressMode {
		if mode != "strict" {
			continue
		}
		if k, err := engine.EgressKind(name); err != nil || k != "container" {
			continue
		}
		target, err := llmTarget(cfg)
		if err == nil {
			err = engine.EnsureStrictGateway(cfg.Proxy.Port, target)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "\033[33;1mhotbedd:\033[0m strict egress gateway: %v\n", err)
		}
		return
	}
}

// SetTrack switches DNS tracking for a sandbox, keeping its preset.
// Turning tracking on/off for an `allow` sandbox changes its netns (DNS
// DNAT in/out); for allowlist only the fence's record flag flips.
func SetTrack(cfg *config.Config, name string, on bool) error {
	preset := cfg.EgressMode[name]
	if preset == "" {
		preset = "allow" // never applied through hotbedd: docker default
	}
	if preset != "allow" && preset != "allowlist" && preset != "strict" {
		return fmt.Errorf("dns tracking needs the allow, allowlist or strict preset (%s is %s)", name, preset)
	}
	if preset == "allowlist" || preset == "strict" {
		if err := cfg.SetEgressState(name, preset, on); err != nil {
			return err
		}
		audit.Record("egress.track", name, fmt.Sprintf("tracking %v", on))
		return nil
	}
	_, err := Apply(cfg, name, "allow", on)
	return err
}

// AllowDomain adds a domain to the sandbox's allowlist. On an installed
// allowlist fence the domain's current IPv4s are opened right away (the
// fence admits its lookups from the next query on — config is re-read
// live — and the keeper follows up on later answers).
func AllowDomain(cfg *config.Config, name, domain string) error {
	domain = normDomain(domain)
	if domain == "" {
		return fmt.Errorf("empty domain")
	}
	list := append([]string{}, cfg.EgressAllowlistFor(name)...)
	for _, d := range list {
		if d == domain {
			return nil
		}
	}
	cfg.SetEgressAllowlist(name, append(list, domain))
	if cfg.EgressMode[name] != "allowlist" {
		return nil // list only: becomes active when the preset switches
	}
	var errs []string
	for _, ip := range resolve4(domain) {
		if err := engine.ApplyEgressIP(name, ip); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s allowed, but opening its ips failed: %s", domain, strings.Join(errs, "; "))
	}
	return nil
}

// DenyDomain removes an exact allowlist entry. On an installed allowlist
// fence the chain is rebuilt from the remaining list, which is the only
// way to drop the IP ACCEPTs the domain had collected. A domain admitted
// via a parent entry (suffix match) cannot be denied on its own.
func DenyDomain(cfg *config.Config, name, domain string) error {
	domain = normDomain(domain)
	list := cfg.EgressAllowlistFor(name)
	keep := make([]string, 0, len(list))
	found := false
	for _, d := range list {
		if d == domain {
			found = true
			continue
		}
		keep = append(keep, d)
	}
	if !found {
		if by := cfg.AllowlistCovers(name, domain); by != "" {
			return fmt.Errorf("%s is allowed via the entry %s - remove that one instead", domain, by)
		}
		return nil
	}
	cfg.SetEgressAllowlist(name, keep)
	if cfg.EgressMode[name] != "allowlist" {
		return nil
	}
	_, err := Apply(cfg, name, "allowlist", cfg.EgressTrack[name])
	return err
}

// Forget drops a deleted sandbox's egress records and tracking history.
func Forget(cfg *config.Config, name string) error {
	_ = dnsfence.ClearSeen(name)
	return cfg.ForgetEgress(name)
}

// proxyEndpoints lists the "ip:port" proxy endpoints an allowlist fence
// must keep open besides the gateway: the URL the sandbox's agents were
// synced with (any route ResolveProxyURL picked) and the gateway on the
// configured proxy.port. A hostname (host.docker.internal) is resolved
// inside the sandbox and also put on the allowlist, so its DNS lookup
// passes the fence. Loopback (VM ssh tunnels) needs nothing.
func proxyEndpoints(cfg *config.Config, name string) []string {
	set := map[string]bool{}
	if gw := engine.BridgeGateway(); gw != "" && cfg.Proxy.Port != "" {
		set[net.JoinHostPort(gw, cfg.Proxy.Port)] = true
	}
	if tg, ok := cfgsync.Lookup(name); ok {
		if u, err := url.Parse(tg.BaseURL); err == nil && u.Hostname() != "" {
			host, port := u.Hostname(), u.Port()
			if port == "" {
				port = "80"
			}
			if ip := net.ParseIP(host); ip != nil {
				if !ip.IsLoopback() {
					set[net.JoinHostPort(ip.String(), port)] = true
				}
			} else {
				for _, ip := range engine.ResolveInSandbox(name, host) {
					set[net.JoinHostPort(ip, port)] = true
				}
				if cfg.AllowlistCovers(name, host) == "" {
					cfg.SetEgressAllowlist(name, append(append([]string{}, cfg.EgressAllowlistFor(name)...), host))
					audit.Record("egress.allowlist", name, "proxy host "+host+" added (synced proxy route)")
				}
			}
		}
	}
	out := make([]string, 0, len(set))
	for ep := range set {
		out = append(out, ep)
	}
	sort.Strings(out)
	return out
}

// ensureFence makes sure a DNS fence listens where sandboxes DNAT to. On
// Docker Desktop that is the in-engine relay; elsewhere the daemon itself
// serves it (cli serve), so there is nothing to do.
func ensureFence(cfg *config.Config) {
	if env.RelayDisabled() || env.DNSFenceDisabled() || !engine.IsDockerDesktop() {
		return
	}
	if _, err := engine.EnsureRelay(cfg.Proxy.Port); err != nil {
		fmt.Fprintf(os.Stderr, "\033[33;1mhotbedd:\033[0m dns fence relay: %v\n", err)
	}
}

func normDomain(d string) string {
	d = strings.ToLower(strings.TrimSpace(d))
	d = strings.TrimSuffix(d, ".")
	return strings.TrimPrefix(d, "*.")
}

func resolve4(d string) []string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, d)
	if err != nil {
		return nil
	}
	var out []string
	for _, a := range addrs {
		if ip := a.IP.To4(); ip != nil {
			out = append(out, ip.String())
		}
	}
	return out
}

// ------------------------------------------------------------ re-applier ---

// StartReapplier watches every sandbox with a recorded preset and puts its
// rules back whenever the container (re)started without them — stop/start,
// the idle sweeper and engine restarts all wipe the netns.
func StartReapplier() {
	go func() {
		seen := map[string]string{} // name -> StartedAt already verified
		for {
			reapplyTick(seen)
			time.Sleep(30 * time.Second)
		}
	}()
}

var reapplyMu sync.Mutex

func reapplyTick(seen map[string]string) {
	reapplyMu.Lock()
	defer reapplyMu.Unlock()
	cfg, err := config.Load()
	if err != nil {
		return
	}
	for name, preset := range cfg.EgressMode {
		track := cfg.EgressTrack[name]
		if preset == "allow" && !track {
			continue // docker default: nothing to keep
		}
		// container StartedAt, docker-VM container StartedAt, or a libvirt
		// guest's boot id — whatever changes when the rules got wiped
		started, running, err := engine.EgressStartID(name)
		if err != nil || !running || seen[name] == started {
			continue // gone / booting / engine down: next tick
		}
		ok, err := engine.EgressInstalled(name, preset, track)
		if err != nil {
			continue
		}
		if !ok {
			if _, err := Apply(cfg, name, preset, track); err != nil {
				fmt.Fprintf(os.Stderr, "\033[33;1mhotbedd:\033[0m egress re-apply %s: %v\n", name, err)
				continue
			}
			audit.Record("egress.reapply", name, preset+" restored after restart")
		}
		seen[name] = started
	}
}
