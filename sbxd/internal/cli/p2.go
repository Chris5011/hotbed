package cli

// P2 verbs: global OpenCode config, per-sandbox keys, budgets, usage
// ledger views, and config-sync into sandboxes.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"sbxd/internal/attach"
	"sbxd/internal/cfgsync"
	"sbxd/internal/config"
	"sbxd/internal/engine"
	"sbxd/internal/keys"
	"sbxd/internal/ledger"
	"sbxd/internal/sandbox"
)

// p2Stores returns local (file-backed) stores; these verbs work with or
// without a running daemon because the stores are stateless file views.
// Bootstraps the one-time budgets migration (keys.json → config.json).
func p2Stores() (*config.Config, *keys.Store, *ledger.Ledger, error) {
	if n, err := config.MigrateFromKeys(); err != nil {
		return nil, nil, nil, err
	} else if n > 0 {
		ok("migrated %d budget wallet(s) from keys.json into config.json\n", n)
	}
	cfg, err := config.Open()
	if err != nil {
		return nil, nil, nil, err
	}
	ks, err := keys.Open()
	if err != nil {
		return nil, nil, nil, err
	}
	return cfg, ks, ledger.Open(cfg.Prices), nil
}

// ---------------------------------------------------------------- config ---

func configCmd(args []string) error {
	verb := firstArg(args)
	rest := args
	if verb != "" && !strings.HasPrefix(verb, "-") {
		rest = args[1:]
	}
	switch verb {
	case "init":
		cfg, _, _, err := p2Stores()
		if err != nil {
			return err
		}
		n, err := cfg.ImportHostOpenCode()
		if err != nil {
			return err
		}
		if cfg.Opencode.DefaultProvider == "" && len(cfg.Opencode.Providers) > 0 {
			cfg.Opencode.DefaultProvider = cfg.DefaultProvider()
		}
		if err := cfg.Save(); err != nil {
			return err
		}
		ok("imported %d provider(s) from %s → %s\n", n, config.HostOpenCodePath(), config.Path())
		if n == 0 {
			fmt.Println("  add providers (base_url + api_key) to sbxd config, then `sbxd sync NAME`")
		}
		return nil
	case "print", "show", "":
		raw := hasFlag(rest, "--raw")
		if !raw && daemonUp() {
			var out map[string]any
			if daemonJSON("/api/config", &out) {
				return printJSON(out)
			}
		}
		b, err := os.ReadFile(config.Path())
		if err != nil {
			return fmt.Errorf("config missing — run `sbxd config init`")
		}
		fmt.Println(string(b))
		return nil
	case "get":
		cfg, _, _, err := p2Stores()
		if err != nil {
			return err
		}
		path := firstArg(rest)
		if path == "" {
			return fmt.Errorf("usage: sbxd config get <path>")
		}
		b, _ := json.Marshal(cfg)
		var m any
		_ = json.Unmarshal(b, &m)
		for _, seg := range strings.Split(strings.Trim(path, "."), ".") {
			mm, okk := m.(map[string]any)
			if !okk {
				m = nil
				break
			}
			m = mm[seg]
		}
		if m == nil {
			return fmt.Errorf("no value at config path %q", path)
		}
		return printJSON(m)
	case "set":
		cfg, _, _, err := p2Stores()
		if err != nil {
			return err
		}
		if len(rest) < 2 {
			return fmt.Errorf("usage: sbxd config set <path> <value>")
		}
		path, val := rest[0], strings.Join(rest[1:], " ")
		if err := configSetPath(cfg, path, val); err != nil {
			return err
		}
		if err := cfg.Save(); err != nil {
			return err
		}
		ok("config %s updated (path %q)\n", config.Path(), path)
		return nil
	case "path":
		fmt.Println(config.Path())
		return nil
	case "edit":
		ed := os.Getenv("EDITOR")
		if ed == "" {
			return fmt.Errorf("no $EDITOR set — edit %s directly", config.Path())
		}
		c := exec.Command(ed, config.Path())
		c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
		return c.Run()
	default:
		return fmt.Errorf("unknown config verb %q (init|print|get|set|path|edit)", verb)
	}
}

