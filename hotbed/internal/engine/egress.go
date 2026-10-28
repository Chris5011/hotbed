package engine

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"hotbed/internal/audit"
	"hotbed/internal/env"
)

// Egress presets for containers.
//
//	allow        no rules (Docker default: all outbound permitted)
//	proxy-only   loopback + established + private ranges (RFC1918, docker
//	             bridge) + DNS stay open; public destinations drop — LLM
//	             traffic must ride the hbd proxy on the host side
//	none         loopback + established only; everything else drops
//	allowlist    none, plus: all container :53 traffic DNAT'ed into the
//	             fence listener (gw:dns_port) which only answers domains
//	             from the sandbox's config allowlist; every resolved IP it
//	             answers is ACCEPT'ed in the sandbox netns (on install the
//	             domains are pre-resolved so the fence is complete at
//	             minute zero), everything else keeps dropping. The hosted
//	             LLM proxy (gw:proxy_port) is kit-internal, bearer-key
//	             gated and always accepted here, so a synced opencode
//	             works without listing its provider domain.
//
// Applied via `docker exec … iptables` when the sandbox can run iptables
// (has the binary and NET_ADMIN); otherwise through a privileged helper
// container sharing the sandbox netns (image hotbedd-egress-helper:1, built
// once on demand — its build needs outbound in the window before any
// fence exists). Rules live in the container netns, so they vanish on
// recreation; callers re-apply after `mount add/rm` and after any spawn.
// The DNS fence itself runs inside the engine netns (proxy relay /
// daemon — see dnsfence) with the state-dir event log as their shared
// hand-off. This is a convenience fence, not a security boundary.

// KnownEgressPreset reports whether the preset string is valid ("" = allow).
func KnownEgressPreset(preset string) bool {
	return egressPresets[strings.TrimSpace(preset)]
}

var egressPresets = map[string]bool{
	"allow":      true,
	"proxy-only": true,
	"none":       true,
	"allowlist":  true,
}

const (
	egressChain       = "HBD_EGRESS"
	egressNatChain    = "HBD_DNS"
	egressHelperImage = "hotbedd-egress-helper:1"
	// egressBinCheck fails the whole script fast when iptables is missing
	// (otherwise suppression tricks would report rc=0 with empty output and
	// hide "fence not installed" as if it were "everything allowed").
	egressBinCheck = `command -v iptables >/dev/null 2>&1 || { echo HBD_NO_IPTABLES; exit 64; }; `
)

// egressScript installs the chain for a preset, idempotently: fresh chain
// (create-or-flush), fill, then (re)install the OUTPUT jump at position 1.
func egressScript(preset string) string {
	preset = strings.TrimSpace(preset)
	if preset == "" {
		preset = "allow"
	}
	var rules []string
	rules = append(rules,
		"-o lo -j ACCEPT",
		"-m state --state ESTABLISHED,RELATED -j ACCEPT")
	switch preset {
	case "proxy-only":
		// docker bridge ranges + RFC1918 internals + DNS stay reachable
		rules = append(rules,
			"-d 172.16.0.0/12 -j ACCEPT",
			"-d 10.0.0.0/8 -j ACCEPT",
			"-d 192.168.0.0/16 -j ACCEPT",
			"-p udp --dport 53 -j ACCEPT",
			"-p tcp --dport 53 -j ACCEPT")
	case "allowlist":
		// the fence listener + the hosted LLM proxy on the bridge gateway
		// stay reachable (DNS is DNAT'ed into the fence via the nat side;
		// the proxy is kit-internal, bearer-key gated and not "internet" —
		// synced opencode must reach it, else a synced allowlist sandbox
		// could never use its provider); resolved allowlist
		// IPs are appended by the caller
		rules = append(rules,
			"-d "+dnsFenceGW()+" -p udp --dport "+dnsFencePort()+" -j ACCEPT",
			"-d "+dnsFenceGW()+" -p tcp --dport "+dnsFencePort()+" -j ACCEPT",
			"-d "+dnsFenceGW()+" -p tcp --dport "+proxyFencePort()+" -j ACCEPT")
	}
	rules = append(rules, "-j DROP")

	var b strings.Builder
	b.WriteString(egressBinCheck)
	fmt.Fprintf(&b, "iptables -N %s 2>/dev/null; iptables -F %s 2>/dev/null; ", egressChain, egressChain)
	for _, r := range rules {
		fmt.Fprintf(&b, "iptables -A %s %s; ", egressChain, r)
	}
	fmt.Fprintf(&b, "iptables -D OUTPUT -j %s 2>/dev/null; iptables -I OUTPUT 1 -j %s", egressChain, egressChain)
	if preset == "allowlist" {
		b.WriteString(egressDNATScript())
	}
	return b.String()
}

