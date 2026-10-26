// Package ledger records every proxied LLM call as a journal line and
// renders summaries (today/total spend, per-model split, recent calls).
// Tokens are counted from upstream usage whenever the provider reports it
// (OpenAI final chunk / non-stream usage, Anthropic message_start/_delta),
// otherwise a chars/4 estimate is recorded with est=true.
package ledger

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"hotbed/internal/env"
)

// Entry is one journal line.
type Entry struct {
	TS       string  `json:"ts"`
	Sandbox  string  `json:"sandbox"`
	Provider string  `json:"provider"`
	Model    string  `json:"model"`
	In       int     `json:"in"`
	Out      int     `json:"out"`
	Est      bool    `json:"est,omitempty"`
	Cost     float64 `json:"cost"`
	DurMS    int64   `json:"dur_ms"`
	Status   int     `json:"status"`
	Path     string  `json:"path,omitempty"`
}

// Call bundles what the proxy hands over after one request.
type Call struct {
	Sandbox, Provider, Model string
	In, Out                  int
	Est                      bool
	DurMS                    int64
	Status                   int
	Path                     string
}

// Ledger persists to ~/.local/state/hotbed/usage.jsonl.
type Ledger struct {
	mu     sync.Mutex
	path   string
	prices map[string][2]float64 // config override
}

// Path returns the journal location.
func Path() string { return filepath.Join(env.StateDir(), "usage.jsonl") }

// Open builds a ledger with the given pricing map (nil = builtin only).
func Open(prices map[string][2]float64) *Ledger {
	return &Ledger{path: Path(), prices: prices}
}

// Record computes cost, appends the line, and returns the priced entry.
func (l *Ledger) Record(c Call) (Entry, error) {
	e := Entry{
		TS:       time.Now().UTC().Format(time.RFC3339),
		Sandbox:  c.Sandbox,
		Provider: c.Provider,
		Model:    c.Model,
		In:       c.In,
		Out:      c.Out,
		Est:      c.Est,
		Cost:     l.costOf(c.Model, c.In, c.Out),
		DurMS:    c.DurMS,
		Status:   c.Status,
		Path:     c.Path,
	}
	b, _ := json.Marshal(e)
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		return e, err
	}
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return e, err
	}
	defer f.Close()
	_, err = f.Write(append(b, '\n'))
	return e, err
}

// price returns $/Mtok (in, out) for a model via longest-prefix match;
// config overrides win over builtin table; unknown = (0,0) → cost 0.
var builtinPrices = map[string][2]float64{
	"gpt-4o-mini":      {0.15, 0.6},
	"gpt-4o":           {2.5, 10},
	"gpt-4.1":          {2, 8},
	"o3":               {2, 8},
	"o4-mini":          {1.1, 4.4},
	"claude-opus":      {15, 75},
	"claude-sonnet":    {3, 15},
	"claude-haiku":     {0.8, 4},
	"gemini-2.5-pro":   {1.25, 10},
	"gemini-2.5-flash": {0.3, 2.5},
	"deepseek":         {0.27, 1.1},
	"glm":              {0.6, 2.2},
	"qwen":             {0.4, 1.2},
	"llama":            {0.3, 0.9},
	"mistral":          {0.5, 1.5},
	"kimi":             {0.6, 2.5},
	"gpt-3.5":          {0.5, 1.5},
}

// PricesAPI exposes rule lookup for tests/tools.
func (l *Ledger) Price(model string) (in, out float64, known bool) {
	in, out = 0, 0
	known = false
	best := 0
	up := strings.ToLower(model)
	check := func(key string, gp [2]float64) {
		if strings.HasPrefix(up, strings.ToLower(key)) && len(key) >= best {
			best = len(key)
			in, out = gp[0], gp[1]
			known = true
		}
	}
	for k, v := range l.prices {
		check(k, v)
	}
	for k, v := range builtinPrices {
		check(k, v)
	}
	return
}

// BuiltinPrices exposes the compiled-in rate table (for `hotbedd price ls`).
func BuiltinPrices() map[string][2]float64 {
	out := make(map[string][2]float64, len(builtinPrices))
	for k, v := range builtinPrices {
		out[k] = v
	}
	return out
}

// EffectivePrices merges config overrides over the builtin table
// (config wins). Returns a fresh map.
func (l *Ledger) EffectivePrices() map[string][2]float64 {
	out := BuiltinPrices()
	for k, v := range l.prices {
		out[k] = v
	}
	return out
}

func (l *Ledger) costOf(model string, in, out int) float64 {
	pin, pout, _ := l.Price(model)
	return float64(in)/1e6*pin + float64(out)/1e6*pout
}

// Token estimate for unreported usage: ~4 bytes per token.
func Estimate(text string) int {
	n := len(text) / 4
	if n < 1 {
		n = 1
	}
	return n
}

// --- summaries ---

