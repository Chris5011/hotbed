// Package keys stores per-sandbox proxy credentials.
//
// Every sandbox gets its own bearer token ("sbxk-…" ) minted by sbxd; the
// upstream provider key never enters the sandbox. Budget wallets (USD and
// tokens) live in config.json now; this file only carries credentials.
//
// The store is a stateless file view: every call re-reads keys.json, so CLI
// and daemon (and a restarted daemon) instantly agree — no stale caches.
package keys

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"sbxd/internal/audit"
	"sbxd/internal/env"
)

// Key is one sandbox credential.
type Key struct {
	Token    string `json:"token"`
	Sandbox  string `json:"sandbox"`
	Provider string `json:"provider"`
	Created  string `json:"created"`
	Revoked  string `json:"revoked,omitempty"`
}

type file struct {
	Keys []Key `json:"keys"`
}

// Store is a stateless view on the keys file.
type Store struct {
	path string
	mu   sync.Mutex
}

// Path returns the keys file location.
func Path() string { return filepath.Join(env.DataDir(), "keys.json") }

// New builds a store view.
func New() *Store { return &Store{path: Path()} }

func (s *Store) load() (file, error) {
	var f file
	b, err := os.ReadFile(s.path)
	if err != nil {
		return f, nil // fresh store
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return f, fmt.Errorf("parse %s: %w", s.path, err)
	}
	return f, nil
}

func (s *Store) save(f file) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, b, 0o600)
}

// Open loads the store (stateless; kept for API compat).
func Open() (*Store, error) { return New(), nil }

// EnsureKey returns the active key for (sandbox, provider), minting one if
// needed. Existing tokens of the same sandbox/provider are kept (idempotent).
func (s *Store) EnsureKey(sandbox, provider string) (Key, bool, error) {
	f, err := s.load()
	if err != nil {
		return Key{}, false, err
	}
	for _, k := range f.Keys {
		if k.Sandbox == sandbox && k.Provider == provider && k.Revoked == "" {
			return k, false, nil
		}
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return Key{}, false, err
	}
	k := Key{
		// full 128 bits of randomness in the token (hex) — brute-force
		// resistance for the 0.0.0.0 proxy listener; older shorter tokens
		// keep working (ByToken matches stored strings)
		Token:    "sbxk-" + provider + "-" + hex.EncodeToString(b),
		Sandbox:  sandbox,
		Provider: provider,
		Created:  time.Now().UTC().Format(time.RFC3339),
	}
	f.Keys = append(f.Keys, k)
	if err := s.save(f); err != nil {
		return Key{}, false, err
	}
	audit.Record("key.mint", sandbox, provider)
	return k, true, nil
}

// Revoke kills a sandbox's active keys (next EnsureKey mints fresh).
func (s *Store) Revoke(sandbox string) (int, error) {
	f, err := s.load()
	if err != nil {
		return 0, err
	}
	n := 0
	for i, k := range f.Keys {
		if k.Sandbox == sandbox && k.Revoked == "" {
			f.Keys[i].Revoked = time.Now().UTC().Format(time.RFC3339)
			n++
		}
	}
	if err := s.save(f); err != nil {
		return 0, err
	}
	if n > 0 {
		audit.Record("key.revoke", sandbox, fmt.Sprintf("%d key(s) revoked", n))
	}
	return n, nil
}

// ByToken resolves a bearer credential (non-revoked) with a constant-time
// token comparison — the proxy listens on 0.0.0.0, so timing must not leak
// token prefixes to a network-adjacent observer. Fresh read each call:
// tokens minted by the CLI are instantly valid for a running daemon proxy,
// revoked ones instantly invalid.
func (s *Store) ByToken(token string) (Key, bool) {
	f, err := s.load()
	if err != nil {
		return Key{}, false
	}
	for _, k := range f.Keys {
		if k.Revoked != "" {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(k.Token), []byte(token)) == 1 {
			return k, true
		}
	}
	return Key{}, false
}

// List returns active keys sorted by sandbox/provider.
func (s *Store) List() []Key {
	f, err := s.load()
	if err != nil {
		return nil
	}
	var out []Key
	for _, k := range f.Keys {
		if k.Revoked == "" {
			out = append(out, k)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Sandbox != out[j].Sandbox {
			return out[i].Sandbox < out[j].Sandbox
		}
		return out[i].Provider < out[j].Provider
	})
	return out
}

// Raw returns the parsed file (for masked API views).
func (s *Store) Raw() (file, error) { return s.load() }
