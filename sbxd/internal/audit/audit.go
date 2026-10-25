// Package audit appends sbxd's control-plane actions as JSONL in the state
// dir (audit.jsonl): sandbox start/stop/spawn, key mint/revoke, wallet
// changes, mount rebuilds, egress changes. Recording is best-effort by
// design — a failed audit write never breaks the action it documents.
package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"sbxd/internal/env"
)

// Entry is one trail line.
type Entry struct {
	TS      string `json:"ts"`
	Kind    string `json:"kind"`
	Sandbox string `json:"sandbox,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

func path() string { return filepath.Join(env.StateDir(), "audit.jsonl") }

// Record appends one event (best-effort).
func Record(kind, sandbox, detail string) {
	dir := filepath.Dir(path())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	e := Entry{
		TS:      time.Now().UTC().Format(time.RFC3339),
		Kind:    kind,
		Sandbox: sandbox,
		Detail:  detail,
	}
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	f, err := os.OpenFile(path(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close() //nolint:errcheck
	_, _ = f.Write(append(b, '\n'))
}

// Tail returns the last n entries (oldest first). Malformed lines are
// skipped; a missing file yields nil.
func Tail(n int) []Entry {
	b, err := os.ReadFile(path())
	if err != nil {
		return nil
	}
	var out []Entry
	start := 0
	for i := 0; i <= len(b); i++ {
		if i == len(b) || b[i] == '\n' {
			if i > start {
				var e Entry
				if json.Unmarshal(b[start:i], &e) == nil && e.Kind != "" {
					out = append(out, e)
				}
			}
			start = i + 1
		}
	}
	if n > 0 && len(out) > n {
		out = out[len(out)-n:]
	}
	return out
}
