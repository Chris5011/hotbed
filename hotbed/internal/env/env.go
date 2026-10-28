// Package env holds shared runtime constants and path resolution for hotbedd.
package env

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Version of the hotbedd daemon/CLI. A var on purpose: release builds inject
// the tagged version here via -ldflags "-X hotbed/internal/env.Version=v…"
// (Makefile / scripts/release.sh; see docs/RELEASING.md).
var Version = "0.7.0"

const (
	// DefaultPort the daemon (dashboard/API) listens on (localhost only).
	DefaultPort = "7770"

	// DefaultBind is the dashboard/API listener address. Loopback on
	// purpose — remote exposure is opt-in via `--bind` / HOTBEDD_BIND and
	// comes with an explicit unauthenticated-warning; see serve().
	DefaultBind = "127.0.0.1"

	// DefaultProxyPort for the LLM token-accounting proxy. Unlike the
	// dashboard this listener is reachable from sandboxes by design
	// (per-sandbox bearer keys gate it).
	DefaultProxyPort = "7775"

	// DefaultDNSPort for the egress-allowlist DNS fence: the iptables DNAT
	// inside fenced sandboxes rewrites their :53 queries to gw:DNSPort,
	// where the fence listener (relay / main daemon) answers.
	DefaultDNSPort = "15353"

	// ContainerLabel marks hotbed-managed docker sandboxes (set by bin/hbd).
	ContainerLabel = "dev.hotbed"

	// LegacyContainerLabel is the pre-v0.5 label ("dev.sbx"). Containers
	// created before the rename keep it, and the collector lists BOTH label
	// generations (deduped by name) so a rename never hides a live sandbox.
	LegacyContainerLabel = "dev.sbx"

	// Name prefixes used by the kit.
	ContainerPrefix = "hotbed-"    // docker sandbox containers (bin/hbd)
	VMStdPrefix     = "hotbedvm-"  // libvirt domains
	VMDockerPrefix  = "hotbedvmd-" // docker-backend VM containers
	VmDomainPrefix  = "hotbedvm-"  // domain name = hotbedvm-<name>
)

// Root resolves the hotbed kit root (contains bin/hbd, vm/, tests/).
// Order: $HOTBED_ROOT > executable dir if it looks like the kit > $HOME/hotbed.
func Root() string {
	if v := os.Getenv("HOTBED_ROOT"); v != "" {
		if isKit(v) {
			return v
		}
	}
	if exe, err := os.Executable(); err == nil {
		// .../bin/hotbedd -> kit root; the module lives in a subdirectory
		// of the kit (hotbed/bin/hotbedd), so also try one level further up.
		cand := filepath.Dir(filepath.Dir(exe)) // .../bin/hotbedd -> .../hotbed
		if isKit(cand) {
			return cand
		}
		if up := filepath.Dir(cand); isKit(up) { // .../hotbed -> kit root
			return up
		}
		cand2 := filepath.Dir(exe)
		if isKit(cand2) {
			return cand2
		}
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "hotbed")
}

func isKit(dir string) bool {
	if dir == "" {
		return false
	}
	if _, err := os.Stat(filepath.Join(dir, "bin", "hbd")); err != nil {
		return false
	}
	_, err := os.Stat(filepath.Join(dir, "vm", "hotbedvm.sh"))
	return err == nil
}

// --- per-instance dirs (XDG, overridable for tests/hermetic suites) ---

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// SafeName is THE file-name-safe form of a sandbox name (single source -
// cfgsync and attach used to run two identical copies that could drift).
// Lowercase, keeps only a-z 0-9 '-', '_' (dissolves unusual characters).
// FILLS "_" when nothing survives, so it is always a usable filename.
func SafeName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "_"
	}
	return b.String()
}

func homeDefault(rel string) string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, rel)
}

// The v0.5 rename (sbxd → hotbedd, OpenCode-SBX → hotbed) moved the
// per-instance XDG dirs. migrationOnce copies v0.4-era state legacy into the
// new layout on first sight — copy, never move, so a downgrade still boots.
// Explicit dir overrides (hermetic suites) never trigger migration.
var migrateOnce sync.Once

func migrateLegacyDirs() {
	ownerSeeded := false // config migration doubles as owner-stamp seeding
	for _, p := range [][3]string{
		{"HOTBEDD_CONFIG_DIR", ".config/hotbedd", ".config/sbxd"},
		{"HOTBEDD_DATA_DIR", ".local/share/hotbedd", ".local/share/sbxd"},
		{"HOTBEDD_STATE_DIR", ".local/state/hotbedd", ".local/state/sbxd"},
	} {
		if os.Getenv(p[0]) != "" {
			continue // overridden (hermetic suite): assume fresh state
		}
		dir := homeDefault(p[1])
		if _, err := os.Stat(dir); err == nil {
			continue // new layout already live
		}
		legacy := homeDefault(p[2])
		if _, err := os.Stat(legacy); err != nil {
			continue // no v0.4 state on this box
		}
		if copyTree(legacy, dir) == nil { // best effort; failure means a fresh dir
			if p[0] == "HOTBEDD_CONFIG_DIR" && !ownerSeeded {
				// seed the owner stamp WHILE migrating, from the legacy
				// dir hash. Runs only for the default (non-overridden)
				// config dir — hermetic suite dirs are minted fresh per
				// run and must stay anonymous, otherwise every suite
				// daemon inherits this box's legacy stamp and its
				// sweeper could stop the user's legacy sandboxes.
				sum := sha256.Sum256([]byte(legacy))
				_ = os.WriteFile(filepath.Join(dir, "owner-id"),
					[]byte(hex.EncodeToString(sum[:6])), 0o600)
				ownerSeeded = true
			}
		}
	}
}