func printJSON(m any) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}

// configSetPath writes obj[segments...] = parsed value into the config
// through a JSON round-trip (maps by key, lists by numeric index).
func configSetPath(cfg *config.Config, path, val string) error {
	b, _ := json.Marshal(cfg)
	var m any
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	segs := strings.Split(strings.Trim(path, "."), ".")
	parent := m
	for i, seg := range segs[:len(segs)-1] {
		switch node := parent.(type) {
		case map[string]any:
			next, okk := node[seg]
			if !okk || next == nil {
				next = map[string]any{}
				node[seg] = next
			}
			parent = next
		case []any:
			idx, err := strconv.Atoi(seg)
			if err != nil || idx < 0 || idx >= len(node) {
				return fmt.Errorf("bad list index %q in %s", seg, strings.Join(segs[:i+1], "."))
			}
			parent = node[idx]
		default:
			return fmt.Errorf("cannot descend into %s", strings.Join(segs[:i+1], "."))
		}
	}
	last := segs[len(segs)-1]
	var parsed any
	if err := json.Unmarshal([]byte(val), &parsed); err != nil {
		parsed = val // keep plain strings convenient
	}
	switch node := parent.(type) {
	case map[string]any:
		node[last] = parsed
	case []any:
		idx, err := strconv.Atoi(last)
		if err != nil || idx < 0 || idx > len(node) {
			return fmt.Errorf("bad list index %q", last)
		}
		if idx == len(node) {
			node = append(node, parsed)
			return remarshal(cfg, m)
		}
		node[idx] = parsed
	default:
		return fmt.Errorf("cannot set %q", path)
	}
	return remarshal(cfg, m)
}

func remarshal(cfg *config.Config, m any) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, cfg)
}

// ----------------------------------------------------------------- usage ---

func usageCmd(args []string) error {
	_, _, led, err := p2Stores()
	if err != nil {
		return err
	}
	name := ""
	for _, a := range args {
		if !strings.HasPrefix(a, "-") && a != "" {
			name = a
			break
		}
	}
	if name != "" {
		s := led.Summarize(name)
		fmt.Printf("burn — sandbox %q\n", name)
		fmt.Printf("  today  $%.4f   total $%.4f   calls %d   tokens in/out %d/%d\n",
			s.Today, s.Total, s.Calls, s.TokensIn, s.TokensOut)
		for _, m := range s.Models {
			estMark := ""
			if m.Est > 0 {
				estMark = fmt.Sprintf("  (est on %d calls)", m.Est)
			}
			fmt.Printf("  %-42s in %7d  out %7d  $%.4f  %d call(s)%s\n",
				m.Model, m.In, m.Out, m.Cost, m.Calls, estMark)
		}
		return nil
	}
	rows := led.Overview()
	if len(rows) == 0 {
		fmt.Println("no usage recorded yet (proxy calls land here per sandbox)")
		return nil
	}
	fmt.Printf("%-24s %10s %10s %8s\n", "SANDBOX", "TODAY $", "TOTAL $", "CALLS")
	for _, r := range rows {
		fmt.Printf("%-24s %10.4f %10.4f %8d\n", r.Sandbox, r.Today, r.Total, r.Calls)
	}
	return nil
}

// ------------------------------------------------------------------- key ---

