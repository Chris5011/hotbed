package engine

import (
	"strings"
	"testing"
)

// TestAllowlistKeepsSyncedProxy (issue #3): the synced proxy endpoint is
// accepted before the final DROP; malformed or non-IP entries are ignored.
func TestAllowlistKeepsSyncedProxy(t *testing.T) {
	s := egressAllowlistScript([]string{"1.2.3.4"}, "172.17.0.1", false,
		[]string{"172.30.112.5:7775", "172.17.0.1:7790", "host.docker.internal:7775", "garbage"})
	for _, want := range []string{
		"-A HBD_EGRESS -d 172.30.112.5 -p tcp --dport 7775 -j ACCEPT",
		"-A HBD_EGRESS -d 172.17.0.1 -p tcp --dport 7790 -j ACCEPT",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(s, "host.docker.internal") || strings.Contains(s, "garbage") {
		t.Error("non-IP endpoint leaked into iptables")
	}
	if strings.Index(s, "172.30.112.5") > strings.Index(s, "-A HBD_EGRESS -j DROP") {
		t.Error("proxy ACCEPT after the DROP")
	}
}
