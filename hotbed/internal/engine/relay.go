package engine

// The proxy relay.
//
// On Docker Desktop the engine runs in its own utility VM, whose containers
// cannot reach the WSL distro's loopback or eth0 — so the LLM proxy listening
// there is unreachable from kit sandboxes (the sandbox probes built into the
// sync flow fail on every candidate route). The kit-native fix is to run the
// proxy INSIDE the engine VM: we spawn a `--network host` container running
// the same static-ish hotbedd binary in `hotbedd proxy` (relay) mode, bind-mounted
// together with hotbedd's config/keys/state dirs (no image to build, kit stays
// binary-first). Sandboxes then reach the proxy on the engine's own bridge
// gateway, e.g. http://172.17.0.1:7775/v1.
//
// The relay container is intentionally NOT labeled dev.hotbed, so it never
// appears in the sandbox inventory. It restarts with the engine
// (--restart unless-stopped) and re-reads config/keys per request, so keys
// minted later by the main daemon are picked up live.

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"hotbed/internal/audit"
	"hotbed/internal/env"
)

// LegacyRelayName was the pre-name-spacing relay container name — kept only
// so a daemon upgrade can silently migrate (destroy-and-rename) its own
// old relay instead of leaving a stale one bound to the engine netns.
const LegacyRelayName = "hotbed-proxy-relay"

// RelayContainer returns the engine-side proxy relay container name for
// this daemon's state set. The historical fixed name was a collision
// magnet: two hotbedd processes with different config/keys dirs (a real
// daemon and a hermetic test suite) saw each other's relay as "foreign"
// and adopt-and-destroyed it mid-run, leaving sandboxed opencode clients
// with ECONNREFUSED. Deriving the name from the config dir gives each
// state set its own relay; port separation comes from proxy.port.
func RelayContainer() string {
	if n := strings.TrimSpace(os.Getenv("HOTBEDD_RELAY_NAME")); n != "" {
		return n
	}
	// OwnerLabel is the same sha256(ConfigDir)[:6] v0.4 used (owner-id file
	// preserves the legacy hash across the rename), so the name does not
	// change across the v0.5 rename and the LIVE relay is never orphaned.
	return "sbx-relay-" + env.OwnerLabel()[:6]
}

var (
	desktopMu    sync.Mutex
	desktopFlag  bool
	desktopKnown bool
)

// IsDockerDesktop reports whether the default docker engine is Docker
// Desktop (whose containers live in a separate VM). A successful probe is
// cached for the process lifetime; a FAILED probe (docker briefly hung,
// engine restarting) is not — the next call retries, so a transient blip
// at daemon boot can never permanently disable the relay fallback.
func IsDockerDesktop() bool {
	desktopMu.Lock()
	defer desktopMu.Unlock()
	if desktopKnown {
		return desktopFlag
	}
	out, err := RunTO(8*time.Second, "docker", "info", "--format", "{{.OperatingSystem}}")
	if err != nil {
		return false
	}
	desktopFlag = strings.Contains(strings.ToLower(strings.TrimSpace(out)), "docker desktop")
	desktopKnown = true
	return desktopFlag
}

