package keys

import (
	"strings"
	"testing"
)

// TestAgentKeys: one virtual key per (sandbox, agent, provider), stable
// across re-syncs, resolvable to its agent; the shared key stays separate.
func TestAgentKeys(t *testing.T) {
	t.Setenv("HOTBEDD_DATA_DIR", t.TempDir())
	t.Setenv("HOTBEDD_STATE_DIR", t.TempDir())
	s := New()
	codex, minted, err := s.EnsureAgentKey("sb", "codex", "up")
	if err != nil || !minted {
		t.Fatalf("mint codex: %v minted=%v", err, minted)
	}
	again, minted, _ := s.EnsureAgentKey("sb", "codex", "up")
	if minted || again.Token != codex.Token {
		t.Error("re-sync minted a second codex key")
	}
	hermes, _, _ := s.EnsureAgentKey("sb", "hermes", "up")
	shared, _, _ := s.EnsureKey("sb", "up")
	if hermes.Token == codex.Token || shared.Token == codex.Token || shared.Token == hermes.Token {
		t.Error("agents share a token")
	}
	if !strings.HasPrefix(codex.Token, "hbk-codex-up-") || !strings.HasPrefix(shared.Token, "hbk-up-") {
		t.Errorf("token tags: %s / %s", codex.Token, shared.Token)
	}
	if k, ok := s.ByToken(hermes.Token); !ok || k.Agent != "hermes" || k.Sandbox != "sb" {
		t.Errorf("ByToken(hermes) = %+v %v", k, ok)
	}
	if n, _ := s.Revoke("sb"); n != 3 {
		t.Errorf("revoke = %d, want 3", n)
	}
}

// TestWorkspaceKeyRebind: a deleted sandbox's workspace key comes back for
// the next sandbox on that folder; a live sandbox's key is never shared.
func TestWorkspaceKeyRebind(t *testing.T) {
	t.Setenv("HOTBEDD_DATA_DIR", t.TempDir())
	t.Setenv("HOTBEDD_STATE_DIR", t.TempDir())
	s := New()
	a, _, _ := s.EnsureWorkspaceKey("sbA", "hermes", "up", "ws1")
	// second sandbox on the same folder while A lives: own key
	b, minted, _ := s.EnsureWorkspaceKey("sbB", "hermes", "up", "ws1")
	if !minted || b.Token == a.Token {
		t.Fatal("live sandbox key was shared")
	}
	// A deleted -> C on the same folder gets A's token back, bound to C
	_, _ = s.Revoke("sbA")
	c, minted, _ := s.EnsureWorkspaceKey("sbC", "hermes", "up", "ws1")
	if minted || c.Token != a.Token || c.Sandbox != "sbC" {
		t.Errorf("rebind = %+v minted=%v, want A's token on sbC", c, minted)
	}
	if k, ok := s.ByToken(a.Token); !ok || k.Sandbox != "sbC" {
		t.Errorf("token resolves to %+v %v", k, ok)
	}
	// other folder or other agent: fresh key
	if d, _, _ := s.EnsureWorkspaceKey("sbD", "hermes", "up", "ws2"); d.Token == a.Token {
		t.Error("key crossed workspaces")
	}
	if e, _, _ := s.EnsureWorkspaceKey("sbE", "codex", "up", "ws1"); e.Token == a.Token {
		t.Error("key crossed agents")
	}
}
