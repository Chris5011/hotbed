package cli

// `hotbedd agents` — coding-agent modules (internal/agents).

import (
	"fmt"
	"strings"

	"hotbed/internal/agents"
	"hotbed/internal/attach"
	"hotbed/internal/audit"
	"hotbed/internal/sandbox"
)

const agentsUsage = `usage:
  hotbedd agents [ls]                    modules, enabled state, proxy wiring
  hotbedd agents enable|disable ID       offer / hide a module at sandbox creation
  hotbedd agents add SANDBOX ID          install a module into a running container or VM (+ proxy sync)`

func agentsCmd(args []string) error {
	verb := firstArg(args)
	switch verb {
	case "", "ls", "list":
		cfg, _, _, err := p2Stores()
		if err != nil {
			return err
		}
		mods, errs := agents.Load()
		fmt.Printf("%-13s %-14s %-8s %-9s %s\n", "ID", "NAME", "ENABLED", "PROXY", "COMMAND")
		for _, m := range mods {
			px := "own auth"
			if m.Proxy != nil {
				px = m.Proxy.Style
			} else if m.Builtin {
				px = "sync"
			}
			fmt.Printf("%-13s %-14s %-8v %-9s %s\n", m.ID, m.Name, agents.Enabled(cfg, m), px, m.Command)
		}
		for _, e := range errs {
			fmt.Printf("\033[33;1mskipped:\033[0m %v\n", e)
		}
		fmt.Printf("module dirs: %s\n", strings.Join(agents.Dirs(), " · "))
		return nil
	case "enable", "disable":
		id := firstArg(args[1:])
		if id == "" {
			return fmt.Errorf("%s", agentsUsage)
		}
		if _, err := agents.Get(id); err != nil {
			return err
		}
		cfg, _, _, err := p2Stores()
		if err != nil {
			return err
		}
		if err := cfg.SetAgentEnabled(id, verb == "enable"); err != nil {
			return err
		}
		ok("agent %s %sd\n", id, verb)
		return nil
	case "add":
		if len(args) < 3 {
			return fmt.Errorf("%s", agentsUsage)
		}
		return agentsAdd(args[1], args[2])
	}
	return fmt.Errorf("%s", agentsUsage)
}

func agentsAdd(name, id string) error {
	m, err := agents.Get(id)
	if err != nil {
		return err
	}
	sb, err := findSandbox(name)
	if err != nil {
		return err
	}
	if sb.State != "running" {
		return fmt.Errorf("%s is %s - start it first", name, sb.State)
	}
	port := ""
	if sb.Kind == sandbox.KindVM && len(sb.Ports) > 0 {
		port = strings.TrimPrefix(sb.Ports[0], "127.0.0.1:")
	}
	box, err := attach.BoxFor(sb, port)
	if err != nil {
		return err
	}
	fmt.Printf("installing %s into %s %s (needs outbound network - a fenced sandbox may refuse the downloads)…\n", m.Name, sb.Kind, name)
	if err := agents.Install(box, m); err != nil {
		return err
	}
	audit.Record("agent.install", name, m.ID)
	ok("%s installed in %s\n", m.Name, name)
	cfg, ks, _, err := p2Stores()
	if err != nil {
		return err
	}
	base := ""
	if m.Proxy != nil && len(cfg.Opencode.Providers) > 0 {
		if base, _, err = attach.ResolveProxyURL(sb, cfg.Proxy.Port); err != nil {
			return fmt.Errorf("proxy not reachable from %s: %w", name, err)
		}
	}
	res, err := agents.Sync(box, m, cfg, ks, base)
	if err != nil {
		return err
	}
	if res.Persisted != "" {
		fmt.Println("sessions: " + res.Persisted + " (kept in the workspace)")
	}
	if res.Skipped != "" {
		fmt.Println("proxy: " + res.Skipped)
	} else {
		ok("proxy wired: %s via %s (model %s)\n", m.ID, res.Provider, orDash(res.Model))
	}
	fmt.Printf("launch: the dashboard button, or  tmux attach -t %s  once started there\n", attach.AgentSessionName(name, id))
	return nil
}

// findSandbox resolves a name across all kinds (daemon inventory when it
// runs, else a one-shot collector scan).
func findSandbox(name string) (sandbox.Sandbox, error) {
	var list []sandbox.Sandbox
	var resp struct {
		Sandboxes []sandbox.Sandbox `json:"sandboxes"`
	}
	if daemonUp() && daemonJSON("/api/sandboxes", &resp) {
		list = resp.Sandboxes
	} else {
		list = (&sandbox.Collector{}).Scan()
	}
	for _, sb := range list {
		if sb.Name == name {
			return sb, nil
		}
	}
	return sandbox.Sandbox{}, fmt.Errorf("no sandbox named %s (hotbedd ps)", name)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
