package api

import (
	"strings"
	"testing"
)

func TestParseListingAndOrder(t *testing.T) {
	out := "noise\nHBDOK\n" + strings.Join([]string{
		"f\tf\t12\t1700000000.5\tzeta.txt\t",
		"d\td\t4096\t1700000000\tAlpha\t",
		"d\td\t4096\t1700000000\t.cache\t",
		"l\td\t9\t1700000000\tlinked\t/opt/real",
		"f\tf\t1\t1700000000\t.env\t",
		"d\td\t4096\t1700000000\tbeta\t",
		"f\tf\t3\t1700000000\tApple.md\t",
	}, "\n") + "\n"
	list, trunc, err := parseListing(out)
	if err != nil || trunc {
		t.Fatalf("parse: %v %v", err, trunc)
	}
	var names []string
	for _, e := range list {
		names = append(names, e.Name)
	}
	// folders (and links to folders) first, visible before hidden, case-insensitive
	if got := strings.Join(names, ","); got != "Alpha,beta,linked,.cache,Apple.md,zeta.txt,.env" {
		t.Fatalf("order: %s", got)
	}
	if !list[2].Dir || list[2].Type != "link" || list[2].Target != "/opt/real" {
		t.Fatalf("link to a folder: %+v", list[2])
	}
	if list[5].Size != 12 || list[5].MTime != 1700000000 {
		t.Fatalf("file fields: %+v", list[5])
	}
	if _, _, err := parseListing("HBDERR no such directory\n"); err == nil || err.Error() != "no such directory" {
		t.Fatalf("error passthrough: %v", err)
	}
}

func TestCleanSandboxPath(t *testing.T) {
	for in, want := range map[string]string{"": "/workspace", "/workspace/../etc/": "/etc", "/": "/"} {
		if got, err := cleanSandboxPath(in); err != nil || got != want {
			t.Fatalf("%q -> %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"rel/path", "/a\nb"} {
		if _, err := cleanSandboxPath(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	// quotes stay inside the single-quoted shell word
	if s := listScript("/tmp/it's"); !strings.Contains(s, `d='/tmp/it'\''s'`) {
		t.Fatalf("quoting: %s", s)
	}
}