// copyTree recursively copies src under dst (files + dirs, modes kept).
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(out, b, 0o600)
	})
}

// ConfigDir holds hotbedd's own settings: $HOTBEDD_CONFIG_DIR else ~/.config/hotbedd.
func ConfigDir() string {
	if v := os.Getenv("HOTBEDD_CONFIG_DIR"); v != "" {
		return v
	}
	migrateOnce.Do(migrateLegacyDirs)
	return homeDefault(".config/hotbedd")
}

// DataDir holds keys.json: $HOTBEDD_DATA_DIR else ~/.local/share/hotbedd.
func DataDir() string {
	if v := os.Getenv("HOTBEDD_DATA_DIR"); v != "" {
		return v
	}
	migrateOnce.Do(migrateLegacyDirs)
	return homeDefault(".local/share/hotbedd")
}

// StateDir holds the usage journal: $HOTBEDD_STATE_DIR else ~/.local/state/hotbedd.
func StateDir() string {
	if v := os.Getenv("HOTBEDD_STATE_DIR"); v != "" {
		return v
	}
	migrateOnce.Do(migrateLegacyDirs)
	return homeDefault(".local/state/hotbedd")
}

// RelayDisabled opts out of the in-engine proxy relay (tests/hermetic
// suites, air-gapped boxes, manual setups). HOTBEDD_RELAY_DISABLE=1.
func RelayDisabled() bool { return getenv("HOTBEDD_RELAY_DISABLE", "") == "1" }

// DNSFenceDisabled opts out of the egress-allowlist DNS fence listener
// (same use cases as RelayDisabled). HOTBEDD_DNS_FENCE_DISABLE=1.
func DNSFenceDisabled() bool { return getenv("HOTBEDD_DNS_FENCE_DISABLE", "") == "1" }

// OwnerLabelKey is the container label stamping WHICH hotbedd daemon spawned a
// sandbox. Idle sweepers and other management verbs skip foreign-owner
// sandboxes so a hermetic test daemon can never stop the user's real
// sandboxes (and vice versa). Containers created before this stamp (label
// empty) keep legacy behavior.
const OwnerLabelKey = "dev.hotbed.owner"

// LegacyOwnerLabelKey is the pre-v0.5 owner stamp. Legacy sandboxes carry
// their owner under dev.sbx.owner — an owner check that only reads the new
// key sees "" there and would treat a foreign legacy sandbox as its own
// (the regression that let a hermetic suite's idle sweeper stop the live
// moon-sweepers container during v0.5 verification).
const LegacyOwnerLabelKey = "dev.sbx.owner"

// ForeignOwner reports whether a container's owner stamp (current or legacy
// label) belongs to a DIFFERENT daemon — i.e. this daemon must leave it
// alone. Empty stamp = pre-stamp legacy era: "ours" (matches v0.4).
func ForeignOwner(labels map[string]string) bool {
	o := labels[OwnerLabelKey]
	if o == "" {
		o = labels[LegacyOwnerLabelKey]
	}
	return o != "" && o != OwnerLabel()
}

// OwnerLabel returns the owner stamp value for THIS daemon. v0.4 derived it
// directly from sha256(ConfigDir)[:6] — the rename would have re-stamped
// every existing sandbox as foreign (they compare labels verbatim). So the
// stamp now lives in a stable owner-id file inside the config dir, seeded on
// first run from the LEGACY dir hash when v0.4 state exists: a rename never
// makes the daemon disown its own sandboxes, and the relay name (which shares
// the same hash) stays byte-identical too.
func OwnerLabel() string {
	dir := ConfigDir()
	idFile := filepath.Join(dir, "owner-id")
	if b, err := os.ReadFile(idFile); err == nil {
		if s := strings.TrimSpace(string(b)); len(s) >= 6 {
			return s
		}
	}
	// No owner-id yet: stamp is the hash of THIS daemon's config dir. The
	// legacy-hash fallback deliberately lives in migrateLegacyDirs (it fires
	// only when the DEFAULT config dir is freshly copied from the legacy
	// one) — a fallback here would hand every fresh/hermetic config dir
	// this box's legacy stamp.
	sum := sha256.Sum256([]byte(dir))
	stamp := hex.EncodeToString(sum[:6])
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(idFile, []byte(stamp), 0o600)
	return stamp
}
