package cli

// `hotbedd recipe …` — declarative sandboxes (hbd.yaml): parse/validate/print.
// Spawning a recipe lands with the P3 create-wizard (the print output is the
// exact `bin/hbd` invocation that flow will run).

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"hotbed/internal/engine"
	"hotbed/internal/env"
	"hotbed/internal/recipe"
)

func recipeCmd(args []string) error {
	verb := firstArg(args)
	rest := args
	if verb != "" && !strings.HasPrefix(verb, "-") {
		rest = args[1:]
	}
	switch verb {
	case "print":
		return recipePrint(rest)
	case "validate":
		return recipeValidate(rest)
	case "up":
		return recipeUp(rest)
	default:
		return fmt.Errorf("usage: hotbedd recipe print FILE | validate FILE | up FILE")
	}
}

func loadRecipe(path string) (*recipe.Recipe, error) {
	if path == "" {
		path = "hbd.yaml"
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	r, err := recipe.Parse(string(b))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := r.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return r, nil
}

func recipeValidate(args []string) error {
	r, err := loadRecipe(firstArg(args))
	if err != nil {
		return err
	}
	ok("%s is a valid recipe ✓  (%s)\n", firstArg(args), r.Name)
	return nil
}

// BinFlags renders the kit flags a spawn flow would pass to `bin/hbd`.
func binFlags(r *recipe.Recipe) []string {
	var out []string
	if r.Stack != "" {
		out = append(out, "--stack", r.Stack)
	}
	for _, m := range r.Mounts {
		out = append(out, "--mount", m)
	}
	keys := make([]string, 0, len(r.Env))
	for k := range r.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, "--env", k+"="+r.Env[k])
	}
	return out
}

func recipePrint(args []string) error {
	path := firstArg(args)
	r, err := loadRecipe(path)
	if err != nil {
		return err
	}
	pb, pt := r.Budget, r.BudgetTokens
	fmt.Printf("recipe %s\n", path)
	fmt.Printf("  name:          %s\n", r.Name)
	fmt.Printf("  stack:         %s\n", r.Stack)
	if len(r.Ports) > 0 {
		fmt.Printf("  ports:         %s   (kit: ssh/22 auto-published at spawn; extras need the create wizard)\n", strings.Join(r.Ports, ", "))
	}
	if len(r.Mounts) > 0 {
		fmt.Printf("  mounts:\n")
		for _, m := range r.Mounts {
			fmt.Printf("    %s\n", m)
		}
	}
	if len(r.Env) > 0 {
		keys := make([]string, 0, len(r.Env))
		for k := range r.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Printf("  env:\n")
		for _, k := range keys {
			fmt.Printf("    %s=%s\n", k, r.Env[k])
		}
	}
	if pb != nil {
		fmt.Printf("  budget:        $%.6g\n", *pb)
	}
	if pt != nil {
		fmt.Printf("  budget_tokens: %d\n", *pt)
	}
	if r.Egress != "" {
		fmt.Printf("  egress:        %s\n", r.Egress)
	}
	if r.Sync {
		fmt.Printf("  sync:          true (provider=%s, model=%s)\n", r.Provider, r.Model)
	}
	if r.Attach != "" {
		fmt.Printf("  attach:        %s\n", r.Attach)
	}
	flags := binFlags(r)
	fmt.Printf("  bin/hbd flags: %s\n", strings.Join(flags, " "))
	attachExtra := ""
	if r.Attach != "" {
		attachExtra = " " + r.Attach
	}
	fmt.Printf("  then:          spawn container as hotbed-%s via `hotbedd recipe up %s`; %s attach hotbed-%s%s\n", r.Name, "$FILE", kitRootSbx(), r.Name, attachExtra)
	return nil
}

func kitRootSbx() string {
	if _, err := os.Stat(env.Root() + "/bin/hbd"); err == nil {
		return env.Root() + "/bin/hbd"
	}
	return "hbd"
}

