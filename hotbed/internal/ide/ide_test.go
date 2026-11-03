package ide

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestContainerURI(t *testing.T) {
	u := ContainerURI("hotbed-x")
	const pfx = "vscode-remote://attached-container+"
	if !strings.HasPrefix(u, pfx) || !strings.HasSuffix(u, "/workspace") {
		t.Fatalf("uri %q", u)
	}
	b, err := hex.DecodeString(strings.TrimSuffix(strings.TrimPrefix(u, pfx), "/workspace"))
	if err != nil || string(b) != `{"containerName":"/hotbed-x"}` {
		t.Fatalf("payload %q %v", b, err)
	}
}

// The user's own config keeps every byte; the Include goes on top once,
// a backup is taken once, CRLF + BOM files stay CRLF + BOM.
func TestEnsureIncludeKeepsUserConfig(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config")
	orig := bom + "Host box\r\n  HostName 10.0.0.1\r\n"
	if err := os.WriteFile(p, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := ensureInclude(p); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := os.ReadFile(p)
	s := string(got)
	if !strings.HasPrefix(s, bom+"# hotbed") || strings.Count(s, includeLine) != 1 {
		t.Fatalf("include not on top exactly once: %q", s)
	}
	if !strings.HasSuffix(s, strings.TrimPrefix(orig, bom)) || !strings.Contains(s, includeLine+"\r\n") {
		t.Fatalf("user config changed or line endings mixed: %q", s)
	}
	if bak, _ := os.ReadFile(p + ".hotbed-bak"); string(bak) != orig {
		t.Fatalf("backup %q", bak)
	}
}

func TestEnsureIncludeCreatesMissingConfig(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config")
	if err := ensureInclude(p); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); !strings.Contains(string(b), includeLine+"\n") {
		t.Fatalf("got %q", b)
	}
	if _, err := os.Stat(p + ".hotbed-bak"); err == nil {
		t.Fatal("no backup expected for a new file")
	}
}

func TestReplaceBlock(t *testing.T) {
	p := filepath.Join(t.TempDir(), "hotbed.conf")
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(replaceBlock(p, "hotbed-a", "Host hotbed-a\n  HostName a\n"))
	must(replaceBlock(p, "hotbed-b", "Host hotbed-b\n  HostName b\n"))
	must(replaceBlock(p, "hotbed-a", "Host hotbed-a\n  HostName a2\n"))
	b, _ := os.ReadFile(p)
	s := string(b)
	if strings.Count(s, "Host hotbed-a\n") != 1 || !strings.Contains(s, "HostName a2") ||
		strings.Contains(s, "HostName a\n") || !strings.Contains(s, "HostName b") {
		t.Fatalf("replace: %q", s)
	}
	must(replaceBlock(p, "hotbed-b", ""))
	b, _ = os.ReadFile(p)
	if strings.Contains(string(b), "hotbed-b") || !strings.Contains(string(b), "a2") {
		t.Fatalf("remove: %q", b)
	}
}

const bom = string(rune(0xFEFF))