// ModelRow aggregates one model inside a sandbox summary.
type ModelRow struct {
	Model string  `json:"model"`
	In    int     `json:"in"`
	Out   int     `json:"out"`
	Est   int     `json:"est_calls"`
	Cost  float64 `json:"cost"`
	Calls int     `json:"calls"`
}

// Summary aggregates the journal for one sandbox ("" = all sandboxes).
type Summary struct {
	Sandbox        string     `json:"sandbox"`
	Total          float64    `json:"total_usd"`
	Today          float64    `json:"today_usd"`
	TokensIn       int        `json:"tokens_in"`
	TokensOut      int        `json:"tokens_out"`
	TodayTokensIn  int        `json:"today_tokens_in"`
	TodayTokensOut int        `json:"today_tokens_out"`
	Calls          int        `json:"calls"`
	Models         []ModelRow `json:"models"`
	Calls25        []Entry    `json:"recent"`
}

const maxScan = 8000 // journal lines considered per summary

func (l *Ledger) scan() []Entry {
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := os.Open(l.path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		lines = append(lines, sc.Text())
		if len(lines) > maxScan {
			lines = lines[len(lines)-maxScan:]
		}
	}
	var out []Entry
	for _, ln := range lines {
		var e Entry
		if json.Unmarshal([]byte(ln), &e) == nil && e.Sandbox != "" {
			out = append(out, e)
		}
	}
	return out
}

// Summarize aggregates for one sandbox ("" = global).
func (l *Ledger) Summarize(sandbox string) Summary {
	s := Summary{Sandbox: sandbox}
	today := time.Now().UTC().Format("2006-01-02")
	mm := map[string]*ModelRow{}
	for _, e := range l.scan() {
		if sandbox != "" && e.Sandbox != sandbox {
			continue
		}
		s.Total += e.Cost
		s.Calls++
		s.TokensIn += e.In
		s.TokensOut += e.Out
		if strings.HasPrefix(e.TS, today) {
			s.Today += e.Cost
			s.TodayTokensIn += e.In
			s.TodayTokensOut += e.Out
		}
		m := mm[e.Model]
		if m == nil {
			m = &ModelRow{Model: e.Model}
			mm[e.Model] = m
		}
		m.In += e.In
		m.Out += e.Out
		m.Cost += e.Cost
		m.Calls++
		if e.Est {
			m.Est++
		}
	}
	for _, m := range mm {
		s.Models = append(s.Models, *m)
	}
	sort.Slice(s.Models, func(i, j int) bool { return s.Models[i].Cost > s.Models[j].Cost })
	all := l.scan()
	for i := len(all) - 1; i >= 0 && len(s.Calls25) < 25; i-- {
		if sandbox == "" || all[i].Sandbox == sandbox {
			s.Calls25 = append(s.Calls25, all[i])
		}
	}
	return s
}

// PerSandbox returns today/total per sandbox for the burn overview table.
type Row struct {
	Sandbox string  `json:"sandbox"`
	Today   float64 `json:"today_usd"`
	Total   float64 `json:"total_usd"`
	Tokens  int     `json:"tokens_today"`
	Calls   int     `json:"calls"`
}

// Overview lists per-sandbox aggregates.
func (l *Ledger) Overview() []Row {
	today := time.Now().UTC().Format("2006-01-02")
	mm := map[string]*Row{}
	for _, e := range l.scan() {
		r := mm[e.Sandbox]
		if r == nil {
			r = &Row{Sandbox: e.Sandbox}
			mm[e.Sandbox] = r
		}
		r.Total += e.Cost
		r.Calls++
		if strings.HasPrefix(e.TS, today) {
			r.Today += e.Cost
			r.Tokens += e.In + e.Out
		}
	}
	var out []Row
	for _, r := range mm {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Today > out[j].Today })
	return out
}

// Purge drops every journal row for the given sandbox names and rewrites
// the file atomically (same perms). Rows are burn history the user can be
// deleting deliberately — callers restrict this to stale/unpinned names.
// Returns how many rows were removed (0 → file left untouched).
func (l *Ledger) Purge(names map[string]bool) (int, error) {
	if len(names) == 0 {
		return 0, nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := os.Open(l.path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	type probe struct {
		Sandbox string `json:"sandbox"`
	}
	var kept []string
	dropped := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		ln := sc.Text()
		var p probe
		if json.Unmarshal([]byte(ln), &p) == nil && names[p.Sandbox] {
			dropped++
			continue
		}
		kept = append(kept, ln)
	}
	f.Close()
	if dropped == 0 {
		return 0, nil
	}
	tmp := l.path + ".tmp"
	w, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	for _, ln := range kept {
		if _, err := w.WriteString(ln + "\n"); err != nil {
			w.Close()
			return dropped, err
		}
	}
	if err := w.Close(); err != nil {
		return 0, err
	}
	if err := os.Rename(tmp, l.path); err != nil {
		return dropped, err
	}
	return dropped, nil
}