// egressDNATScript builds the allowlist nat side: all container :53 traffic
// is redirected into the fence listener on gw:dns_port (iptables only —
// resolver file ops run separately via plain exec, see egressResolverExec).
func egressDNATScript() string {
	return fmt.Sprintf(
		"iptables -t nat -N %s 2>/dev/null; iptables -t nat -F %s 2>/dev/null; "+
			"iptables -t nat -A %s -p udp --dport 53 -j DNAT --to-destination %s:%s; "+
			"iptables -t nat -A %s -p tcp --dport 53 -j DNAT --to-destination %s:%s; "+
			"iptables -t nat -D OUTPUT -j %s 2>/dev/null; iptables -t nat -I OUTPUT 1 -j %s",
		egressNatChain, egressNatChain,
		egressNatChain, dnsFenceGW(), dnsFencePort(),
		egressNatChain, dnsFenceGW(), dnsFencePort(),
		egressNatChain, egressNatChain)
}

// egressResolverInstall / egressResolverRestore are the resolver file ops
// for the allowlist fence. They run via PLAIN docker exec (the sandbox's
// own mount ns) — the netns helper shares nothing of the rootfs, so letting
// it rewrite files would silently corrupt the wrong /etc/resolv.conf.
func egressResolverInstall() string {
	return fmt.Sprintf(
		"cat /etc/resolv.conf > /etc/resolv.conf.hbd-bak 2>/dev/null; "+
			"printf 'nameserver %s\\noptions ndots:1 timeout:2 attempts:4\\n' > /etc/resolv.conf; true",
		dnsFenceGW())
}

func egressResolverRestore() string {
	return "if [ -f /etc/resolv.conf.hbd-bak ]; then " +
		"cat /etc/resolv.conf.hbd-bak > /etc/resolv.conf 2>/dev/null; fi; true"
}

// egressResolverExec runs a resolver script in the sandbox's own mount ns.
// Best-effort: shell-less containers skip it instead of failing the whole
// fence (the nat side DNATs every :53 query anyway, so routers with an
// unrewritten resolv.conf still land in the fence).
func egressResolverExec(name, script string) error {
	_, err := RunTO(20*time.Second, "docker", "exec", "-u", "root", name, "sh", "-c", script)
	return err
}

// egressAllowlistScript is the full allowlist iptables setup: base chain +
// one ACCEPT per pre-resolved IP + nat side. Resolver file ops run via
// egressResolverExec separately (plain exec, own mount ns).
func egressAllowlistScript(ips []string) string {
	var b strings.Builder
	b.WriteString(egressBinCheck)
	fmt.Fprintf(&b, "iptables -N %s 2>/dev/null; iptables -F %s 2>/dev/null; ", egressChain, egressChain)
	fmt.Fprintf(&b, "iptables -A %s -o lo -j ACCEPT; ", egressChain)
	fmt.Fprintf(&b, "iptables -A %s -m state --state ESTABLISHED,RELATED -j ACCEPT; ", egressChain)
	for _, ip := range ips {
		fmt.Fprintf(&b, "iptables -A %s -d %s -j ACCEPT; ", egressChain, ip)
	}
	fmt.Fprintf(&b, "iptables -A %s -d %s -p udp --dport %s -j ACCEPT; ", egressChain, dnsFenceGW(), dnsFencePort())
	fmt.Fprintf(&b, "iptables -A %s -d %s -p tcp --dport %s -j ACCEPT; ", egressChain, dnsFenceGW(), dnsFencePort())
	fmt.Fprintf(&b, "iptables -A %s -d %s -p tcp --dport %s -j ACCEPT; ", egressChain, dnsFenceGW(), proxyFencePort())
	fmt.Fprintf(&b, "iptables -A %s -j DROP; ", egressChain)
	fmt.Fprintf(&b, "iptables -D OUTPUT -j %s 2>/dev/null; iptables -I OUTPUT 1 -j %s; ", egressChain, egressChain)
	b.WriteString(egressDNATScript())
	return b.String()
}

