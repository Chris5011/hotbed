// Package keys stores per-sandbox proxy credentials (virtual keys).
//
// Every (sandbox, agent, provider) gets its own bearer token ("hbk-…")
// minted by hotbedd; the proxy translates it to the upstream provider key,
// which never enters the sandbox, and books every call to the key's
// sandbox AND agent — per-sandbox-per-agent burn tracking. Keys minted
// before agents existed carry no agent (shared / manual keys). Budget wallets (USD and
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
	"strings"
	"sync"
	"time"

	"hotbed/internal/audit"
	"hotbed/internal/env"
)

// Key is one sandbox credential.
type Key struct {
	Token    string `json:"token"`
	Sandbox  string `json:"sandbox"`
	Provider string `json:"provider"`
	Agent    string `json:"agent,omitempty"` // agent module id; "" = shared/manual key
	// Workspace is the id of the project folder the key was minted for
	// (/workspace/.hotbed/id): a later sandbox on the same folder gets
	// the same token back — agents that bind sessions to their
	// credentials (Hermes) can then resume conversations.
	Workspace string `json:"workspace,omitempty"`
	Created   string `json:"created"`
	Revoked   string `json:"revoked,omitempty"`
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

// EnsureKey returns the active shared key for (sandbox, provider) — the
// agent-less key of `hotbedd key mint`. Syncs use EnsureAgentKey.
func (s *Store) EnsureKey(sandbox, provider string) (Key, bool, error) {
	return s.EnsureAgentKey(sandbox, "", provider)
}

// EnsureAgentKey returns the active virtual key for (sandbox, agent,
// provider), minting one if needed (idempotent).
func (s *Store) EnsureAgentKey(sandbox, agent, provider string) (Key, bool, error) {
	return s.EnsureWorkspaceKey(sandbox, agent, provider, "")
}

// EnsureWorkspaceKey is EnsureAgentKey with workspace continuity: when no
// active key exists for (sandbox, agent, provider), a key previously minted
// for the same workspace whose sandbox is gone (revoked on delete) is
// re-activated and re-bound to this sandbox instead of minting a fresh
// token. A key still active for another sandbox is never shared.
func (s *Store) EnsureWorkspaceKey(sandbox, agent, provider, workspace string) (Key, bool, error) {
	f, err := s.load()
	if err != nil {
		return Key{}, false, err
	}
	for i, k := range f.Keys {
		if k.Sandbox == sandbox && k.Provider == provider && k.Agent == agent && k.Revoked == "" {
			if workspace != "" && k.Workspace == "" {
				f.Keys[i].Workspace = workspace
				_ = s.save(f)
				k.Workspace = workspace
			}
			return k, false, nil
		}
	}
	if workspace != "" && agent != "" {
		for i := len(f.Keys) - 1; i >= 0; i-- { // newest first
			k := f.Keys[i]
			if k.Workspace != workspace || k.Agent != agent || k.Provider != provider || k.Revoked == "" {
				continue
			}
			old := k.Sandbox
			f.Keys[i].Revoked, f.Keys[i].Sandbox = "", sandbox
			if err := s.save(f); err != nil {
				return Key{}, false, err
			}
			audit.Record("key.rebind", sandbox, fmt.Sprintf("%s@%s: workspace key of deleted %s re-activated", agent, provider, old))
			return f.Keys[i], false, nil
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
		Token:     "hbk-" + tokenTag(agent, provider) + "-" + hex.EncodeToString(b),
		Sandbox:   sandbox,
		Provider:  provider,
		Agent:     agent,
		Workspace: workspace,
		Created:   time.Now().UTC().Format(time.RFC3339),
	}
	f.Keys = append(f.Keys, k)
	if err := s.save(f); err != nil {
		return Key{}, false, err
	}
	audit.Record("key.mint", sandbox, strings.TrimPrefix(agent+"@"+provider, "@"))
	return k, true, nil
}

// tokenTag is the readable token prefix (survives masking in the UI).
func tokenTag(agent, provider string) string {
	if agent == "" {
		return provider
	}
	return agent + "-" + provider
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
		if out[i].Agent != out[j].Agent {
			return out[i].Agent < out[j].Agent
		}
		return out[i].Provider < out[j].Provider
	})
	return out
}

// Raw returns the parsed file (for masked API views).
func (s *Store) Raw() (file, error) { return s.load() }