// recipeUp spawns the recipe (containers only): validate → kit image →
// docker create+start → wallets → egress → optional key+sync → attach hint.
func recipeUp(args []string) error {
	yes := hasFlag(args, "--yes")
	var clean []string
	for _, a := range args {
		if a != "--yes" {
			clean = append(clean, a)
		}
	}
	path := firstArg(clean)
	r, err := loadRecipe(path)
	if err != nil {
		return err
	}
	cn := "hotbed-" + r.Name
	if !r.Sync && r.Provider == "" {
		// nothing to decide — spawn is the intent of this verb
	}
	if err := confirm(fmt.Sprintf("spawn %s from %s (stop→create→start for the sandbox; mounts/ports bake in at creation)", cn, path), yes); err != nil {
		return err
	}
	base := ""
	if abs, e := filepath.Abs(path); e == nil {
		base = filepath.Dir(abs)
	}
	home, _ := os.UserHomeDir()
	mounts := make([]engine.Bind, 0, len(r.Mounts))
	seen := map[string]bool{}
	for _, m := range r.Mounts {
		parts := strings.SplitN(m, ":", 3)
		src, tgt := parts[0], parts[1]
		if strings.HasPrefix(src, "~") {
			src = home + strings.TrimPrefix(src, "~")
		}
		if seen[tgt] {
			return fmt.Errorf("duplicate container target in mounts: %s", tgt)
		}
		seen[tgt] = true
		mode := "rw"
		if len(parts) == 3 {
			mode = parts[2]
		}
		mounts = append(mounts, engine.Bind{Source: src, Target: tgt, Mode: mode})
	}
	if !engine.RecipeImageExists(r.Stack) {
		kb := kitRootSbx()
		return fmt.Errorf("kit image %s is not built — run `%s build %s` first (one-time per stack)", engine.RecipeImageName(r.Stack), kb, r.Stack)
	}
	cn, imgWarn, err2 := engine.SpawnRecipeContainer(engine.RecipeSpawn{
		Name:    r.Name,
		Stack:   r.Stack,
		Mounts:  mounts,
		Env:     r.Env,
		Ports:   r.Ports,
		Project: base,
	})
	if err2 != nil {
		return err2
	}
	if imgWarn != "" {
		fmt.Printf("\033[33;1mhotbedd:\033[0m %s\n", imgWarn)
	}
	ok("spawned %s (stack=%s) — syncing dashboard in ~5s\n", cn, r.Stack)

	// wallets
	if r.Budget != nil || r.BudgetTokens != nil {
		cfg, _, _, e := p2Stores()
		if e == nil {
			if e := cfg.SetWallets(cn, r.Budget, r.BudgetTokens); e == nil {
				if r.Budget != nil {
					fmt.Printf("wallet:  $%.6g\n", *r.Budget)
				}
				if r.BudgetTokens != nil {
					fmt.Printf("tokens:  %d\n", *r.BudgetTokens)
				}
			}
		}
	}
	// egress
	if r.Egress != "" {
		var e error
		var done string
		switch {
		case r.Egress == "allowlist":
			if list := configAllowlist(cn); len(list) == 0 {
				e = fmt.Errorf("no domains in config egress_allowlist for %s", cn)
			} else {
				var n int
				if n, e = engine.ApplyEgressFence(cn, r.Egress, list); e == nil {
					done = fmt.Sprintf("allowlist (%d domains, %d ips)", len(list), n)
				}
			}
		default:
			if e = engine.ApplyEgressContainer(cn, r.Egress); e == nil && r.Egress == "allow" {
				done = "allow (reset)"
			} else if e == nil {
				done = r.Egress
			}
		}
		if e != nil {
			fmt.Fprintf(os.Stderr, "\033[33;1mhotbedd:\033[0m egress %s failed: %v\n", r.Egress, e)
		} else {
			fmt.Printf("egress:  %s\n", done)
		}
	}
	// opencode config sync (needs a minted key; reuses the P2 flow)
	if r.Sync {
		sd := []string{cn}
		if r.Provider != "" {
			sd = append(sd, "--provider", r.Provider)
		}
		if r.Model != "" {
			sd = append(sd, "--model", r.Model)
		}
		if e := syncCmd(sd); e != nil {
			fmt.Fprintf(os.Stderr, "\033[33;1mhotbedd:\033[0m sync skipped: %v\n", e)
		}
	}
	attachExtra := ""
	if r.Attach != "" {
		attachExtra = " " + r.Attach
	}
	fmt.Printf("attach:  %s attach %s%s\n", kitRootSbx(), cn, attachExtra)
	return nil
}
