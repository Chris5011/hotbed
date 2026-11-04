package api

import (
	"encoding/csv"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hotbed/internal/ledger"
)

func TestCSVSafe(t *testing.T) {
	for in, want := range map[string]string{
		"=HYPERLINK(1)": "'=HYPERLINK(1)", "+1": "'+1", "-cmd": "'-cmd", "@x": "'@x",
		"api.openai.com": "api.openai.com", "": "",
	} {
		if got := csvSafe(in); got != want {
			t.Fatalf("csvSafe(%q) = %q", in, got)
		}
	}
}

func researchFixture(t *testing.T) *Server {
	t.Helper()
	st := t.TempDir()
	t.Setenv("HOTBEDD_STATE_DIR", st)
	lines := `{"ts":"2026-11-01T10:00:00Z","sandbox":"a","provider":"p","agent":"claude-code","model":"m1","in":10,"out":5,"cost":0.5,"dur_ms":100,"status":200}
{"ts":"2026-11-01T11:00:00Z","sandbox":"a","provider":"p","model":"m1","in":1,"out":1,"est":true,"cost":0.25,"dur_ms":50,"status":200}
{"ts":"2026-11-02T09:00:00Z","sandbox":"gone","provider":"p","model":"=evil","in":2,"out":3,"cost":1,"dur_ms":10,"status":200}
`
	if err := os.WriteFile(filepath.Join(st, "usage.jsonl"), []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(st, "dns-seen"), 0o755); err != nil {
		t.Fatal(err)
	}
	seen := `{"sandbox":"gone","domains":{"github.com":{"first":"2026-11-01T00:00:00Z","last":"2026-11-02T00:00:00Z","count":4,"blocked":1},"=x.example":{"count":1}}}`
	if err := os.WriteFile(filepath.Join(st, "dns-seen", "gone.json"), []byte(seen), 0o600); err != nil {
		t.Fatal(err)
	}
	return &Server{Led: ledger.Open(nil)}
}

func readCSV(t *testing.T, body string) [][]string {
	t.Helper()
	rows, err := csv.NewReader(strings.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatalf("csv: %v\n%s", err, body)
	}
	return rows
}

func TestUsageCSV(t *testing.T) {
	s := researchFixture(t)
	w := httptest.NewRecorder()
	s.usageCSV(w, httptest.NewRequest("GET", "/api/usage.csv", nil))
	rows := readCSV(t, w.Body.String())
	if len(rows) != 4 || rows[0][0] != "ts_utc" {
		t.Fatalf("calls export: %v", rows)
	}
	if rows[3][5] != "'=evil" || rows[3][2] != "true" { // injection defused, sandbox gone = stale
		t.Fatalf("row 3: %v", rows[3])
	}
	if !strings.Contains(w.Header().Get("Content-Disposition"), "hotbed-burn-all-") {
		t.Fatalf("filename: %s", w.Header().Get("Content-Disposition"))
	}
	w = httptest.NewRecorder()
	s.usageCSV(w, httptest.NewRequest("GET", "/api/usage.csv?group=day&sandbox=a", nil))
	rows = readCSV(t, w.Body.String())
	// one day for sandbox a: 2 calls, 11 in, 6 out, 1 estimated, $0.75
	if len(rows) != 2 || strings.Join(rows[1], ",") != "2026-11-01,a,true,2,11,6,1,0.750000" {
		t.Fatalf("daily export: %v", rows)
	}
}

func TestDNSCSV(t *testing.T) {
	s := researchFixture(t)
	w := httptest.NewRecorder()
	s.dnsCSV(w, httptest.NewRequest("GET", "/api/dns.csv", nil))
	rows := readCSV(t, w.Body.String())
	if len(rows) != 3 {
		t.Fatalf("dns export: %v", rows)
	}
	if strings.Join(rows[2], ",") != "gone,true,github.com,4,1,2026-11-01T00:00:00Z,2026-11-02T00:00:00Z" {
		t.Fatalf("github row: %v", rows[2])
	}
	if rows[1][2] != "'=x.example" {
		t.Fatalf("injection row: %v", rows[1])
	}
}
