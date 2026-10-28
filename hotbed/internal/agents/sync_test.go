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
	if len(b.runs) != 1 || !strings.Contains(b.runs[0], ". '/home/hbd/.config/hotbed/agents/x.env'") ||
		!strings.Contains(b.runs[0], "x set model 'b'") {
		t.Errorf("setup run = %q", b.runs)
	}
}
