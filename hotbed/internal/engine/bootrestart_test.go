package engine

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBootRestartable(t *testing.T) {
	race := `failed to create task for container: ... error mounting "/run/desktop/mnt/host/wsl/docker-desktop-bind-mounts/kali-linux/66711402" to rootfs at "/home/hbd/.gitconfig": ... not a directory: Are you trying to mount a directory onto a file (or vice-versa)?`
	cases := []struct {
		err, policy string
		want        bool
	}{
		{race, "unless-stopped", true},
		{race, "always", true},
		{race, "no", false},                                         // nobody asked docker to keep it up
		{"", "unless-stopped", false},                               // stopped by the user
		{"OCI runtime: exec format error", "unless-stopped", false}, // not a mount problem
	}
	for _, c := range cases {
		if got := bootRestartable(c.err, c.policy); got != c.want {
			t.Fatalf("bootRestartable(%q, %q) = %v", c.err[:min(len(c.err), 40)], c.policy, got)
		}
	}
}

func TestDisplaySource(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("HOTBEDD_STATE_DIR", t.TempDir())
	p := filepath.Join(home, "projects", "app")
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	mirrorPaths, mirrorScanned = nil, false
	const pfx = "/run/desktop/mnt/host/wsl/docker-desktop-bind-mounts/kali-linux/"
	for _, want := range []string{p, filepath.Join(home, ".gitconfig")} {
		if got := DisplaySource(pfx + pathHash(want)); got != want {
			t.Fatalf("mirror of %s resolved to %s", want, got)
		}
	}
	unknown := pfx + pathHash("/nowhere")
	if got := DisplaySource(unknown); got != unknown {
		t.Fatalf("unknown mirror must stay as is, got %s", got)
	}
	if got := DisplaySource("/home/x/plain"); got != "/home/x/plain" {
		t.Fatalf("non-mirror changed: %s", got)
	}
}