func keyCmd(args []string) error {
	verb := firstArg(args)
	rest := args
	if verb != "" {
		rest = args[1:]
	}
	_, ks, _, err := p2Stores()
	if err != nil {
		return err
	}
	switch verb {
	case "mint", "ensure":
		name := firstArg(rest)
		if name == "" {
			return fmt.Errorf("usage: sbxd key mint SANDBOX [PROVIDER]")
		}
		prov := ""
		for _, a := range rest {
			if a != name && !strings.HasPrefix(a, "-") {
				prov = a
				break
			}
		}
		if prov == "" {
			cfg, _, _, e := p2Stores()
			if e != nil {
				return e
			}
			prov = cfg.DefaultProvider()
			if prov == "" {
				return fmt.Errorf("no default provider in %s (sbxd config init)", config.Path())
			}
		}
		k, created, err := ks.EnsureKey(name, prov)
		if err != nil {
			return err
		}
		if created {
			ok("minted key for %s (provider %s)\n  %s\n", name, prov, k.Token)
			fmt.Println("  it lives in the sandbox once synced — upstream keys never do")
		} else {
			ok("existing key for %s (provider %s)\n  %s\n", name, prov, k.Token)
		}
		return nil
	case "ls", "list":
		kk := ks.List()
		if len(kk) == 0 {
			fmt.Println("no keys minted yet (sbxd key mint SANDBOX)")
			return nil
		}
		fmt.Printf("%-52s %-18s %-14s %s\n", "TOKEN", "SANDBOX", "PROVIDER", "CREATED")
		for _, k := range kk {
			fmt.Printf("%-52s %-18s %-14s %s\n", k.Token, k.Sandbox, k.Provider, k.Created)
		}
		return nil
	case "revoke":
		name := firstArg(rest)
		if name == "" {
			return fmt.Errorf("usage: sbxd key revoke SANDBOX")
		}
		n, err := ks.Revoke(name)
		if err != nil {
			return err
		}
		ok("revoked %d key(s) for %s (holders start failing with 401)\n", n, name)
		return nil
	default:
		return fmt.Errorf("unknown key verb %q (mint|ls|revoke)", verb)
	}
}

// ---------------------------------------------------------------- budget ---
// Wallets live in config.json: budgets (USD) and budget_tokens. An explicit
// entry beats the proxy default; 0 = unlimited; missing = default.

func usdStr(v float64) string {
	if v <= 0 {
		return "unlimited"
	}
	return fmt.Sprintf("$%.6g", v)
}

func tokStr(n int) string {
	if n <= 0 {
		return "unlimited"
	}
	return fmt.Sprintf("%d tokens", n)
}

func budgetCmd(args []string) error {
	if firstArg(args) == "" {
		return budgetList()
	}
	name := firstArg(args)
	var usdArg, tokArg string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--tokens" && i+1 < len(args):
			i++
			tokArg = args[i]
		case strings.HasPrefix(a, "--tokens="):
			tokArg = strings.TrimPrefix(a, "--tokens=")
		case !strings.HasPrefix(a, "-") && a != name && usdArg == "":
			usdArg = a
		}
	}

	cfg, _, led, err := p2Stores()
	if err != nil {
		return err
	}

	// show one wallet (no USD, no --tokens given)
	if usdArg == "" && tokArg == "" {
		s := led.Summarize(name)
		fmt.Printf("%s: USD wallet %s, token wallet %s\n", name, usdStr(cfg.USDWallet(name)), tokStr(cfg.TokenWallet(name)))
		fmt.Printf("  today: $%.6g / %d tokens   total: $%.6g / %d tokens   (%d calls)\n",
			s.Today, s.TodayTokensIn+s.TodayTokensOut, s.Total, s.TokensIn+s.TokensOut, s.Calls)
		return nil
	}

	var usd *float64
	var tokens *int
	if usdArg != "" {
		v, perr := strconv.ParseFloat(usdArg, 64)
		if perr != nil || v < 0 {
			return fmt.Errorf("USD wallet must be a non-negative number")
		}
		usd = &v
	}
	if tokArg != "" {
		n, perr := strconv.Atoi(tokArg)
		if perr != nil || n < 0 {
			return fmt.Errorf("--tokens must be a non-negative integer")
		}
		tokens = &n
	}
	if err := cfg.SetWallets(name, usd, tokens); err != nil {
		return err
	}
	ok("wallets for %s: USD %s, tokens %s — the proxy 429s when one runs dry (0 = unlimited)\n",
		name, usdStr(cfg.USDWallet(name)), tokStr(cfg.TokenWallet(name)))
	return nil
}