// egressResetScript returns to the unfenced default (removes jump + chain).
// The trailing `true` makes the script succeed even when nothing existed
// yet. Resolver restore runs via egressResolverExec (own mount ns).
func egressResetScript() string {
	return egressBinCheck + fmt.Sprintf(
		"iptables -D OUTPUT -j %s 2>/dev/null; iptables -F %s 2>/dev/null; iptables -X %s 2>/dev/null; "+
			"iptables -t nat -D OUTPUT -j %s 2>/dev/null; iptables -t nat -F %s 2>/dev/null; iptables -t nat -X %s 2>/dev/null; true",
		egressChain, egressChain, egressChain,
		egressNatChain, egressNatChain, egressNatChain)
}

// dnsFenceGW is the DNAT target address (the sandboxed gateway where the
// fence listener lives). Empty gw → allowlist cannot be installed.
func dnsFenceGW() string { return BridgeGateway() }

// dnsFencePort is the fence's listen port; HOTBEDD_DNS_PORT overrides
// (must match the fence runner and requires a relay/daemon restart — an
// advanced knob, hence env rather than config: engine stays config-blind).
func dnsFencePort() string {
	if v := strings.TrimSpace(os.Getenv("HOTBEDD_DNS_PORT")); v != "" {
		return v
	}
	return env.DefaultDNSPort
}

// proxyFencePort is the hosted LLM proxy's listen port, accepted alongside
// the fence listener in the allowlist preset. Same reasoning as the DNS
// port: HOTBEDD_PROXY_PORT overrides for non-default config proxy.port
// setups (advanced knob; must match the actual proxy listener).
func proxyFencePort() string {
	if v := strings.TrimSpace(os.Getenv("HOTBEDD_PROXY_PORT")); v != "" {
		return v
	}
	return env.DefaultProxyPort
}

// ApplyEgressFence installs/removes outbound rules inside a container,
// resolving allowlist domains up front so the fence is complete at minute
// zero. preset "allowlist" requires domains (from config.EgressAllowlist);
// other presets ignore domains. Returns the count of resolved IPv4s.
func ApplyEgressFence(name, preset string, domains []string) (int, error) {
	preset = strings.TrimSpace(preset)
	if preset == "" {
		preset = "allow"
	}
	if preset == "allowlist" {
		gw := BridgeGateway()
		if gw == "" {
			return 0, fmt.Errorf("allowlist fence %s: no default docker bridge gateway", name)
		}
		// an empty list is legal: only the fence and the hosted proxy stay
		// reachable — the "start closed, allow what the DNS tab shows"
		// workflow begins exactly here
		norm := normalizeDomains(domains)
		var ips []string
		resolved := map[string]bool{}
		for _, d := range norm {
			for _, a := range lookupIP(d) {
				if a == nil {
					continue
				}
				if ip := a.To4(); ip != nil && !resolved[ip.String()] {
					resolved[ip.String()] = true
					ips = append(ips, ip.String())
				}
			}
		}
		script := egressAllowlistScript(ips)
		forgetApplied(name) // the chain is rebuilt: keeper must re-open fresh answers
		if out, err := egressRun(name, script); err != nil {
			return len(ips), fmt.Errorf("egress %s → allowlist: %w: %s", name, err, strings.TrimSpace(out))
		}
		if err := egressResolverExec(name, egressResolverInstall()); err != nil {
			// non-fatal (the nat side catches unrewritten resolvers too)
			fmt.Fprintf(os.Stderr, "\033[33;1mhotbedd:\033[0m egress %s: resolver rewrite skipped: %v\n", name, err)
		}
		keeperNudge() // so later lookups attribute to this sandbox fast
		audit.Record("egress.set", name,
			fmt.Sprintf("allowlist: %d domains, %d pre-resolved ips (%s)",
				len(norm), len(ips), strings.Join(norm, ",")))
		return len(ips), nil
	}
	return 0, applyEgress(name, preset)
}

