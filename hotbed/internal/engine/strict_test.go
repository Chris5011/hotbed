package engine

import (
	"strings"
	"testing"
)

// The guest side of the LLM tunnel listens on the configured proxy.port;
// the hand-over at a strict switch must free exactly that port.
func TestDropTunnelScriptUsesConfiguredPort(t *testing.T) {
	t.Setenv("HOTBEDD_PROXY_PORT", "")
	if s := dropTunnelScript("7799"); !strings.Contains(s, "'sport = :7799'") || strings.Contains(s, ":7775") {
		t.Fatalf("custom port not used: %s", s)
	}
	if s := dropTunnelScript(""); !strings.Contains(s, "'sport = :7775'") {
		t.Fatalf("empty port must fall back to the default: %s", s)
	}
}