// budgetList renders every configured wallet plus the defaults.
func budgetList() error {
	cfg, _, led, err := p2Stores()
	if err != nil {
		return err
	}
	todayUSD := map[string]float64{}
	todayTok := map[string]int{}
	for _, r := range led.Overview() {
		todayUSD[r.Sandbox] = r.Today
		todayTok[r.Sandbox] = r.Tokens
	}
	names := map[string]bool{}
	for k := range cfg.Budgets {
		names[k] = true
	}
	for k := range cfg.BudgetTokens {
		names[k] = true
	}
	var sorted []string
	for k := range names {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	if len(sorted) > 0 {
		fmt.Printf("%-20s %-12s %-12s %-18s %s\n", "SANDBOX", "USD WALLET", "TODAY", "TOKEN WALLET", "TOK TODAY")
		for _, k := range sorted {
			fmt.Printf("%-20s %-12s %-12s %-18s %d\n",
				k, usdStr(cfg.USDWallet(k)), fmt.Sprintf("$%.6g", todayUSD[k]),
				tokStr(cfg.TokenWallet(k)), todayTok[k])
		}
	} else {
		fmt.Println("no wallets configured (every sandbox unlimited)")
	}
	fmt.Printf("defaults: USD %s, tokens %s  — set with `sbxd config set proxy.default_budget_usd V`\n",
		usdStr(cfg.DefaultBudget()), tokStr(cfg.DefaultTokenWallet()))
	return nil
}

// ------------------------------------------------------------------ sync ---

func syncCmd(args []string) error {
	provider := ""
	targets := []string{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--provider" && i+1 < len(args):
			provider = args[i+1]
			i++
		case strings.HasPrefix(a, "--provider="):
			provider = strings.TrimPrefix(a, "--provider=")
		case a == "--all":
			targets = append(targets, "__all__")
		case !strings.HasPrefix(a, "-"):
			targets = append(targets, a)
		}
	}

	cfg, ks, _, err := p2Stores()
	if err != nil {
		return err
	}
	if len(cfg.Opencode.Providers) == 0 {
		return fmt.Errorf("no providers configured — run `sbxd config init` first")
	}

	sc := &sandbox.Collector{}
	if len(targets) == 1 && targets[0] == "__all__" {
		targets = nil
		for _, sb := range sc.Scan() {
			if sb.State == "running" {
				targets = append(targets, sb.Name)
			}
		}
	}
	if len(targets) == 0 {
		return fmt.Errorf("usage: sbxd sync NAME|all [--provider P]")
	}

	rc := 0
	for _, name := range targets {
		sb, found := sc.Find("", name)
		if !found {
			fmt.Fprintf(os.Stderr, "\033[31;1msbxd:\033[0m sandbox %q not found\n", name)
			rc = 1
			continue
		}
		port := ""
		if len(sb.Ports) > 0 {
			port = strings.TrimPrefix(sb.Ports[0], "127.0.0.1:")
		}
		tr, cfgPath, err := attach.WriteTransport(sb, port)
		if err != nil {
			fmt.Fprintf(os.Stderr, "\033[31;1msbxd:\033[0m %s: %v\n", name, err)
			rc = 1
			continue
		}
		base, via, err := attach.ResolveProxyURL(sb, cfg.Proxy.Port)
		if err != nil {
			fmt.Fprintf(os.Stderr, "\033[31;1msbxd:\033[0m %s: %v\n", name, err)
			rc = 1
			continue
		}
		tg, err := cfgsync.Apply(sb, cfg, ks, tr, base, via, provider)
		if err != nil {
			fmt.Fprintf(os.Stderr, "\033[31;1msbxd:\033[0m %s: %v\n", name, err)
			rc = 1
			continue
		}
		ok("%s → opencode talks through the proxy now\n  provider %s  %s\n  config   %s\n  key      %s\n  reach    %s\n",
			sb.Name, tg.Provider, tg.BaseURL, cfgPath, tg.Key, tg.Via)
	}
	if rc != 0 {
		return fmt.Errorf("some targets failed")
	}
	return nil
}

var _ = engine.LookPath
