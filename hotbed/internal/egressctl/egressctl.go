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
	"os"
	"strings"
	"sync"
	"time"

	"hotbed/internal/audit"
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
		return 0, fmt.Errorf("unknown egress preset %q (allow|proxy-only|none|allowlist)", preset)
	}
	if preset != "allow" && preset != "allowlist" {
		track = false
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
	case preset == "allowlist":
		ensureFence(cfg)
		n, err = engine.ApplyEgressFence(name, preset, cfg.EgressAllowlistFor(name))
	case preset == "allow" && track:
		ensureFence(cfg)
		err = engine.ApplyDNSTrack(name)
	default:
		err = engine.ApplyEgressContainer(name, preset)
	}
	return n, err
}

// SetTrack switches DNS tracking for a sandbox, keeping its preset.
// Turning tracking on/off for an `allow` sandbox changes its netns (DNS
// DNAT in/out); for allowlist only the fence's record flag flips.
func SetTrack(cfg *config.Config, name string, on bool) error {
	preset := cfg.EgressMode[name]
	if preset == "" {
		preset = "allow" // never applied through hotbedd: docker default
	}
	if preset != "allow" && preset != "allowlist" {
		return fmt.Errorf("dns tracking needs the allow or allowlist preset (%s is %s)", name, preset)
	}
	if preset == "allowlist" {
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
		out, err := engine.RunTO(8*time.Second, "docker", "inspect", "--format",
			"{{.State.Running}} {{.State.StartedAt}}", name)
		if err != nil {
			continue // gone or engine down: try again next tick
		}
		running, started, _ := strings.Cut(strings.TrimSpace(out), " ")
		if running != "true" || seen[name] == started {
			continue
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
