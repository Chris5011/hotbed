package proxy

import (
	"encoding/json"
	"testing"
)

func TestRewriteAnthropicGateway(t *testing.T) {
	in := `{"model":"m","system":[{"type":"text","text":"A"}],
	  "messages":[{"role":"user","content":[{"type":"text","text":"hi"}]},
	              {"role":"system","content":"B"},
	              {"role":"assistant","content":"ok"}],
	  "output_config":{"effort":"high"}}`
	out, changed := rewriteAnthropicGateway([]byte(in))
	if !changed {
		t.Fatal("expected a rewrite")
	}
	var got struct {
		System       []map[string]any `json:"system"`
		Messages     []map[string]any `json:"messages"`
		OutputConfig map[string]any   `json:"output_config"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.System) != 2 || got.System[0]["text"] != "A" || got.System[1]["text"] != "B" {
		t.Errorf("system = %v, want blocks A,B", got.System)
	}
	if len(got.Messages) != 2 || got.Messages[0]["role"] != "user" || got.Messages[1]["role"] != "assistant" {
		t.Errorf("messages = %v, want user,assistant", got.Messages)
	}
	if got.OutputConfig["effort"] != "xhigh" {
		t.Errorf("effort = %v, want xhigh", got.OutputConfig["effort"])
	}

	// string system + no system messages + medium effort: untouched
	plain := `{"system":"S","messages":[{"role":"user","content":"x"}],"output_config":{"effort":"medium"}}`
	if out, changed := rewriteAnthropicGateway([]byte(plain)); changed || string(out) != plain {
		t.Errorf("plain request rewritten: %s", out)
	}
	// string system gets merged as a block
	out, _ = rewriteAnthropicGateway([]byte(`{"system":"S","messages":[{"role":"system","content":[{"type":"text","text":"R"}]}]}`))
	var g2 struct {
		System   []map[string]any `json:"system"`
		Messages []any            `json:"messages"`
	}
	_ = json.Unmarshal(out, &g2)
	if len(g2.System) != 2 || g2.System[0]["text"] != "S" || g2.System[1]["text"] != "R" || len(g2.Messages) != 0 {
		t.Errorf("string system merge = %s", out)
	}
}
