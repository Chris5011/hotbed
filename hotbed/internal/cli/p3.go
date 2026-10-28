package cli

// P3 verbs start here: pricing (`hotbedd price …`). Later P3 surface grows
// in this file as it lands (mounts, recipes, egress, snapshots, audit).
//
// Pricing: `prices` in config.json maps "model-prefix" → [in, out] USD per
// million tokens; the ledger resolves by longest prefix, config entries
// beat the compiled-in table, unknown models cost 0 (est fallback rows).

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"hotbed/internal/audit"
	"hotbed/internal/config"
	"hotbed/internal/dnsfence"
	"hotbed/internal/egressctl"
	"hotbed/internal/engine"
)

func priceCmd(args []string) error {
	verb := firstArg(args)
	rest := args
	if verb != "" && !strings.HasPrefix(verb, "-") {
		rest = args[1:]
	}
	switch verb {
	case "ls", "list", "":
		return priceList()
	case "set":
		return priceSet(rest)
	case "rm", "remove", "unset":
		return priceRm(rest)
	case "show":
		return priceShow(rest)
	default:
		return fmt.Errorf("unknown price verb %q (ls|set|rm|show)", verb)
	}
}

// priceList prints the effective table (compiled-in + config overrides).
func priceList() error {
	cfg, _, led, err := p2Stores()
	if err != nil {
		return err
	}
	eff := led.EffectivePrices()
	keys := make([]string, 0, len(eff))
	for k := range eff {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Printf("%-24s %11s %11s  %s\n", "MODEL (prefix)", "IN $/Mtok", "OUT $/Mtok", "SOURCE")
	for _, k := range keys {
		v := eff[k]
		src := "builtin"
		if _, ok := cfg.Prices[k]; ok {
			src = "config"
		}
		fmt.Printf("%-24s %11.6g %11.6g  %s\n", k, v[0], v[1], src)
	}
	if len(keys) == 0 {
		fmt.Println("(no pricing rules; unreported models are costed 0 until measured)")
	}
	return nil
}

// priceSet stores one override in config.json.
func priceSet(args []string) error {
	if len(args) < 3 {
		return fmt.Errorf("usage: hotbedd price set MODEL IN OUT   (USD per million tokens; MODEL matches by prefix)")
	}
	m := args[0]
	inF, err := strconv.ParseFloat(args[1], 64)
	if err != nil || inF < 0 {
		return fmt.Errorf("IN price must be a non-negative number")
	}
	outF, err := strconv.ParseFloat(args[2], 64)
	if err != nil || outF < 0 {
		return fmt.Errorf("OUT price must be a non-negative number")
	}
	cfg, _, led, err := p2Stores()
	if err != nil {
		return err
	}
	if cfg.Prices == nil {
		cfg.Prices = map[string][2]float64{}
	}
	cfg.Prices[m] = [2]float64{inF, outF}
	if err := cfg.Save(); err != nil {
		return err
	}
	_, _, known := led.Price(m)
	src := "new rule active"
	if known {
		src = "overrides the builtin table"
	}
	ok("price %s: in $%.6g, out $%.6g per Mtok (%s) — new ledger rows use it immediately; recorded rows keep their cost\n", m, inF, outF, src)
	return nil
}

// priceRm drops a config override (records fall back to builtin pricing).
func priceRm(args []string) error {
	if firstArg(args) == "" {
		return fmt.Errorf("usage: hotbedd price rm MODEL")
	}
	m := firstArg(args)
	cfg, _, _, err := p2Stores()
	if err != nil {
		return err
	}
	if _, ok := cfg.Prices[m]; !ok {
		return fmt.Errorf("no config override for %q (builtin prices are compiled in)", m)
	}
	delete(cfg.Prices, m)
	if err := cfg.Save(); err != nil {
		return err
	}
	ok("price override for %s removed — ledger falls back to builtin pricing\n", m)
	return nil
}

// priceShow resolves the effective rate for one model string.
func priceShow(args []string) error {
	if firstArg(args) == "" {
		return fmt.Errorf("usage: hotbedd price show MODEL")
	}
	m := firstArg(args)
	_, _, led, err := p2Stores()
	if err != nil {
		return err
	}
	inF, outF, known := led.Price(m)
	if !known {
		fmt.Printf("%s: no pricing rule (costed $0 until usage is reported)\n", m)
		return nil
	}
	fmt.Printf("%s: in $%.6g, out $%.6g per Mtok\n", m, inF, outF)
	return nil
}

// ----------------------------------------------------------------- mount ---
// Easy host-dir mounts for containers. Creation time: `hbd --mount HOST:CT`.
// Live changes: `hotbedd mount add|rm` recreates the container (docker cannot
// attach binds to a running container) after a --yes/interactive confirm.

func mountCmd(args []string) error {
	verb := firstArg(args)
	rest := args
	if verb != "" && !strings.HasPrefix(verb, "-") {
		rest = args[1:]
	}
	switch verb {
	case "ls", "list":
		return mountList(rest)
	case "add":
		return mountAdd(rest)
	case "rm", "remove":
		return mountRm(rest)
	default:
		return fmt.Errorf("usage: hotbedd mount ls NAME | add NAME HOST:CT[:ro]... [--yes] | rm NAME CT-PATH [--yes]")
	}
}

// confirm asks on a TTY; non-TTY requires the flag (keeps the smoke suite hermetic).
func confirm(q string, yes bool) error {
	if yes {
		return nil
	}
	fi, e := os.Stdin.Stat()
	if e == nil && (fi.Mode()&os.ModeCharDevice) == 0 {
		return fmt.Errorf("refusing non-interactive recreate; re-run with --yes")
	}
	fmt.Fprintf(os.Stderr, "%s [y/N] ", q)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return nil
	}
	return fmt.Errorf("aborted")
}

