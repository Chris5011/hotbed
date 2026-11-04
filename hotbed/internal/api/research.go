package api

// Research views and exports:
//
//	GET  /api/dns                      every sandbox's DNS record consolidated
//	                                   (stale = sandbox no longer exists)
//	POST /api/dns/purge                {"sandboxes":[...]} | {"stale_only":true}
//	GET  /api/dns.csv[?sandbox=NAME]   sandbox,stale,domain,lookups,blocked,first,last
//	GET  /api/usage.csv[?sandbox=NAME][&group=day]
//	                                   every proxied call, or per day per sandbox
//
// DNS records outlive their sandbox (like the burn ledger) so research
// data survives a cleanup; the purge removes them deliberately. Exports
// read the complete history (the ledger export streams the whole journal,
// not the dashboard's summary window).

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"hotbed/internal/dnsfence"
	"hotbed/internal/ledger"
)

// liveNames is the set of sandboxes that exist right now.
func (s *Server) liveNames() map[string]bool {
	live := map[string]bool{}
	if s.Collector != nil {
		for _, sb := range s.Collector.Snapshot() {
			live[sb.Name] = true
		}
	}
	return live
}

type dnsDomainRow struct {
	Domain    string    `json:"domain"`
	Lookups   int       `json:"lookups"`
	Blocked   int       `json:"blocked"`
	Sandboxes []string  `json:"sandboxes"`
	First     time.Time `json:"first"`
	Last      time.Time `json:"last"`
}

type dnsSandboxRow struct {
	Sandbox string    `json:"sandbox"`
	Stale   bool      `json:"stale"`
	Domains int       `json:"domains"`
	Lookups int       `json:"lookups"`
	Blocked int       `json:"blocked"`
	Last    time.Time `json:"last"`
}

// dnsAll serves the consolidated record (?stale=0 hides deleted sandboxes).
func (s *Server) dnsAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpError(w, 405, "GET only")
		return
	}
	withStale := r.URL.Query().Get("stale") != "0"
	live := s.liveNames()
	doms := map[string]*dnsDomainRow{}
	var sbs []dnsSandboxRow
	for _, f := range dnsfence.ListSeen() {
		stale := !live[f.Sandbox]
		if stale && !withStale {
			continue
		}
		row := dnsSandboxRow{Sandbox: f.Sandbox, Stale: stale, Domains: len(f.Domains)}
		for d, e := range f.Domains {
			row.Lookups += e.Count
			row.Blocked += e.Blocked
			if e.Last.After(row.Last) {
				row.Last = e.Last
			}
			dr := doms[d]
			if dr == nil {
				dr = &dnsDomainRow{Domain: d, First: e.First}
				doms[d] = dr
			}
			dr.Lookups += e.Count
			dr.Blocked += e.Blocked
			dr.Sandboxes = append(dr.Sandboxes, f.Sandbox)
			if !e.First.IsZero() && (dr.First.IsZero() || e.First.Before(dr.First)) {
				dr.First = e.First
			}
			if e.Last.After(dr.Last) {
				dr.Last = e.Last
			}
		}
		sbs = append(sbs, row)
	}
	list := make([]*dnsDomainRow, 0, len(doms))
	for _, d := range doms {
		sort.Strings(d.Sandboxes)
		list = append(list, d)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Lookups != list[j].Lookups {
			return list[i].Lookups > list[j].Lookups
		}
		return list[i].Domain < list[j].Domain
	})
	if sbs == nil {
		sbs = []dnsSandboxRow{}
	}
	okJSON(w, map[string]any{"domains": list, "sandboxes": sbs})
}

// dnsPurge drops DNS records: stale ones in bulk, or named ones (a live
// sandbox's record only with force — its DNS tab has "clear" for that).
func (s *Server) dnsPurge(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpError(w, 405, "POST only")
		return
	}
	var in struct {
		Sandboxes []string `json:"sandboxes"`
		StaleOnly bool     `json:"stale_only"`
		Force     bool     `json:"force"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&in); err != nil {
		httpError(w, 400, "invalid JSON: "+err.Error())
		return
	}
	if !in.StaleOnly && len(in.Sandboxes) == 0 {
		httpError(w, 400, "nothing to purge: {\"sandboxes\":[...]} or {\"stale_only\":true}")
		return
	}
	live := s.liveNames()
	target := []string{}
	if in.StaleOnly {
		for _, f := range dnsfence.ListSeen() {
			if !live[f.Sandbox] {
				target = append(target, f.Sandbox)
			}
		}
	} else {
		for _, n := range in.Sandboxes {
			n = strings.TrimSpace(n)
			if n == "" {
				continue
			}
			if live[n] && !in.Force {
				httpError(w, 400, "refusing to purge the record of existing sandbox "+n+" (use clear in its DNS tab)")
				return
			}
			target = append(target, n)
		}
	}
	for _, n := range target {
		if err := dnsfence.ClearSeen(n); err != nil {
			httpError(w, 500, "purge "+n+": "+err.Error())
			return
		}
	}
	okJSON(w, map[string]any{"ok": true, "purged": target})
}

// csvSafe defuses spreadsheet formula injection: domain, model and agent
// names come from inside sandboxes.
func csvSafe(v string) string {
	if v != "" && strings.ContainsRune("=+-@\t\r", rune(v[0])) {
		return "'" + v
	}
	return v
}

var csvNameRe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func csvStart(w http.ResponseWriter, kind, sandbox string) *csv.Writer {
	scope := "all"
	if sandbox != "" {
		scope = csvNameRe.ReplaceAllString(sandbox, "_")
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="hotbed-%s-%s-%s.csv"`,
		kind, scope, time.Now().UTC().Format("20060102-1504")))
	w.Header().Set("Cache-Control", "no-store")
	return csv.NewWriter(w)
}

