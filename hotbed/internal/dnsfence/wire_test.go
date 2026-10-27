package dnsfence

import (
	"bytes"
	"net"
	"testing"

	"hotbed/internal/config"
)

// qA is a real-ish UDP query: example.com A IN, id 0x1234, RD set.
// dig +short shows the same on "example.com A +qr".
var qExampleA = []byte{
	0x12, 0x34, // id
	0x01, 0x00, // flags: RD
	0x00, 0x01, // QDCOUNT 1
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	7, 'e', 'x', 'a', 'm', 'p', 'l', 'e',
	3, 'c', 'o', 'm',
	0,
	0, 1, // QTYPE A
	0, 1, // QCLASS IN
}

// upExampleA is an upstream response: 2 A answers, second name compressed.
var upExampleA = []byte{
	0x12, 0x34,
	0x81, 0x80, // QR RD RA
	0x00, 0x01, 0x00, 0x02, 0x00, 0x00, 0x00, 0x00,
	7, 'e', 'x', 'a', 'm', 'p', 'l', 'e', 3, 'c', 'o', 'm', 0,
	0, 1, 0, 1,
	// answer 1: example.com A 93.184.216.34
	0xC0, 0x0C, // compression pointer
	0, 1, 0, 1, 0, 0, 0, 60, 0, 4,
	93, 184, 216, 34,
	// answer 2: www.example.com A 192.0.2.7 (pointer to offset 12)
	3, 'w', 'w', 'w', 0xC0, 0x0C,
	0, 1, 0, 1, 0, 0, 1, 4, 0, 4,
	192, 0, 2, 7,
}

func TestParseQuery(t *testing.T) {
	q, ok := parseQuery(qExampleA)
	if !ok {
		t.Fatal("canonical A query did not parse")
	}
	if q.id != 0x1234 {
		t.Errorf("id = %#x", q.id)
	}
	if q.domain != "example.com" {
		t.Errorf("domain = %q", q.domain)
	}
	if !q.hasA || !q.recDes {
		t.Errorf("hasA=%v recDes=%v", q.hasA, q.recDes)
	}
	// a response is not a query
	if _, ok := parseQuery(upExampleA); ok {
		t.Error("upstream response parsed as a query")
	}
	// truncated junk refused
	if _, ok := parseQuery([]byte{0, 1, 2, 3, 4, 5, 0x01, 0x00, 0, 1}); ok {
		t.Error("truncated packet parsed")
	}
	// non-A/AAAA type refused (TXT=16)
	txt := append(append([]byte{}, qExampleA[:len(qExampleA)-4]...), 0, 16, 0, 1)
	if _, ok := parseQuery(txt); ok {
		t.Error("TXT query parsed")
	}
}

func TestRefusedEchoesQuestion(t *testing.T) {
	q, ok := parseQuery(qExampleA)
	if !ok {
		t.Fatal("query did not parse")
	}
	resp := q.refusedA()
	if resp[0] != 0x12 || resp[1] != 0x34 {
		t.Errorf("id not echoed: % x", resp[:2])
	}
	if got := int(resp[3]) & 0x0F; got != rcREFUSED {
		t.Errorf("rcode = %d", got)
	}
	if resp[4] != 0 || resp[5] != 1 {
		t.Errorf("QDCOUNT = % x", resp[4:6])
	}
	if !bytes.HasSuffix(resp, qExampleA[12:]) {
		t.Errorf("question not echoed: % x", resp[12:])
	}
}

func TestWithA(t *testing.T) {
	q, _ := parseQuery(qExampleA)
	resp := q.withA([][]byte{{93, 184, 216, 34}})
	if an := int(resp[6])<<8 | int(resp[7]); an != 1 {
		t.Fatalf("ANCOUNT = %d", an)
	}
	ips := extractIPv4(resp)
	if len(ips) != 1 || !net.IP(ips[0]).Equal(net.IPv4(93, 184, 216, 34)) {
		t.Errorf("extractIPv4(from withA) = %v", ips)
	}
}

func TestExtractIPv4(t *testing.T) {
	ips := extractIPv4(upExampleA)
	want := [][]byte{
		{93, 184, 216, 34},
		{192, 0, 2, 7},
	}
	if len(ips) != len(want) {
		t.Fatalf("got %d ips, want %d", len(ips), len(want))
	}
	for i, w := range want {
		if !net.IP(ips[i]).Equal(net.IP(w)) {
			t.Errorf("ip %d = %v, want %v", i, ips[i], w)
		}
	}
	// garbage tolerated (empty result, not a panic)
	if extractIPv4([]byte{0, 0}) != nil {
		t.Error("short packet yielded ips")
	}
}

func TestParseQueryAAAA(t *testing.T) {
	aaaa := append(append([]byte{}, qExampleA[:len(qExampleA)-4]...), 0, 28, 0, 1)
	q, ok := parseQuery(aaaa)
	if !ok {
		t.Fatal("AAAA query did not parse")
	}
	if q.hasA {
		t.Error("AAAA flagged as A")
	}
	if out := q.withA([][]byte{{1, 2, 3, 4}}); out == nil {
		t.Error("noRecords path for AAAA gave nil")
	}
}

// TestAllowed covers allowlist suffix matching + entry normalization.
func TestAllowed(t *testing.T) {
	f := &fence{
		load: func() (*config.Config, error) {
			return &config.Config{
				EgressAllowlist: map[string][]string{
					"sbx-a": {"Example.com.", " *.DEV.io "},
				},
			}, nil
		},
	}
	yes := []string{"example.com", "api.example.com", "Example.COM", "api.dev.io", "deep.foo.dev.io"}
	no := []string{"notexample.com", "foo.io", "evil.com", "example.org", ""}
	for _, q := range yes {
		if !f.allowed("sbx-a", q) {
			t.Errorf("allowed(sbx-a, %q) = false, want true", q)
		}
	}
	for _, q := range no {
		if f.allowed("sbx-a", q) {
			t.Errorf("allowed(sbx-a, %q) = true, want false", q)
		}
	}
	if f.allowed("sbx-unknown", "example.com") {
		t.Error("unknown sandbox allowed")
	}
	if f.allowed("", "example.com") {
		t.Error("empty sandbox allowed")
	}
}
