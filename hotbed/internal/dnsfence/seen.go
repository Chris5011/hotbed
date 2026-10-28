package dnsfence

// DNS lookup tracking: the "which domains does this sandbox try to reach"
// record behind the dashboard's DNS tab.
//
// The fence counts lookups in memory and flushes them every couple of
// seconds into one aggregate file per sandbox (state dir
// dns-seen/<name>.json). Each flush re-reads the file and merges only the
// pending deltas, so the daemon may delete a file at any time ("clear")
// without fighting the fence for ownership — the next flush simply starts
// a fresh one.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"hotbed/internal/env"
)

const (
	SeenDir        = "dns-seen"
	seenFlushEvery = 2 * time.Second
	seenMaxDomains = 2000 // domains kept per sandbox file (oldest-seen dropped)
)

// SeenEntry aggregates the lookups of one domain by one sandbox.
type SeenEntry struct {
	First   time.Time `json:"first"`
	Last    time.Time `json:"last"`
	Count   int       `json:"count"`
	Blocked int       `json:"blocked"` // lookups the fence refused
}

// SeenFile is the per-sandbox file format.
type SeenFile struct {
	Sandbox string                `json:"sandbox"`
	Domains map[string]*SeenEntry `json:"domains"`
}

// SeenPath locates a sandbox's tracking file.
func SeenPath(sandbox string) string {
	return filepath.Join(env.StateDir(), SeenDir, sandbox+".json")
}

// ReadSeen loads a sandbox's tracking file (empty when absent).
func ReadSeen(sandbox string) SeenFile {
	out := SeenFile{Sandbox: sandbox, Domains: map[string]*SeenEntry{}}
	b, err := os.ReadFile(SeenPath(sandbox))
	if err != nil {
		return out
	}
	var v SeenFile
	if json.Unmarshal(b, &v) == nil && v.Domains != nil {
		out.Domains = v.Domains
	}
	return out
}

// ClearSeen drops a sandbox's tracking history.
func ClearSeen(sandbox string) error {
	err := os.Remove(SeenPath(sandbox))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// seenLog buffers lookups between flushes.
type seenLog struct {
	mu      sync.Mutex
	pending map[string]map[string]*SeenEntry // sandbox -> domain -> delta
}

func newSeenLog() *seenLog {
	return &seenLog{pending: map[string]map[string]*SeenEntry{}}
}

// record counts one lookup (cheap; the flush loop does the file work).
func (l *seenLog) record(sandbox, domain string, blocked bool) {
	if sandbox == "" || !trackableDomain(domain) {
		return
	}
	now := time.Now().UTC()
	l.mu.Lock()
	defer l.mu.Unlock()
	m := l.pending[sandbox]
	if m == nil {
		m = map[string]*SeenEntry{}
		l.pending[sandbox] = m
	}
	e := m[domain]
	if e == nil {
		e = &SeenEntry{First: now}
		m[domain] = e
	}
	e.Last = now
	e.Count++
	if blocked {
		e.Blocked++
	}
}

// loop flushes pending lookups forever.
func (l *seenLog) loop() {
	for {
		time.Sleep(seenFlushEvery)
		l.flush()
	}
}

func (l *seenLog) flush() {
	l.mu.Lock()
	batch := l.pending
	l.pending = map[string]map[string]*SeenEntry{}
	l.mu.Unlock()
	for sb, deltas := range batch {
		_ = mergeSeen(sb, deltas)
	}
}

// mergeSeen folds deltas into the sandbox file (atomic replace).
func mergeSeen(sandbox string, deltas map[string]*SeenEntry) error {
	cur := ReadSeen(sandbox)
	for d, delta := range deltas {
		e := cur.Domains[d]
		if e == nil {
			cp := *delta
			cur.Domains[d] = &cp
			continue
		}
		if e.First.IsZero() || delta.First.Before(e.First) {
			e.First = delta.First
		}
		if delta.Last.After(e.Last) {
			e.Last = delta.Last
		}
		e.Count += delta.Count
		e.Blocked += delta.Blocked
	}
	if len(cur.Domains) > seenMaxDomains {
		names := make([]string, 0, len(cur.Domains))
		for d := range cur.Domains {
			names = append(names, d)
		}
		sort.Slice(names, func(i, j int) bool {
			return cur.Domains[names[i]].Last.After(cur.Domains[names[j]].Last)
		})
		for _, d := range names[seenMaxDomains:] {
			delete(cur.Domains, d)
		}
	}
	b, err := json.Marshal(cur)
	if err != nil {
		return err
	}
	path := SeenPath(sandbox)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// trackableDomain keeps hostile qnames (raw label bytes may be anything)
// out of the record: hostname charset only, sane length.
func trackableDomain(d string) bool {
	if d == "" || len(d) > 253 {
		return false
	}
	for i := 0; i < len(d); i++ {
		c := d[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '.', c == '_':
		default:
			return false
		}
	}
	return true
}
