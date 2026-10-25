package sandbox

// Boot self-registration tracking for VMs. The kit (vm/sbxvm.sh) signals the
// controller when a guest boot begins and again once the guest is confirmed
// up (ssh + cloud-init provisioning). Between those two signals the engine
// already reports "running" long before the guest actually boots — the
// collector rewrites such VM rows to an honest "booting" state while a
// pending boot exists, and the boot-end handler re-establishes the proxy
// tunnel for synced VMs. Signals persist across daemon restarts
// (boot_pending.json) and a begin without its end expires after ~30 minutes
// (treated as a failed or abandoned boot: the row fades back to whatever the
// engine itself reports).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"sbxd/internal/env"
)

// bootStale caps how long a begin signal may linger without its end. A
// legitimate worst-case boot is ssh (900s) + cloud-init (720s) ≈ 27 min, so
// 30 min gives every real boot ample headroom before it stops overriding the
// engine state.
const bootStale = 30 * time.Minute

type bootTracker struct {
	mu   sync.Mutex
	pend map[string]time.Time // sandbox name -> begin signal time
	path string               // boot_pending.json next to audit.jsonl
}

var (
	bootOnce sync.Once
	bootTrk  *bootTracker
)

func bootTrackerFor() *bootTracker {
	bootOnce.Do(func() {
		bootTrk = &bootTracker{
			pend: map[string]time.Time{},
			path: filepath.Join(env.StateDir(), "boot_pending.json"),
		}
		bootTrk.load()
	})
	return bootTrk
}

// NoteBootBegin records a boot-start signal. Idempotent: repeated begins for
// an already-pending sandbox are accepted (200) and change nothing.
func NoteBootBegin(name string) {
	t := bootTrackerFor()
	t.mu.Lock()
	if _, ok := t.pend[name]; !ok {
		t.pend[name] = time.Now()
		t.saveLocked()
	}
	t.mu.Unlock()
}

// NoteBootEnd clears a pending boot; reports whether one was pending (true
// = a real begin→end transition; false = end without begin, e.g. a foreign
// boot whose begin signal was lost — still accepted and audited).
func NoteBootEnd(name string) bool {
	t := bootTrackerFor()
	t.mu.Lock()
	_, ok := t.pend[name]
	if ok {
		delete(t.pend, name)
		t.saveLocked()
	}
	t.mu.Unlock()
	return ok
}

// IsPendingBoot reports whether the sandbox signalled boot-begin without
// boot-end so far (and not stale-pruned yet).
func IsPendingBoot(name string) bool {
	t := bootTrackerFor()
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pruneLocked()
	_, ok := t.pend[name]
	return ok
}

// pruneLocked drops stale entries and persists when anything was dropped.
// Caller holds t.mu.
func (t *bootTracker) pruneLocked() {
	changed := false
	for k, ts := range t.pend {
		if time.Since(ts) > bootStale {
			delete(t.pend, k)
			changed = true
		}
	}
	if changed {
		t.saveLocked()
	}
}

// load restores pending signals from disk (only entries young enough to
// still matter); a missing or unreadable file is simply an empty tracker —
// boot tracking is always best-effort and never blocks the daemon.
func (t *bootTracker) load() {
	raw, err := os.ReadFile(t.path)
	if err != nil {
		return
	}
	var m map[string]string
	if json.Unmarshal(raw, &m) != nil {
		return
	}
	for k, v := range m {
		if ts, err := time.Parse(time.RFC3339, v); err == nil && time.Since(ts) <= bootStale {
			t.pend[k] = ts
		}
	}
}

// saveLocked writes boot_pending.json atomically; tolerated failures leave
// the in-memory tracker authoritative for this process lifetime. Caller
// holds t.mu.
func (t *bootTracker) saveLocked() {
	if t.path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(t.path), 0o700); err != nil {
		return
	}
	m := make(map[string]string, len(t.pend))
	for k, ts := range t.pend {
		m[k] = ts.Format(time.RFC3339)
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return
	}
	tmp := t.path + ".tmp"
	if os.WriteFile(tmp, raw, 0o600) == nil {
		_ = os.Rename(tmp, t.path)
	}
}
