package egressproxy

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hotbed/internal/dnsfence"
)

// setup writes a config (sb1 strict with allowlist example.com, sb2
// allowlist) and an attribution snapshot mapping 127.0.0.1 to sandbox src.
func setup(t *testing.T, src string) (*httptest.Server, *Handler) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOTBEDD_CONFIG_DIR", filepath.Join(dir, "cfg"))
	t.Setenv("HOTBEDD_STATE_DIR", filepath.Join(dir, "state"))
	_ = os.MkdirAll(filepath.Join(dir, "cfg"), 0o755)
	_ = os.MkdirAll(filepath.Join(dir, "state"), 0o755)
	cfg := map[string]any{
		"egress_mode":      map[string]string{"sb1": "strict", "sb2": "allowlist"},
		"egress_allowlist": map[string][]string{"sb1": {"example.com"}, "sb2": {"example.com"}},
		"egress_track":     map[string]bool{"sb1": true},
	}
	b, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(dir, "cfg", "config.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	ips, _ := json.Marshal(map[string]any{"ips": map[string]string{"127.0.0.1": src}})
	if err := os.WriteFile(dnsfence.IPsPath(), ips, 0o600); err != nil {
		t.Fatal(err)
	}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "upstream:"+r.Host+r.URL.Path)
	}))
	t.Cleanup(up.Close)
	h := New(dnsfence.NewJudge())
	upAddr := up.Listener.Addr().String()
	h.Dial = func(network, _ string) (net.Conn, error) { return net.Dial(network, upAddr) } // every host -> upstream
	px := httptest.NewServer(h)
	t.Cleanup(px.Close)
	return px, h
}

func connect(t *testing.T, px *httptest.Server, target string) (int, string) {
	t.Helper()
	c, err := net.Dial("tcp", px.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = io.WriteString(c, "CONNECT "+target+" HTTP/1.1\r\nHost: "+target+"\r\n\r\n")
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		return resp.StatusCode, ""
	}
	// speak plain HTTP through the tunnel to the fake upstream
	_, _ = io.WriteString(c, "GET /through HTTP/1.1\r\nHost: "+target+"\r\nConnection: close\r\n\r\n")
	in, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(in.Body)
	return 200, string(body)
}

func TestStrictConnectAllowlist(t *testing.T) {
	px, _ := setup(t, "sb1")
	if code, body := connect(t, px, "api.example.com:443"); code != 200 || !strings.Contains(body, "/through") {
		t.Fatalf("allowed subdomain: %d %q", code, body)
	}
	if code, _ := connect(t, px, "evil.org:443"); code != http.StatusForbidden {
		t.Fatalf("unlisted host: want 403, got %d", code)
	}
	if code, _ := connect(t, px, "notexample.com:443"); code != http.StatusForbidden {
		t.Fatalf("suffix lookalike: want 403, got %d", code)
	}
}

func TestStrictPlainHTTP(t *testing.T) {
	px, _ := setup(t, "sb1")
	pu, _ := url.Parse(px.URL)
	cl := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}, Timeout: 5 * time.Second}
	resp, err := cl.Get("http://example.com/x")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "/x") {
		t.Fatalf("plain allowed: %d %q", resp.StatusCode, body)
	}
	resp, err = cl.Get("http://evil.org/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("plain denied: want 403, got %d", resp.StatusCode)
	}
}

// Only sandboxes recorded strict may use the proxy; unknown sources and
// sandboxes on other presets are refused (fail closed).
func TestNonStrictSourcesRefused(t *testing.T) {
	for _, src := range []string{"sb2", ""} {
		px, _ := setup(t, src)
		if code, _ := connect(t, px, "example.com:443"); code != http.StatusForbidden {
			t.Fatalf("source %q: want 403, got %d", src, code)
		}
	}
}

func TestTrackRecordsRequests(t *testing.T) {
	px, h := setup(t, "sb1")
	connect(t, px, "api.example.com:443")
	connect(t, px, "evil.org:443")
	h.J.Flush()
	seen := dnsfence.ReadSeen("sb1")
	if e := seen.Domains["api.example.com"]; e == nil || e.Blocked != 0 {
		t.Fatalf("allowed host not recorded as allowed: %+v", seen.Domains)
	}
	if e := seen.Domains["evil.org"]; e == nil || e.Blocked != 1 {
		t.Fatalf("blocked host not recorded as blocked: %+v", seen.Domains)
	}
}
