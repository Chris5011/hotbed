// Package env holds shared runtime constants and path resolution for sbxd.
package env

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
)

// Version of the sbxd daemon/CLI. A var on purpose: release builds inject
// the tagged version here via -ldflags "-X sbxd/internal/env.Version=v…"
// (Makefile / scripts/release.sh; see docs/RELEASING.md).
var Version = "0.2.8"

const (
	// DefaultPort the daemon (dashboard/API) listens on (localhost only).
	DefaultPort = "7770"

	// DefaultBind is the dashboard/API listener address. Loopback on
	// purpose — remote exposure is opt-in via `--bind` / SBXD_BIND and
	// comes with an explicit unauthenticated-warning; see serve().
	DefaultBind = "127.0.0.1"

	// DefaultProxyPort for the LLM token-accounting proxy. Unlike the
	// dashboard this listener is reachable from sandboxes by design
	// (per-sandbox bearer keys gate it).
	DefaultProxyPort = "7775"

	// ContainerLabel marks sbx-managed docker sandboxes (set by bin/sbx).
	ContainerLabel = "dev.sbx"

	// Name prefixes used by the kit.
	ContainerPrefix = "sbx-"    // docker sandbox containers (bin/sbx)
	VMStdPrefix     = "sbxvm-"  // libvirt domains
	VMDockerPrefix  = "sbxvmd-" // docker-backend VM containers
	VmDomainPrefix  = "sbxvm-"  // domain name = sbxvm-<name>
)

// Root resolves the OpenCode-SBX kit root (contains bin/sbx, vm/, tests/).
// Order: $SBX_ROOT > executable dir if it looks like the kit > $HOME/OpenCode-SBX.
func Root() string {
	if v := os.Getenv("SBX_ROOT"); v != "" {
		if isKit(v) {
			return v
		}
	}
	if exe, err := os.Executable(); err == nil {
		cand := filepath.Dir(filepath.Dir(exe)) // .../bin/sbxd -> kit
		if isKit(cand) {
			return cand
		}
		cand2 := filepath.Dir(exe)
		if isKit(cand2) {
			return cand2
		}
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "OpenCode-SBX")
}

func isKit(dir string) bool {
	if dir == "" {
		return false
	}
	if _, err := os.Stat(filepath.Join(dir, "bin", "sbx")); err != nil {
		return false
	}
	_, err := os.Stat(filepath.Join(dir, "vm", "sbxvm.sh"))
	return err == nil
}

// --- per-instance dirs (XDG, overridable for tests/hermetic suites) ---

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func homeDefault(rel string) string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, rel)
}

// ConfigDir holds sbxd's own settings: $SBXD_CONFIG_DIR else ~/.config/sbxd.
func ConfigDir() string {
	return getenv("SBXD_CONFIG_DIR", homeDefault(".config/sbxd"))
}

// DataDir holds keys.json: $SBXD_DATA_DIR else ~/.local/share/sbxd.
func DataDir() string {
	return getenv("SBXD_DATA_DIR", homeDefault(".local/share/sbxd"))
}

// StateDir holds the usage journal: $SBXD_STATE_DIR else ~/.local/state/sbxd.
func StateDir() string {
	return getenv("SBXD_STATE_DIR", homeDefault(".local/state/sbxd"))
}

// RelayDisabled opts out of the in-engine proxy relay (tests/hermetic
// suites, air-gapped boxes, manual setups). SBXD_RELAY_DISABLE=1.
func RelayDisabled() bool { return getenv("SBXD_RELAY_DISABLE", "") == "1" }

// OwnerLabelKey is the container label stamping WHICH sbxd daemon spawned a
// sandbox — a stable short hash of that daemon's config dir. Idle sweepers
// and other management verbs skip foreign-owner sandboxes so a hermetic
// test daemon can never stop the user's real sandboxes (and vice versa).
// Containers created before this stamp (label empty) keep legacy behavior.
const OwnerLabelKey = "dev.sbx.owner"

// OwnerLabel returns the owner stamp value for THIS daemon.
func OwnerLabel() string {
	sum := sha256.Sum256([]byte(ConfigDir()))
	return hex.EncodeToString(sum[:6])
}
