// Package cli dispatches sbxd subcommands: server mode ("serve") and the
// docker-lexicon client verbs (ps, logs, attach, connect, start, stop…).
package cli

import (
	"fmt"
	"os"
	"strings"

	"sbxd/internal/env"
)

// Dispatch routes the command line.
func Dispatch(args []string) error {
	cmd := "help"
	if len(args) > 0 {
		cmd = args[0]
		args = args[1:]
	}
	switch cmd {
	case "serve":
		return serve(args)
	case "proxy":
		return proxyCmd(args)
	case "ps", "ls":
		return ps(args)
	case "stats":
		return statsCmd(args)
	case "events":
		return eventsCmd(args)
	case "logs":
		return logsCmd(args)
	case "attach":
		return attachCmd(args)
	case "connect":
		return connectCmd(args)
	case "start":
		return lifeCmd(args, "start")
	case "stop":
		return lifeCmd(args, "stop")
	case "rm", "remove":
		return rmCmd(args)
	case "config":
		return configCmd(args)
	case "usage":
		return usageCmd(args)
	case "key":
		return keyCmd(args)
	case "budget", "budgets":
		return budgetCmd(args)
	case "price":
		return priceCmd(args)
	case "mount":
		return mountCmd(args)
	case "recipe":
		return recipeCmd(args)
	case "egress":
		return egressCmd(args)
	case "audit":
		return auditCmd(args)
	case "sync":
		return syncCmd(args)
	case "doctor":
		return doctorCmd(args)
	case "version":
		fmt.Printf("sbxd %s (daemon: sbx/sbxvm engine layer)\n", env.Version)
	case "help", "-h", "--help":
		printHelp()
	default:
		printHelp()
		return fmt.Errorf("unknown command %q", cmd)
	}
	return nil
}

func printHelp() {
	fmt.Printf(`sbxd %s — SBX Desktop: daemon + dashboard + docker-style CLI for OpenCode-SBX

SERVER
  sbxd serve [--port N]     start the resident daemon (dashboard at http://127.0.0.1:N)

CLIENT (talks to the daemon when it runs; keeps working standalone)
  sbxd ps                   unified sandbox table (containers + both sbxvm backends)
  sbxd stats                same table with live cpu/memory
  sbxd events               live sandbox event feed
  sbxd logs NAME            stream container logs / VM serial (add -f to follow)
  sbxd attach NAME          attach a persistent tmux session (shell inside sandbox)
  sbxd attach NAME --oc     attach with the opencode TUI inside
  sbxd connect NAME         print every copy-connection variant
  sbxd start|stop NAME      engine start/stop (docker stop / kit sbxvm semantics)
  sbxd rm NAME [--yes]      fully delete a sandbox (container, or VM incl.
                            disk+snapshots with --yes); also revokes its proxy
                            keys, drops wallets and the stored sync target
  sbxd doctor               runtime sanity summary

CONFIG & BURN (P2)
  sbxd config init          import providers from host ~/.config/opencode/opencode.json
  sbxd config print         global OpenCode settings (masked unless --raw)
  sbxd config set PATH VAL  set e.g. opencode.default_model, proxy.port
  sbxd usage [NAME]         token/cost ledger (global overview or per-sandbox)
  sbxd key mint|ls|revoke   per-sandbox proxy keys (upstream keys stay in sbxd)
  sbxd budget [NAME] [USD] [--tokens N]
                            wallets (USD + tokens) in config.json; bare "budget" lists all
  sbxd price ls|set|rm|show $/Mtok pricing table; "set" stores it in config.json
  sbxd sync NAME|all        push proxy-backed opencode config into sandbox(es)
  sbxd mount ls|add|rm      list/change host bind mounts of a container; add/rm
                            recreates it (stop→create→start) after --yes or a confirm
  sbxd recipe print|up|validate
                            declarative sandboxes (sbx.yaml): print+validate plans,
                            up spawns the container (needs a built kit image) then
                            applies wallets, egress and opencode sync from the file
  sbxd egress NAME [allow|proxy-only|none]
                            outbound fence via iptables inside the sandbox (show: omit preset)
  sbxd audit [N]            last N control-plane actions (start/stop, keys, wallets, mounts, egress)
  sbxd version|help
`, env.Version)
}

func done(err error) error {
	if err != nil {
		return fmt.Errorf("%s failed: %w", os.Args[1], err)
	}
	return nil
}

func firstArg(args []string) string {
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			return a
		}
	}
	return ""
}

func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}