// BridgeGateway returns the gateway IP of the engine's default bridge
// network (what sandboxes use to reach the engine's host netns, where the
// relay binds with --network host).
func BridgeGateway() string {
	out, err := RunTO(8*time.Second, "docker", "network", "inspect", "bridge",
		"--format", "{{(index .IPAM.Config 0).Gateway}}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// pickRelayImage chooses a glibc-capable base image already present locally.
func pickRelayImage() (string, error) {
	for _, img := range []string{"hotbed:base", "hotbed:go", "debian:latest", "ubuntu:latest"} {
		if _, err := RunTO(8*time.Second, "docker", "image", "inspect", img); err == nil {
			return img, nil
		}
	}
	return "", fmt.Errorf("no usable base image for the proxy relay (have: docker images — needs hotbed:base or a debian-like glibc image)")
}

// relayRunning reports whether a relay container exists and is running.
func relayRunning(name string) (exists, running bool) {
	out, err := RunTO(8*time.Second, "docker", "inspect", "--format", "{{.State.Running}}", name)
	if err != nil {
		return false, false // absent container exits nonzero
	}
	return true, strings.TrimSpace(out) == "true"
}

// waitRelay waits briefly for the relay proxy to actually SERVE its port —
// verified from inside the relay's own netns via docker exec, since the
// host cannot route into the engine VM. A container state of "Running" is
// not enough: e.g. a relay whose config has no providers exits instantly
// and flaps under the restart policy.
func waitRelay(name, port string) bool {
	for i := 0; i < 10; i++ {
		out, err := RunTO(3*time.Second, "docker", "exec", name,
			"bash", "-c", "</dev/tcp/127.0.0.1/"+port+" && echo UP")
		if err == nil && strings.Contains(out, "UP") {
			return true
		}
		time.Sleep(400 * time.Millisecond)
	}
	return false
}

// relayMounts lists a container's host-side mount sources.
func relayMounts(name string) (string, error) {
	return RunTO(8*time.Second, "docker", "inspect", "--format",
		"{{range .Mounts}}{{println .Source}}{{end}}", name)
}

// relayOwns reports whether the relay container was spawned by THIS daemon
// (same config/data dirs). Foreign relays are never adopted; with per-state
// names the case is nearly impossible, but a hand-spawned or migrated one
// is replaced so the key set always matches the daemon minting keys.
func relayOwns(name string) bool {
	out, err := relayMounts(name)
	if err != nil {
		return false
	}
	// Mount sources are host paths — compare those, not the container-side
	// env values (which are fixed mount points like /etc/hotbedd).
	return strings.Contains(out, env.ConfigDir()+"\n") &&
		strings.Contains(out, env.DataDir()+"\n")
}

// legacyRelayCleanup removes the pre-name-spacing hotbed-proxy-relay when it
// belongs to this daemon's state set (or is stopped junk) — otherwise it
// would keep holding proxy.port inside the engine's host netns and our
// namespaced relay could never serve.
func legacyRelayCleanup() {
	exists, running := relayRunning(LegacyRelayName)
	if !exists {
		return
	}
	if !running && !relayOwns(LegacyRelayName) {
		// stopped foreign junk under the old fixed name: free the name
		_, _ = RunTO(30*time.Second, "docker", "rm", "-f", LegacyRelayName)
		return
	}
	if relayOwns(LegacyRelayName) {
		_, _ = RunTO(30*time.Second, "docker", "rm", "-f", LegacyRelayName)
		audit.Record("relay.migrate", LegacyRelayName, "pre-name-spacing relay replaced by "+RelayContainer())
		fmt.Printf("\033[36;1mhotbedd:\033[0m relay migrated to per-state name %s\n", RelayContainer())
	}
	// foreign && still running: leave it; a port clash surfaces as an
	// honest waitRelay error below
}

// EnsureRelay makes sure this state set's relay container exists and is
// running; returns the gateway IP the sandboxes should dial. Idempotent:
// an already-running relay of ours is reused, a stopped one is started.
func EnsureRelay(proxyPort string) (string, error) {
	gw := BridgeGateway()
	if gw == "" {
		return "", fmt.Errorf("no default docker bridge gateway — cannot wire the proxy relay")
	}
	legacyRelayCleanup()
	name := RelayContainer()
	exists, running := relayRunning(name)
	if exists && running && relayOwns(name) {
		return gw, nil
	}
	if exists && running { // foreign relay: our dirs take the relay over
		_, _ = RunTO(30*time.Second, "docker", "rm", "-f", name)
		audit.Record("relay.replace", name,
			"relay served other state dirs — respawned for this daemon")
		fmt.Printf("\033[36;1mhotbedd:\033[0m relay adopted for this state set (removed foreign %s)\n", name)
		exists = false
	} else if exists && !running && relayOwns(name) {
		if _, err := RunTO(30*time.Second, "docker", "start", name); err != nil {
			return "", fmt.Errorf("relay start: %w", err)
		}
		if !waitRelay(name, proxyPort) {
			return "", fmt.Errorf("relay container %s is up but not serving port %s (logs: docker logs %s)", name, proxyPort, name)
		}
		return gw, nil
	}
	// foreign-stopped or absent: make sure the name is free, then spawn
	if exists {
		_, _ = RunTO(30*time.Second, "docker", "rm", "-f", name)
	}

	bin, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("relay binary: %w", err)
	}
	image, err := pickRelayImage()
	if err != nil {
		return "", err
	}
	args := []string{
		"run", "-d", "--name", name,
		"--network", "host", // binds inside the engine VM's own netns
		"--restart", "unless-stopped",
		"--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		"-e", "HOTBEDD_CONFIG_DIR=/etc/hotbedd",
		"-e", "HOTBEDD_DATA_DIR=/keys",
		"-e", "HOTBEDD_STATE_DIR=/state", // usage receipts shared with the dashboard
		"-v", env.ConfigDir() + ":/etc/hotbedd:ro",
		"-v", env.DataDir() + ":/keys:ro",
		"-v", env.StateDir() + ":/state",
		"-v", bin + ":/usr/local/bin/hotbedd:ro",
		"--entrypoint", "/usr/local/bin/hotbedd",
		image, "proxy",
	}
	if _, err := RunTO(90*time.Second, "docker", args...); err != nil {
		return "", fmt.Errorf("relay spawn: %w", err)
	}
	if !waitRelay(name, proxyPort) {
		return "", fmt.Errorf("relay container %s is up but not serving port %s (logs: docker logs %s)", name, proxyPort, name)
	}
	audit.Record("relay.spawn", name,
		fmt.Sprintf("image=%s gw=%s port=%s via=engine host netns", image, gw, proxyPort))
	fmt.Printf("\033[36;1mhotbedd:\033[0m proxy relay spawned in the docker engine (%s → http://%s:%s/v1)\n",
		name, gw, proxyPort)
	return gw, nil
}

// RelayStatus summarizes the relay for doctor/CLI output.
func RelayStatus() string {
	name := RelayContainer()
	exists, running := relayRunning(name)
	switch {
	case exists && running:
		return fmt.Sprintf("%s (running at gw %s)", name, BridgeGatewayOrDash())
	case exists:
		return name + " (stopped)"
	default:
		return "not spawned — spawns automatically on the first container sync (Docker Desktop only)"
	}
}

func BridgeGatewayOrDash() string {
	if gw := BridgeGateway(); gw != "" {
		return gw
	}
	return "—"
}
