package dnsfence

import (
	"bytes"
	"net"
	"testing"
	"time"

	"hotbed/internal/config"
)

// fakeUpstream answers every query with upExampleA (id echoed).
func fakeUpstream(t *testing.T) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 4096)
		for {
			_, src, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			resp := append([]byte{}, upExampleA...)
			copy(resp[:2], buf[:2]) // echo the transaction id
			_, _ = pc.WriteTo(resp, src)
		}
	}()
	return pc.LocalAddr().String()
}

func testFence(t *testing.T, cfg *config.Config) *fence {
	t.Helper()
	t.Setenv("HOTBEDD_STATE_DIR", t.TempDir())
	t.Setenv("HOTBEDD_CONFIG_DIR", t.TempDir())
	return &fence{
		upstream: fakeUpstream(t),
		load:     func() (*config.Config, error) { return cfg, nil },
		logged:   map[string]time.Time{},
		lookups:  newSeenLog(),
		ipMap: map[string]string{
			"172.17.0.5": "open-sb",
			"172.17.0.6": "fenced-sb",
			"172.17.0.7": "quiet-sb",
		},
	}
}

func src(ip string) net.Addr { return &net.UDPAddr{IP: net.ParseIP(ip), Port: 40000} }

func isRefused(resp []byte) bool { return len(resp) > 3 && resp[3]&0x0F == rcREFUSED }

// TestArbitrateTracking covers the three tracking shapes: an open (allow)
// sandbox passes and records, a fenced one refuses and records the block,
// an untracked fenced one refuses silently; unknown sources stay refused.
func TestArbitrateTracking(t *testing.T) {
	cfg := &config.Config{
		EgressAllowlist: map[string][]string{"fenced-sb": {"other.org"}},
		EgressMode:      map[string]string{"open-sb": "allow", "fenced-sb": "allowlist", "quiet-sb": "allowlist"},
		EgressTrack:     map[string]bool{"open-sb": true, "fenced-sb": true},
	}
	f := testFence(t, cfg)

	resp, sb, ips := f.arbitrate(src("172.17.0.5"), qExampleA)
	if isRefused(resp) || !bytes.Equal(resp[2:], upExampleA[2:]) {
		t.Fatalf("open sandbox: want the upstream answer verbatim, got %x", resp)
	}
	if sb != "" || ips != nil {
		t.Errorf("open sandbox announced ips (sb=%q, %d ips) - it has no chain to open", sb, len(ips))
	}

	if resp, _, _ := f.arbitrate(src("172.17.0.6"), qExampleA); !isRefused(resp) {
		t.Error("fenced sandbox: unlisted domain not refused")
	}
	if resp, _, _ := f.arbitrate(src("172.17.0.7"), qExampleA); !isRefused(resp) {
		t.Error("untracked fenced sandbox: unlisted domain not refused")
	}
	if resp, _, _ := f.arbitrate(src("172.17.0.99"), qExampleA); !isRefused(resp) {
		t.Error("unknown source not refused")
	}

	f.lookups.flush()
	open := ReadSeen("open-sb").Domains["example.com"]
	if open == nil || open.Count != 1 || open.Blocked != 0 {
		t.Errorf("open-sb record = %+v, want 1 lookup, 0 blocked", open)
	}
	fenced := ReadSeen("fenced-sb").Domains["example.com"]
	if fenced == nil || fenced.Count != 1 || fenced.Blocked != 1 {
		t.Errorf("fenced-sb record = %+v, want 1 lookup, 1 blocked", fenced)
	}
	if n := len(ReadSeen("quiet-sb").Domains); n != 0 {
		t.Errorf("untracked sandbox recorded %d domains", n)
	}
}

// TestSeenMergeAndClear: flushes accumulate into the file; a clear (file
// removed by the daemon) restarts the record instead of resurrecting it.
func TestSeenMergeAndClear(t *testing.T) {
	t.Setenv("HOTBEDD_STATE_DIR", t.TempDir())
	l := newSeenLog()
	l.record("sb", "a.example.com", false)
	l.record("sb", "a.example.com", true)
	l.flush()
	l.record("sb", "a.example.com", false)
	l.record("sb", "bad\"name<x>", false) // hostile qname: never recorded
	l.flush()
	got := ReadSeen("sb").Domains
	if e := got["a.example.com"]; e == nil || e.Count != 3 || e.Blocked != 1 {
		t.Fatalf("merged = %+v, want 3 lookups, 1 blocked", e)
	}
	if len(got) != 1 {
		t.Errorf("recorded %d domains, want 1 (hostile name dropped)", len(got))
	}
	if err := ClearSeen("sb"); err != nil {
		t.Fatal(err)
	}
	l.record("sb", "b.example.com", false)
	l.flush()
	got = ReadSeen("sb").Domains
	if _, old := got["a.example.com"]; old || got["b.example.com"] == nil {
		t.Errorf("after clear: %v, want only b.example.com", got)
	}
}
