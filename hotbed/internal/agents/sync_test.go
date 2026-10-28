package agents

import (
	"strings"
	"testing"
	"time"

	"hotbed/internal/config"
	"hotbed/internal/keys"
)

// fakeBox records what Sync writes and runs.
type fakeBox struct {
	files map[string]string
	runs  []string
}

func (b *fakeBox) Name() string { return "sb1" }
func (b *fakeBox) Home() string { return "/home/hbd" }
func (b *fakeBox) RunRoot([]byte, map[string]string, time.Duration) (string, error) {
	return "", nil
}
func (b *fakeBox) RunUser(s string, _ time.Duration) (string, error) {
	b.runs = append(b.runs, s)
	return "", nil
}
func (b *fakeBox) Write(p string, d []byte) error { b.files[p] = string(d); return nil }

// TestSyncModelLists: list templates carry every provider model, default
// first; setup gets shell-quoted scalars and runs with the env loaded.
func TestSyncModelLists(t *testing.T) {
	t.Setenv("HOTBEDD_DATA_DIR", t.TempDir())
	t.Setenv("HOTBEDD_STATE_DIR", t.TempDir())
	cfg := &config.Config{Opencode: config.OC{
		DefaultProvider: "up",
		DefaultModel:    "b",
		Providers: map[string]config.Provider{
			"up": {BaseURL: "https://upstream/v1", Models: []string{"a", "b", "c"}},
		},
	}}
	m := Module{ID: "x", Name: "X", Command: "x", Proxy: &Proxy{
		Style: "openai",
		Files: map[string]string{
			"~/ids.json":  "{{models_json}}",
			"~/objs.json": "{{models_objects}}",
		},
		Setup: "x set model {{model}}",
	}}
	b := &fakeBox{files: map[string]string{}}
	res, err := Sync(b, m, cfg, keys.New(), "http://gw:7775/v1")
	if err != nil {
		t.Fatal(err)
	}
	if res.Model != "b" || res.Provider != "up" {
		t.Errorf("result = %+v", res)
	}
	if got := b.files["/home/hbd/ids.json"]; got != `["b","a","c"]` {
		t.Errorf("models_json = %s", got)
	}
	if got := b.files["/home/hbd/objs.json"]; !strings.HasPrefix(got, `[{"id":"b","name":"b"},{"id":"a"`) {
		t.Errorf("models_objects = %s", got)
	}
	// runs: workspace id, then the setup command (env loaded)
	last := b.runs[len(b.runs)-1]
	if len(b.runs) != 2 || !strings.Contains(b.runs[0], "/workspace/.hotbed") ||
		!strings.Contains(last, ". '/home/hbd/.config/hotbed/agents/x.env'") || !strings.Contains(last, "x set model 'b'") {
		t.Errorf("runs = %q", b.runs)
	}
}

// TestSyncPersistWithoutProvider: sessions are persisted even when no
// provider exists (proxy part skipped); links cover dirs and files.
func TestSyncPersistWithoutProvider(t *testing.T) {
	t.Setenv("HOTBEDD_DATA_DIR", t.TempDir())
	t.Setenv("HOTBEDD_STATE_DIR", t.TempDir())
	m := Module{ID: "x", Name: "X", Command: "x", Resume: "x --continue",
		Proxy: &Proxy{Style: "openai", Env: map[string]string{"K": "{{key}}"}},
		Persist: &Persist{
			Env:   map[string]string{"X_HOME": "{{state}}"},
			Links: map[string]string{"~/.x/sessions": "sessions/", "~/.x/state.db": "state.db"},
		}}
	b := &fakeBox{files: map[string]string{}}
	res, err := Sync(b, m, &config.Config{}, keys.New(), "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Persisted != "/workspace/.hotbed/agents/x" || res.Skipped == "" {
		t.Errorf("result = %+v", res)
	}
	env := b.files["/home/hbd/.config/hotbed/agents/x.env"]
	if !strings.Contains(env, "export X_HOME='/workspace/.hotbed/agents/x'") || strings.Contains(env, "export K=") {
		t.Errorf("env file = %q", env)
	}
	if len(b.runs) != 1 ||
		!strings.Contains(b.runs[0], "link_dir '/home/hbd/.x/sessions' '/workspace/.hotbed/agents/x/sessions'") ||
		!strings.Contains(b.runs[0], "link_file '/home/hbd/.x/state.db' '/workspace/.hotbed/agents/x/state.db'") ||
		!strings.Contains(b.runs[0], ".gitignore") {
		t.Errorf("persist script = %q", b.runs)
	}
	if got := LaunchScript("/home/hbd", m, true); !strings.Contains(got, "; x --continue;") {
		t.Errorf("resume launch = %q", got)
	}
}
