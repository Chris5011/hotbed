package api

// Key self-heal (issue #5): sandboxes whose virtual key vanished from the
// key store (a lost concurrent write, a store reset) answered 401 forever.
// At daemon start every running sandbox's held keys are checked against
// the store; a sandbox holding any unknown/revoked key is re-synced
// (OpenCode + every agent), which mints and writes fresh keys.

import (
	"fmt"
	"regexp"
	"time"

	"hotbed/internal/attach"
	"hotbed/internal/audit"
	"hotbed/internal/cfgsync"
	"hotbed/internal/sandbox"
)

var tokenRe = regexp.MustCompile(`hbk-[a-z0-9-]{8,}`)

// HealKeys runs once after the first inventory scan (background).
func (s *Server) HealKeys() {
	if s.Collector == nil || s.Keys == nil {
		return
	}
	var list []sandbox.Sandbox
	for i := 0; i < 24; i++ { // wait for the first scan (~5 s cadence)
		if l, ok := s.Collector.Cached(); ok {
			list = l
			break
		}
		time.Sleep(5 * time.Second)
	}
	s.reloadCfg()
	if len(s.Cfg.Opencode.Providers) == 0 {
		return
	}
	for _, sb := range list {
		if sb.State != "running" {
			continue
		}
		bad := s.lostKeys(sb)
		if len(bad) == 0 {
			continue
		}
		detail := fmt.Sprintf("%d held key(s) unknown to the store (%s…) - re-synced", len(bad), bad[0])
		if _, _, err := s.syncSandbox(sb, ""); err != nil {
			detail += "; opencode sync failed: " + err.Error()
		}
		if _, err := s.syncAgents(sb); err != nil {
			detail += "; agent sync failed: " + err.Error()
		}
		audit.Record("key.heal", sb.Name, detail)
	}
}

// lostKeys returns the readable prefixes of keys a sandbox holds (OpenCode
// target + agent env files) that the store no longer resolves.
func (s *Server) lostKeys(sb sandbox.Sandbox) []string {
	held := map[string]bool{}
	if tg, ok := cfgsync.Lookup(sb.Name); ok && tg.Key != "" {
		held[tg.Key] = true
	}
	if box, err := attach.BoxFor(sb, sshPort(sb)); err == nil {
		out, _ := box.RunUser(`cat ~/.config/hotbed/agents/*.env 2>/dev/null; true`, 20*time.Second)
		for _, t := range tokenRe.FindAllString(out, -1) {
			held[t] = true
		}
	}
	var bad []string
	for t := range held {
		if _, ok := s.Keys.ByToken(t); !ok {
			bad = append(bad, tokenTag(t))
		}
	}
	return bad
}

func tokenTag(t string) string {
	for i := len(t) - 1; i > 0; i-- {
		if t[i] == '-' {
			return t[:i]
		}
	}
	return t
}