func mountList(args []string) error {
	name := firstArg(args)
	if name == "" {
		return fmt.Errorf("usage: hotbedd mount ls NAME")
	}
	binds, vols, err := engine.ContainerMounts(name)
	if err != nil {
		return err
	}
	if len(binds) > 0 {
		fmt.Printf("binds (host -> container, mode):\n")
		for _, b := range binds {
			fmt.Printf("  %-46s -> %-28s %s\n", b.Source, b.Target, b.Mode)
		}
	} else {
		fmt.Println("binds: none")
	}
	if len(vols) > 0 {
		fmt.Printf("named volumes (docker-managed, carried over on recreate):\n")
		for _, v := range vols {
			fmt.Printf("  %-28s -> %-28s %s\n", v.Source, v.Target, v.Mode)
		}
	}
	return nil
}

// applyMounts updates binds after the confirm gate.
func applyMounts(name string, yes bool, transform func([]engine.Bind) []engine.Bind) error {
	binds, _, err := engine.ContainerMounts(name)
	if err != nil {
		return err
	}
	_ = binds // transform computes the delta; confirm covers the recreation
	if err := confirm(fmt.Sprintf("RECREATE %s (stop → remove → create → start) to change mounts?", name), yes); err != nil {
		return err
	}
	next, err := engine.RebuildWithBinds(name, transform)
	if err != nil {
		return err
	}
	ok("mounts updated; %s now binds (%d):\n", name, len(next))
	for _, b := range next {
		mode := b.Mode
		if mode == "" {
			mode = "rw"
		}
		fmt.Printf("  %-46s -> %-28s %s\n", b.Source, b.Target, mode)
	}
	return nil
}

func mountAdd(args []string) error {
	name := firstArg(args)
	if name == "" {
		return fmt.Errorf("usage: hotbedd mount add NAME HOST:CT[:ro]... [--yes]")
	}
	yes := hasFlag(args, "--yes")
	var adding []engine.Bind
	seen := map[string]bool{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == name || strings.HasPrefix(a, "-") {
			continue
		}
		b, err := engine.ParseBind(a)
		if err != nil {
			return err
		}
		if seen[b.Target] {
			return fmt.Errorf("duplicate container target in arguments: %s", b.Target)
		}
		seen[b.Target] = true
		adding = append(adding, b)
	}
	if len(adding) == 0 {
		return fmt.Errorf("no mount spec given (HOST:CT[:ro])")
	}
	cur, vols, err := engine.ContainerMounts(name)
	if err != nil {
		return err
	}
	order := map[string]bool{}
	for _, b := range cur {
		order[b.Target] = true
	}
	for _, v := range vols {
		order[v.Target] = true
	}
	var fresh []engine.Bind
	for _, b := range adding {
		if order[b.Target] {
			fmt.Printf("skip %s: %s is already mounted\n", b.Source, b.Target)
			continue
		}
		fresh = append(fresh, b)
	}
	if len(fresh) == 0 {
		ok("nothing to add (every requested target already mounted)\n")
		return nil
	}
	return applyMounts(name, yes, func(cur []engine.Bind) []engine.Bind {
		return append(append([]engine.Bind(nil), cur...), fresh...)
	})
}