// ApplyDNSTrack puts an unfenced sandbox's DNS through the fence so its
// lookups can be recorded: no filter chain (outbound stays the Docker
// default), only the nat DNAT + resolver rewrite. The fence passes every
// lookup of a sandbox whose recorded preset is `allow`.
func ApplyDNSTrack(name string) error {
	if BridgeGateway() == "" {
		return fmt.Errorf("dns tracking %s: no default docker bridge gateway", name)
	}
	script := egressBinCheck + fmt.Sprintf(
		"iptables -D OUTPUT -j %s 2>/dev/null; iptables -F %s 2>/dev/null; iptables -X %s 2>/dev/null; ",
		egressChain, egressChain, egressChain) + egressDNATScript()
	if out, err := egressRun(name, script); err != nil {
		return fmt.Errorf("dns tracking %s: %w: %s", name, err, strings.TrimSpace(out))
	}
	if err := egressResolverExec(name, egressResolverInstall()); err != nil {
		fmt.Fprintf(os.Stderr, "\033[33;1mhotbedd:\033[0m dns tracking %s: resolver rewrite skipped: %v\n", name, err)
	}
	keeperNudge() // attribution first: unknown sources are refused
	audit.Record("egress.set", name, "allow + dns tracking")
	return nil
}

// EgressInstalled reports whether the sandbox netns currently carries the
// rules of a preset (allowlist: filter + nat side; tracked allow: nat side;
// proxy-only/none: filter chain; allow: nothing to check → true).
func EgressInstalled(name, preset string, track bool) (bool, error) {
	out, err := ShowEgressContainer(name)
	if err != nil {
		return false, err
	}
	hasFilter := strings.Contains(out, "-N "+egressChain)
	hasNat := strings.Contains(out, "-N "+egressNatChain)
	switch preset {
	case "allowlist":
		return hasFilter && hasNat, nil
	case "allow":
		return !track || hasNat, nil
	default:
		return hasFilter, nil
	}
}

// lookupIP resolves d best-effort (short timeout; unresolved domains are
// skipped rather than fatal — the fence DNATs DNS for them anyway and the
// keeper adds their IPs the first time they do resolve).
func lookupIP(d string) []net.IP {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, d)
	if err != nil {
		return nil
	}
	out := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, a.IP)
	}
	return out
}

// normalizeDomains trims, lowercases, strips trailing dots and leading
// wildcards, dedupes.
func normalizeDomains(domains []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(domains))
	for _, d := range domains {
		d = strings.ToLower(strings.TrimSpace(d))
		d = strings.TrimSuffix(d, ".")
		d = strings.TrimPrefix(d, "*.")
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	return out
}

// ApplyEgressIP inserts one ACCEPT into an installed allowlist fence (idempotent,
// noop when the sandbox is not fenced). Called by the daemon's egress keeper
// for every fresh DNS answer of the fence.
func ApplyEgressIP(name, ip string) error {
	valid := net.ParseIP(ip)
	if valid == nil || valid.To4() == nil {
		return fmt.Errorf("egress %s: not an IPv4 address: %q", name, ip)
	}
	ip = valid.To4().String()
	script := fmt.Sprintf(
		"iptables -C %s -d %s -j ACCEPT 2>/dev/null || iptables -I %s 3 -d %s -j ACCEPT",
		egressChain, ip, egressChain, ip)
	if out, err := egressRun(name, script); err != nil {
		return fmt.Errorf("egress %s: allow ip %s: %w: %s", name, ip, err, strings.TrimSpace(out))
	}
	return nil
}

