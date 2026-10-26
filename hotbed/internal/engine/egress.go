package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"hotbed/internal/audit"
)

// Egress presets for containers.
//
//	allow        no rules (Docker default: all outbound permitted)
//	proxy-only   loopback + established + private ranges (RFC1918, docker
//	             bridge) + DNS stay open; public destinations drop — LLM
//	             traffic must ride the hbd proxy on the host side
//	none         loopback + established only; everything else drops
//
// Applied via `docker exec … iptables` when the sandbox can run iptables
// (has the binary and NET_ADMIN); otherwise through a privileged helper
// container sharing the sandbox netns (image hotbedd-egress-helper:1, built
// once on demand — its build needs outbound in the window before any
// fence exists). Rules live in the container netns, so they vanish on
// recreation; callers re-apply after `mount add/rm` and after any spawn.
// This is a convenience fence, not a security boundary.

// KnownEgressPreset reports whether the preset string is valid ("" = allow).
func KnownEgressPreset(preset string) bool {
	return egressPresets[strings.TrimSpace(preset)]
}

var egressPresets = map[string]bool{
	"allow":      true,
	"proxy-only": true,
	"none":       true,
}

const (
	egressChain       = "HBD_EGRESS"
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
	rules = append(rules, "-o lo -j ACCEPT")
	rules = append(rules, "-m state --state ESTABLISHED,RELATED -j ACCEPT")
	if preset == "proxy-only" {
		// docker bridge ranges + RFC1918 internals + DNS stay reachable
		rules = append(rules,
			"-d 172.16.0.0/12 -j ACCEPT",
			"-d 10.0.0.0/8 -j ACCEPT",
			"-d 192.168.0.0/16 -j ACCEPT",
			"-p udp --dport 53 -j ACCEPT",
			"-p tcp --dport 53 -j ACCEPT",
		)
	}
	rules = append(rules, "-j DROP")

	var b strings.Builder
	b.WriteString(egressBinCheck)
	fmt.Fprintf(&b, "iptables -N %s 2>/dev/null; iptables -F %s 2>/dev/null; ", egressChain, egressChain)
	for _, r := range rules {
		fmt.Fprintf(&b, "iptables -A %s %s; ", egressChain, r)
	}
	fmt.Fprintf(&b, "iptables -D OUTPUT -j %s 2>/dev/null; iptables -I OUTPUT 1 -j %s", egressChain, egressChain)
	return b.String()
}

// egressResetScript returns to the unfenced default (removes jump + chain).
// The trailing `true` makes the script succeed even when nothing existed yet.
func egressResetScript() string {
	return egressBinCheck + fmt.Sprintf(
		"iptables -D OUTPUT -j %s 2>/dev/null; iptables -F %s 2>/dev/null; iptables -X %s 2>/dev/null; true",
		egressChain, egressChain, egressChain)
}

// ApplyEgressContainer installs/removes iptables OUTPUT rules inside a
// container. preset=="" or "allow" resets everything (Docker's default).
func ApplyEgressContainer(name, preset string) error {
	preset = strings.TrimSpace(preset)
	if preset == "" {
		preset = "allow"
	}
	if !egressPresets[preset] {
		return fmt.Errorf("unknown egress preset %q (allow|proxy-only|none)", preset)
	}
	script := egressScript(preset)
	if preset == "allow" {
		script = egressResetScript()
	}
	if out, err := egressRun(name, script); err != nil {
		return fmt.Errorf("egress %s → %s: %w: %s", name, preset, err, strings.TrimSpace(out))
	}
	if preset == "allow" {
		audit.Record("egress.set", name, "reset to allow")
	} else {
		audit.Record("egress.set", name, preset)
	}
	return nil
}

// ShowEgressContainer returns the HBD_EGRESS chain rules ("" = unfenced).
func ShowEgressContainer(name string) (string, error) {
	out, err := egressRun(name, egressBinCheck+"iptables -S "+egressChain+" 2>/dev/null || true")
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