func mountRm(args []string) error {
	name := firstArg(args)
	rest := args
	if name != "" && !strings.HasPrefix(name, "-") {
		rest = args[1:]
	}
	target := firstArg(rest)
	if name == "" || target == "" {
		return fmt.Errorf("usage: hotbedd mount rm NAME CT-PATH [--yes]")
	}
	yes := hasFlag(args, "--yes")
	binds, vols, err := engine.ContainerMounts(name)
	if err != nil {
		return err
	}
	for _, v := range vols {
		if v.Target == target {
			return fmt.Errorf("%s is a named docker volume, not a bind — edit it by removing/re-adding the sandbox", target)
		}
	}
	found := false
	for _, b := range binds {
		if b.Target == target {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("no bind at %s in %s", target, name)
	}
	return applyMounts(name, yes, func(cur []engine.Bind) []engine.Bind {
		var out []engine.Bind
		for _, b := range cur {
			if b.Target != target {
				out = append(out, b)
			}
		}
		return out
	})
}

// ---------------------------------------------------------------- egress ---
// Outbound fences via iptables inside the sandbox netns. No confirm gate:
// presets are instantly reversible (`allow`) and lose no data.

const egressUsage = `usage:
  hotbedd egress NAME                                  show preset, tracking, allowlist, rules
  hotbedd egress NAME allow|proxy-only|none [--track|--no-track]
  hotbedd egress NAME allowlist [DOMAINS...] [--track|--no-track]
                                    (no domains = keep the configured list, may be empty)
  hotbedd egress NAME track on|off                     record DNS lookups (allow / allowlist)
  hotbedd egress NAME seen                             domains the sandbox looked up
  hotbedd egress NAME add|rm DOMAIN...                 edit the allowlist (live on a fenced sandbox)`

func egressCmd(args []string) error {
	name := firstArg(args)
	if name == "" || strings.HasPrefix(name, "-") {
		return fmt.Errorf("%s", egressUsage)
	}
	var rest []string
	trackFlag := 0 // 0 keep, 1 on, -1 off
	for _, a := range args[1:] {
		switch a {
		case "--track":
			trackFlag = 1
		case "--no-track":
			trackFlag = -1
		default:
			rest = append(rest, a)
		}
	}
	cfg, err := config.Open()
	if err != nil {
		return err
	}
	verb := firstArg(rest)
	switch verb {
	case "":
		return egressShow(cfg, name)
	case "seen":
		return egressSeen(cfg, name)
	case "track":
		v := firstArg(rest[1:])
		if v != "on" && v != "off" {
			return fmt.Errorf("usage: hotbedd egress NAME track on|off")
		}
		if err := egressctl.SetTrack(cfg, name, v == "on"); err != nil {
			return err
		}
		ok("egress %s: dns tracking %s\n", name, v)
		return nil
	case "add", "rm":
		if len(rest) < 2 {
			return fmt.Errorf("usage: hotbedd egress NAME %s DOMAIN...", verb)
		}
		for _, d := range rest[1:] {
			var e error
			if verb == "add" {
				e = egressctl.AllowDomain(cfg, name, d)
			} else {
				e = egressctl.DenyDomain(cfg, name, d)
			}
			if e != nil {
				return e
			}
		}
		ok("egress %s allowlist: %s\n", name, strings.Join(cfg.EgressAllowlistFor(name), ", "))
		return nil
	}
	preset := verb
	if !engine.KnownEgressPreset(preset) {
		return fmt.Errorf("unknown egress preset %q\n%s", preset, egressUsage)
	}
	track := cfg.EgressTrack[name]
	if trackFlag != 0 {
		track = trackFlag > 0
	}
	if preset == "allowlist" && len(rest) > 1 {
		cfg.SetEgressAllowlist(name, rest[1:])
	}
	n, err := egressctl.Apply(cfg, name, preset, track)
	if err != nil {
		return err
	}
	tr := ""
	if cfg.EgressTrack[name] {
		tr = "; dns tracking on"
	}
	switch preset {
	case "allowlist":
		list := cfg.EgressAllowlistFor(name)
		ok("egress %s → allowlist: %s (%d domain(s), %d ip(s) pre-resolved; DNS goes through the fence, everything else drops%s)\n",
			name, strings.Join(list, ", "), len(list), n, tr)
	case "allow":
		if track {
			ok("egress %s → allow + dns tracking (outbound open; lookups recorded via the fence)\n", name)
		} else {
			ok("egress %s → allow (rules removed, docker default; resolver reset to docker internal)\n", name)
		}
	default:
		ok("egress %s → %s (loopback+established kept; re-applied by the daemon after restarts)\n", name, preset)
	}
	return nil
}

func egressShow(cfg *config.Config, name string) error {
	mode := cfg.EgressMode[name]
	if mode == "" {
		mode = "(not recorded - applied before v0.7 or never)"
	}
	fmt.Printf("%s: preset %s · dns tracking %v\n", name, mode, cfg.EgressTrack[name])
	if list := cfg.EgressAllowlistFor(name); len(list) > 0 {
		fmt.Printf("%s: allowlist: %s\n", name, strings.Join(list, ", "))
	}
	s, err := engine.ShowEgressContainer(name)
	if err != nil {
		return err
	}
	if s == "" {
		fmt.Printf("%s: no egress rules in the netns (allow all)\n", name)
		return nil
	}
	fmt.Printf("%s:\n  %s\n", name, strings.ReplaceAll(s, "\n", "\n  "))
	return nil
}

func egressSeen(cfg *config.Config, name string) error {
	seen := dnsfence.ReadSeen(name)
	if len(seen.Domains) == 0 {
		if !cfg.EgressTrack[name] {
			fmt.Printf("%s: dns tracking is off - `hotbedd egress %s track on`\n", name, name)
		} else {
			fmt.Printf("%s: no lookups recorded yet\n", name)
		}
		return nil
	}
	names := make([]string, 0, len(seen.Domains))
	for d := range seen.Domains {
		names = append(names, d)
	}
	sort.Slice(names, func(i, j int) bool {
		return seen.Domains[names[i]].Last.After(seen.Domains[names[j]].Last)
	})
	fmt.Printf("%-48s %-9s %7s %7s  %s\n", "DOMAIN", "STATUS", "LOOKUPS", "BLOCKED", "LAST (UTC)")
	for _, d := range names {
		e := seen.Domains[d]
		st := "blocked"
		if cfg.EgressMode[name] == "allow" {
			st = "open"
		}
		if by := cfg.AllowlistCovers(name, d); by != "" {
			st = "allowed"
		}
		fmt.Printf("%-48s %-9s %7d %7d  %s\n", d, st, e.Count, e.Blocked, e.Last.Format("2006-01-02 15:04:05"))
	}
	return nil
}

// ----------------------------------------------------------------- audit ---
// `hotbedd audit [N]` — last N control-plane actions (default 50).

func auditCmd(args []string) error {
	n := 50
	if v := firstArg(args); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p > 0 {
			n = p
		}
	}
	entries := audit.Tail(n)
	if len(entries) == 0 {
		fmt.Println("no audit entries yet (actions land in state dir/audit.jsonl)")
		return nil
	}
	fmt.Printf("%-25s %-16s %-24s %s\n", "TIMESTAMP (UTC)", "ACTION", "SANDBOX", "DETAIL")
	for _, e := range entries {
		sb := e.Sandbox
		if sb == "" {
			sb = "—"
		}
		fmt.Printf("%-25s %-16s %-24s %s\n", e.TS, e.Kind, sb, e.Detail)
	}
	return nil
}
