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