// ApplyEgressContainer installs/removes iptables OUTPUT rules inside a
// container. preset=="" or "allow" resets everything (Docker's default).
// allowlist needs domains — use ApplyEgressFence instead (the CLI/API do).
func ApplyEgressContainer(name, preset string) error {
	_, err := ApplyEgressFence(name, preset, nil)
	return err
}

// applyEgress handles the plain (domain-less) presets.
func applyEgress(name, preset string) error {
	preset = strings.TrimSpace(preset)
	if preset == "" {
		preset = "allow"
	}
	if !egressPresets[preset] {
		return fmt.Errorf("unknown egress preset %q (allow|proxy-only|none|allowlist)", preset)
	}
	if preset == "allowlist" {
		return fmt.Errorf("egress %s: allowlist needs domains — `hotbedd egress %s allowlist DOMAINS...`", name, name)
	}
	script := egressScript(preset)
	if preset == "allow" {
		script = egressResetScript()
	}
	if out, err := egressRun(name, script); err != nil {
		return fmt.Errorf("egress %s → %s: %w: %s", name, preset, err, strings.TrimSpace(out))
	}
	if preset == "allow" {
		if err := egressResolverExec(name, egressResolverRestore()); err != nil {
			// best-effort: a shell-less leftover from an older reset stays
			fmt.Fprintf(os.Stderr, "\033[33;1mhotbedd:\033[0m egress %s: resolver restore skipped: %v\n", name, err)
		}
		audit.Record("egress.set", name, "reset to allow")
	} else {
		audit.Record("egress.set", name, preset)
	}
	return nil
}

// ShowEgressContainer returns the HBD_EGRESS filter rules (and the allowlist
// nat side when present); "" = unfenced.
func ShowEgressContainer(name string) (string, error) {
	out, err := egressRun(name, egressBinCheck+
		"iptables -S "+egressChain+" 2>/dev/null; iptables -t nat -S "+egressNatChain+" 2>/dev/null || true")
	return strings.TrimSpace(out), err
}

// egressRun executes an iptables script in the container netns: first via
// plain docker exec (fast path), falling back to the privileged helper
// when the sandbox lacks the iptables binary (sentinel exit 64) or NET_ADMIN.
func egressRun(name, script string) (string, error) {
	out, err := RunTO(30*time.Second, "docker", "exec", "-u", "root", name, "sh", "-c", script)
	if err == nil {
		return out, nil
	}
	why := strings.ToLower(out + " " + err.Error())
	fallback := strings.Contains(why, "status 64") ||
		strings.Contains(why, "sbx_no_iptables") ||
		strings.Contains(why, "permission denied") ||
		strings.Contains(why, "executable file not found") ||
		strings.Contains(why, "not found")
	if fallback {
		return egressHelperRun(name, script)
	}
	return out, err
}

// egressHelperRun shares the sandbox netns with a privileged one-shot
// container that owns a full iptables.
func egressHelperRun(name, script string) (string, error) {
	if err := ensureEgressHelper(); err != nil {
		return "", err
	}
	return RunTO(60*time.Second, "docker", "run", "--rm", "--privileged",
		"--network=container:"+name, egressHelperImage, "sh", "-c", script)
}

// ensureEgressHelper builds the one-file helper image on first use.
// Bootstrapping needs outbound (apk fetch) — in practice this happens the
// first time an egress preset is applied, before any fence exists.
func ensureEgressHelper() error {
	if _, err := RunTO(15*time.Second, "docker", "image", "inspect", egressHelperImage); err == nil {
		return nil
	}
	dir, err := os.MkdirTemp("", "hotbedd-egress")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	df := "FROM alpine:3.21\nRUN apk add --no-cache iptables\n"
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(df), 0o644); err != nil {
		return err
	}
	out, err := RunTO(300*time.Second, "docker", "build", "--quiet", "-t", egressHelperImage, dir)
	if err != nil {
		return fmt.Errorf("building egress helper image: %w: %s", err, strings.TrimSpace(out))
	}
	return nil
}
