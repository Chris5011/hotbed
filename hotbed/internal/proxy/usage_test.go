package proxy

import "testing"

// TestUsageFormats covers every wire shape the ledger reads usage from.
func TestUsageFormats(t *testing.T) {
	cases := []struct {
		name    string
		frames  string
		in, out int
	}{
		{"openai chat final chunk",
			`{"choices":[],"usage":{"prompt_tokens":12,"completion_tokens":7}}`, 12, 7},
		{"anthropic start+delta",
			`{"type":"message_start","message":{"usage":{"input_tokens":30}}}` + "\n" +
				`{"type":"message_delta","usage":{"output_tokens":9}}`, 30, 9},
		{"openai responses completed",
			`{"type":"response.output_text.delta","delta":"hi"}` + "\n" +
				`{"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":40,"output_tokens":5,"total_tokens":45}}}`, 40, 5},
	}
	for _, c := range cases {
		in, out, ok := parseUsageFrames(c.frames)
		if !ok || in != c.in || out != c.out {
			t.Errorf("%s: got (%d,%d,%v), want (%d,%d,true)", c.name, in, out, ok, c.in, c.out)
		}
	}
}

// TestDeltaText: estimates need the emitted text of every stream shape.
func TestDeltaText(t *testing.T) {
	cases := map[string]string{
		`{"choices":[{"delta":{"content":"ab"}}]}`:              "ab",
		`{"type":"content_block_delta","delta":{"text":"cd"}}`:  "cd",
		`{"type":"response.output_text.delta","delta":"ef"}`:    "ef",
		`{"type":"response.completed","response":{"usage":{}}}`: "",
	}
	for in, want := range cases {
		if got := extractDeltaText(in); got != want {
			t.Errorf("extractDeltaText(%s) = %q, want %q", in, got, want)
		}
	}
}