func tsCSV(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// dnsCSV exports DNS records: one row per sandbox and domain.
func (s *Server) dnsCSV(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpError(w, 405, "GET only")
		return
	}
	only := strings.TrimSpace(r.URL.Query().Get("sandbox"))
	live := s.liveNames()
	cw := csvStart(w, "dns", only)
	_ = cw.Write([]string{"sandbox", "stale", "domain", "lookups", "blocked", "first_seen_utc", "last_seen_utc"})
	for _, f := range dnsfence.ListSeen() {
		if only != "" && f.Sandbox != only {
			continue
		}
		names := make([]string, 0, len(f.Domains))
		for d := range f.Domains {
			names = append(names, d)
		}
		sort.Strings(names)
		for _, d := range names {
			e := f.Domains[d]
			_ = cw.Write([]string{csvSafe(f.Sandbox), strconv.FormatBool(!live[f.Sandbox]), csvSafe(d),
				strconv.Itoa(e.Count), strconv.Itoa(e.Blocked), tsCSV(e.First), tsCSV(e.Last)})
		}
	}
	cw.Flush()
}

// usageCSV exports the burn ledger: every proxied call, or ?group=day for
// calls/tokens/cost per day and sandbox (the burn rate over time).
func (s *Server) usageCSV(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpError(w, 405, "GET only")
		return
	}
	only := strings.TrimSpace(r.URL.Query().Get("sandbox"))
	byDay := r.URL.Query().Get("group") == "day"
	live := s.liveNames()
	kind := "burn"
	if byDay {
		kind = "burn-daily"
	}
	cw := csvStart(w, kind, only)
	if !byDay {
		_ = cw.Write([]string{"ts_utc", "sandbox", "stale", "agent", "provider", "model",
			"tokens_in", "tokens_out", "tokens_estimated", "cost_usd", "duration_ms", "status"})
		_ = s.Led.Each(func(e ledger.Entry) bool {
			if only == "" || e.Sandbox == only {
				_ = cw.Write([]string{e.TS, csvSafe(e.Sandbox), strconv.FormatBool(!live[e.Sandbox]), csvSafe(e.Agent),
					csvSafe(e.Provider), csvSafe(e.Model), strconv.Itoa(e.In), strconv.Itoa(e.Out),
					strconv.FormatBool(e.Est), strconv.FormatFloat(e.Cost, 'f', 6, 64),
					strconv.FormatInt(e.DurMS, 10), strconv.Itoa(e.Status)})
			}
			return true
		})
		cw.Flush()
		return
	}
	type agg struct {
		calls, in, out, est int
		cost                float64
	}
	days := map[[2]string]*agg{} // {date, sandbox}
	_ = s.Led.Each(func(e ledger.Entry) bool {
		if only != "" && e.Sandbox != only {
			return true
		}
		day := e.TS
		if len(day) >= 10 {
			day = day[:10]
		}
		k := [2]string{day, e.Sandbox}
		a := days[k]
		if a == nil {
			a = &agg{}
			days[k] = a
		}
		a.calls++
		a.in += e.In
		a.out += e.Out
		a.cost += e.Cost
		if e.Est {
			a.est++
		}
		return true
	})
	keys := make([][2]string, 0, len(days))
	for k := range days {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i][0] != keys[j][0] {
			return keys[i][0] < keys[j][0]
		}
		return keys[i][1] < keys[j][1]
	})
	_ = cw.Write([]string{"date_utc", "sandbox", "stale", "calls", "tokens_in", "tokens_out",
		"calls_estimated", "cost_usd"})
	for _, k := range keys {
		a := days[k]
		_ = cw.Write([]string{k[0], csvSafe(k[1]), strconv.FormatBool(!live[k[1]]), strconv.Itoa(a.calls),
			strconv.Itoa(a.in), strconv.Itoa(a.out), strconv.Itoa(a.est), strconv.FormatFloat(a.cost, 'f', 6, 64)})
	}
	cw.Flush()
}
