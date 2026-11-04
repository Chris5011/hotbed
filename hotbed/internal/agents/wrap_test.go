package agents

import (
	"os/exec"
	"strings"
	"testing"
)

func TestShellWrappers(t *testing.T) {
	mods := []Module{
		{ID: "claude-code", Command: "claude"},
		{ID: "openclaw", Command: "timeout 20 openclaw gateway stop >/dev/null 2>&1; openclaw chat"},
		{ID: "dup", Command: "claude"}, // first wins
	}
	w := ShellWrappers("/home/hbd", mods)
	for _, want := range []string{
		`claude() { ( f='/home/hbd/.config/hotbed/agents/claude-code.env'; [ -f "$f" ] && . "$f"; exec claude "$@" ); }`,
		`openclaw() { ( f='/home/hbd/.config/hotbed/agents/openclaw.env';`,
	} {
		if !strings.Contains(w, want) {
			t.Fatalf("missing %q in:\n%s", want, w)
		}
	}
	if strings.Count(w, "claude()") != 1 {
		t.Fatalf("duplicate wrapper:\n%s", w)
	}
	if _, err := exec.LookPath("bash"); err == nil {
		if out, err := exec.Command("bash", "-n", "-c", w).CombinedOutput(); err != nil {
			t.Fatalf("wrappers do not parse: %v %s", err, out)
		}
	}
}
