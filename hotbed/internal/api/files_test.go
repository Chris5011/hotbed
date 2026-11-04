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
	// like `ls -1A` in en_US.UTF-8: no folders-first, case and dots ignored
	if got := strings.Join(names, ","); got != "Alpha,Apple.md,beta,.cache,.env,linked,zeta.txt" {
		t.Fatalf("order: %s", got)
	}
	if !list[5].Dir || list[5].Type != "link" || list[5].Target != "/opt/real" {
		t.Fatalf("link to a folder: %+v", list[5])
	}
	if list[6].Size != 12 || list[6].MTime != 1700000000 {
		t.Fatalf("file fields: %+v", list[6])
	}
	if _, _, err := parseListing("HBDERR no such directory\n"); err == nil || err.Error() != "no such directory" {
		t.Fatalf("error passthrough: %v", err)
	}
}

// The exact order `LC_ALL=en_US.UTF-8 ls -1A` printed for this set on the
// dev box (glibc 2.41).
func TestLsSortMatchesLs(t *testing.T) {
	in := []string{"zeta.txt", "_private", "my_dir", ".env", "Alpha", "10.txt", "README", "beta", "my-dir", ".cache", "9.txt", "linked", "Apple.md"}
	var list []fileEnt
	for _, n := range in {
		list = append(list, fileEnt{Name: n})
	}
	lsSort(list)
	var got []string
	for _, e := range list {
		got = append(got, e.Name)
	}
	want := "10.txt 9.txt Alpha Apple.md beta .cache .env linked my-dir my_dir _private README zeta.txt"
	if strings.Join(got, " ") != want {
		t.Fatalf("got  %s\nwant %s", strings.Join(got, " "), want)
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
