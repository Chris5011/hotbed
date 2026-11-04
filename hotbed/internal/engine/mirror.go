package engine

// Docker Desktop rewrites every bind source from a WSL distro into its own
// mirror, /run/desktop/mnt/host/wsl/docker-desktop-bind-mounts/<distro>/<hash>,
// where <hash> is the sha256 of the original path (verified). Inspect only
// reports the mirror. DisplaySource maps it back: hashing candidate paths
// under $HOME either matches exactly or not at all, so a shown path is
// never a guess. Results are cached in the state dir.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"hotbed/internal/env"
)

var mirrorRe = regexp.MustCompile(`^/run/desktop/mnt/host/wsl/docker-desktop-bind-mounts/[^/]+/([0-9a-f]{64})$`)

var (
	mirrorMu      sync.Mutex
	mirrorPaths   map[string]string // sha256 -> original path
	mirrorScanned bool
)

func mirrorCachePath() string { return filepath.Join(env.StateDir(), "bind-mirrors.json") }

func pathHash(p string) string {
	s := sha256.Sum256([]byte(p))
	return hex.EncodeToString(s[:])
}

// DisplaySource returns the original host path behind a Docker Desktop
// mirror path, or src unchanged.
func DisplaySource(src string) string {
	m := mirrorRe.FindStringSubmatch(src)
	if m == nil {
		return src
	}
	mirrorMu.Lock()
	defer mirrorMu.Unlock()
	if mirrorPaths == nil {
		mirrorPaths = map[string]string{}
		if b, err := os.ReadFile(mirrorCachePath()); err == nil {
			_ = json.Unmarshal(b, &mirrorPaths)
		}
	}
	if p, ok := mirrorPaths[m[1]]; ok {
		return p
	}
	if !mirrorScanned { // once per daemon run: new mounts since are rare
		mirrorScanned = true
		scanMirrorCandidates()
		if b, err := json.Marshal(mirrorPaths); err == nil {
			_ = os.MkdirAll(filepath.Dir(mirrorCachePath()), 0o755)
			_ = os.WriteFile(mirrorCachePath(), b, 0o600)
		}
		if p, ok := mirrorPaths[m[1]]; ok {
			return p
		}
	}
	return src
}

// scanMirrorCandidates hashes paths under $HOME (dirs and files, 4 levels,
// caches and VCS internals skipped, bounded).
func scanMirrorCandidates() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	skip := map[string]bool{".cache": true, "node_modules": true, ".git": true, ".npm": true,
		".cargo": true, ".rustup": true, "go": true, ".local/state": true, "snap": true}
	n := 0
	mirrorPaths[pathHash(home)] = home
	_ = filepath.WalkDir(home, func(p string, d fs.DirEntry, err error) error {
		if err != nil || n > 200000 {
			return fs.SkipDir
		}
		rel := strings.TrimPrefix(p, home+"/")
		if d.IsDir() && p != home && (skip[d.Name()] || skip[rel] || strings.Count(rel, "/") >= 4) {
			mirrorPaths[pathHash(p)] = p
			return fs.SkipDir
		}
		n++
		mirrorPaths[pathHash(p)] = p
		return nil
	})
}
