package api

// The per-sandbox DNS tab: which domains a container looked up (recorded
// by the DNS fence while tracking is on) and the allowlist toggles that
// open forgotten domains after the fact.
//
//	GET  /api/sandboxes/container/<name>/dns
//	POST /api/sandboxes/container/<name>/dns
//	     {"action":"allow"|"deny","domain":"api.example.com"}
//	     {"action":"track","on":true}
//	     {"action":"preset","preset":"allowlist"}   (allow ⇄ allowlist)
//	     {"action":"clear"}                          (drop the lookup record)

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"

	"hotbed/internal/dnsfence"
	"hotbed/internal/egressctl"
	"hotbed/internal/sandbox"
)

type dnsRow struct {
	Domain    string    `json:"domain"`
	First     time.Time `json:"first,omitzero"`
	Last      time.Time `json:"last,omitzero"`
	Count     int       `json:"count"`
	Blocked   int       `json:"blocked"`
	Allowed   bool      `json:"allowed"`              // on the list (exact or via a parent)
	CoveredBy string    `json:"covered_by,omitempty"` // the list entry admitting it
	Listed    bool      `json:"listed"`               // exact list entry (toggle can remove it)
}

func (s *Server) dnsRoute(w http.ResponseWriter, r *http.Request, sb sandbox.Sandbox) {
	if sb.Kind != sandbox.KindContainer {
		httpError(w, 400, "DNS tracking is available for containers only")
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.dnsState(w, sb.Name)
	case http.MethodPost:
		s.dnsAction(w, r, sb.Name)
	default:
		httpError(w, 405, "GET or POST")
	}
}

func (s *Server) dnsState(w http.ResponseWriter, name string) {
	s.reloadCfg()
	mode := s.Cfg.EgressMode[name]
	seen := dnsfence.ReadSeen(name)
	listed := map[string]bool{}
	for _, d := range s.Cfg.EgressAllowlistFor(name) {
		listed[d] = true
	}
	rows := make([]dnsRow, 0, len(seen.Domains)+len(listed))
	for d, e := range seen.Domains {
		by := s.Cfg.AllowlistCovers(name, d)
		rows = append(rows, dnsRow{
			Domain: d, First: e.First, Last: e.Last, Count: e.Count, Blocked: e.Blocked,
			Allowed: by != "", CoveredBy: by, Listed: listed[d],
		})
	}
	// list entries never looked up still get a row: they must stay
	// switchable off from the same table
	for d := range listed {
		if _, ok := seen.Domains[d]; !ok {
			rows = append(rows, dnsRow{Domain: d, Allowed: true, CoveredBy: d, Listed: true})
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if !rows[i].Last.Equal(rows[j].Last) {
			return rows[i].Last.After(rows[j].Last)
		}
		return rows[i].Domain < rows[j].Domain
	})
	okJSON(w, map[string]any{
		"name":      name,
		"mode":      mode, // "" = never applied through hotbedd (docker default)
		"track":     s.Cfg.EgressTrack[name],
		"allowlist": s.Cfg.EgressAllowlistFor(name),
		"domains":   rows,
	})
}

func (s *Server) dnsAction(w http.ResponseWriter, r *http.Request, name string) {
	var in struct {
		Action string `json:"action"`
		Domain string `json:"domain"`
		On     bool   `json:"on"`
		Preset string `json:"preset"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in); err != nil {
		httpError(w, 400, "invalid JSON: "+err.Error())
		return
	}
	s.reloadCfg()
	var err error
	switch in.Action {
	case "allow":
		err = egressctl.AllowDomain(s.Cfg, name, in.Domain)
	case "deny":
		err = egressctl.DenyDomain(s.Cfg, name, in.Domain)
	case "track":
		err = egressctl.SetTrack(s.Cfg, name, in.On)
	case "clear":
		err = dnsfence.ClearSeen(name)
	case "preset":
		if in.Preset != "allow" && in.Preset != "allowlist" {
			httpError(w, 400, "preset: allow or allowlist")
			return
		}
		_, err = egressctl.Apply(s.Cfg, name, in.Preset, s.Cfg.EgressTrack[name])
	default:
		httpError(w, 400, fmt.Sprintf("unknown action %q (allow|deny|track|clear|preset)", in.Action))
		return
	}
	if err != nil {
		httpError(w, 500, err.Error())
		return
	}
	s.dnsState(w, name)
}
