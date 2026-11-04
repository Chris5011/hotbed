package api

// Strict egress (issue #4): a sandbox entering or leaving strict changes
// how it reaches the LLM proxy (containers: the gateway alias instead of
// a bridge route; libvirt guests: a new lease, so the ssh -R tunnel must
// be rebuilt). The watcher notices either way — dashboard, REST or a CLI
// `hotbedd egress NAME strict` in another process — and re-syncs OpenCode
// and every agent onto the new route once the switch has settled.

import (
	"fmt"
	"net/url"
	"time"

	"hotbed/internal/attach"
	"hotbed/internal/audit"
	"hotbed/internal/cfgsync"
	"hotbed/internal/engine"
	"hotbed/internal/env"
	"hotbed/internal/sandbox"
)

// StartStrictWatch runs the re-sync watcher (background, forever).
func (s *Server) StartStrictWatch() {
	if s.Collector == nil {
		return
	}
	go func() {
		prev := map[string]string{} // sandbox -> mode last acted on
		for {
			time.Sleep(10 * time.Second)
			list, ok := s.Collector.Cached()
			if !ok {
				continue
			}
			s.reloadCfg()
			for _, sb := range list {
				if sb.State != "running" {
					continue
				}
				mode := s.Cfg.EgressMode[sb.Name]
				p, seen := prev[sb.Name]
				stale := s.strictRouteStale(sb, mode)
				// only entering/leaving strict changes the route (a preset
				// change elsewhere, or a deleted sandbox's records, does not)
				if seen && (p == "strict") == (mode == "strict") && !stale {
					continue
				}
				if !seen && !stale && mode != "strict" {
					prev[sb.Name] = mode // first sight, nothing to fix
					continue
				}
				// the config is recorded before the switch runs (a VM's NIC
				// re-point takes seconds): wait until the sandbox really is
				// where its mode says, else the sync rides the old route
				if !engine.StrictSettled(sb.Name, mode == "strict") {
					continue
				}
				prev[sb.Name] = mode
				if !seen && !stale {
					continue // boot: strict and already synced for it
				}
				s.resyncRoute(sb, mode)
			}
		}
	}()
}

// strictRouteStale: a container's synced proxy URL does not match its
// isolation (gateway alias iff strict).
func (s *Server) strictRouteStale(sb sandbox.Sandbox, mode string) bool {
	if sb.Kind != sandbox.KindContainer {
		return false
	}
	tg, ok := cfgsync.Lookup(sb.Name)
	if !ok || tg.BaseURL == "" {
		return false
	}
	u, err := url.Parse(tg.BaseURL)
	if err != nil {
		return false
	}
	viaGW := u.Hostname() == engine.StrictAlias
	return viaGW != (mode == "strict" && engine.IsStrictContainer(sb.Name))
}

func (s *Server) resyncRoute(sb sandbox.Sandbox, mode string) {
	if len(s.Cfg.Opencode.Providers) == 0 {
		return
	}
	if sb.Kind == sandbox.KindVM {
		attach.KillTunnel(sb.Name) // bound to the old guest address
		if sb.Backend != sandbox.BackendDocker {
			// the inventory snapshot may still carry the old lease
			if ip := engine.LibvirtLease(env.VMStdPrefix + sb.Name); ip != "" {
				sb.SSH = "SSH_CMD=ssh -i ~/.ssh/hotbedvm_key dev@" + ip
			}
		}
	}
	detail := "proxy route re-synced for egress " + mode
	if _, _, err := s.syncSandbox(sb, ""); err != nil {
		detail += "; opencode sync failed: " + err.Error()
	}
	if _, err := s.syncAgents(sb); err != nil {
		detail += "; agent sync failed: " + err.Error()
	}
	audit.Record("strict.resync", sb.Name, detail)
	fmt.Printf("\033[36;1mhotbedd:\033[0m %s: %s\n", sb.Name, detail)
}
