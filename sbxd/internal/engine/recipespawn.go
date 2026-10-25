package engine

// Recipe spawn: declarative sandboxes created straight through the docker
// CLI with the same container shape `bin/sbx` produces, but a
// recipe-controlled name (`sbx-<recipe.name>`): identical labels, envs,
// opencode auth mounts, git/ssh identity mounts and the per-stack package
// cache volume, plus the recipe's own mounts/envs/ports.

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"sbxd/internal/audit"
	"sbxd/internal/env"
)

// RecipeImageName is the kit image for a stack ("sbx:<stack>").
func RecipeImageName(stack string) string { return "sbx:" + stack }

// RecipeImageExists reports whether the kit image for a stack is built.
func RecipeImageExists(stack string) bool {
	_, err := RunTO(15*time.Second, "docker", "image", "inspect", RecipeImageName(stack))
	return err == nil
}

// RecipeStacks lists the stacks the kit knows (from bin/sbx STACKS).
var RecipeStacks = map[string]string{ // stack -> named cache volume
	"node":   "sbx-npm",
	"python": "sbx-pip",
	"go":     "sbx-go",
	"rust":   "sbx-cargo",
}

// RecipeSpawn is the resolved content of an sbx.yaml for `recipe up`.
type RecipeSpawn struct {
	Name    string            // recipe.name (container becomes sbx-<name>)
	Stack   string            // required kit stack
	Mounts  []Bind            // recipe mounts, paths pre-expanded & validated
	Env     map[string]string // recipe env
	Ports   []string          // HOST:CT publishes (advertised as label too)
	Project string            // label dev.sbx.project (where the recipe lives)
}

// SpawnRecipeContainer creates+starts the sandbox. The returned name is the
// container the sandbox will be known as.
func SpawnRecipeContainer(s RecipeSpawn) (string, error) {
	cn := "sbx-" + s.Name
	if _, err := Run("docker", "inspect", cn); err == nil {
		return cn, fmt.Errorf("container %s already exists (recipe names must be unique; `sbx rm` or docker rm it first)", cn)
	}
	uid, gid, home := homeUIDGID()
	if s.Stack == "" {
		return cn, fmt.Errorf("stack: required to spawn (`stack: go|node|python|rust|base`)")
	}
	if v, ok := RecipeStacks[s.Stack]; ok {
		if _, err := RunTO(10*time.Second, "docker", "volume", "inspect", v); err != nil {
			if _, e2 := RunTO(10*time.Second, "docker", "volume", "create", v); e2 != nil {
				return cn, fmt.Errorf("cache volume %s: %w", v, e2)
			}
		}
	}
	args := []string{"create", "--name", cn, "--restart", "unless-stopped",
		"--workdir", "/workspace",
		"--label", envLabel + "=1",
		"--label", env.OwnerLabelKey + "=" + env.OwnerLabel(),
		"--label", envLabel + ".project=" + s.Project,
		"--label", envLabel + ".stack=" + s.Stack,
		"--env", "SBX_UID=" + uid,
		"--env", "SBX_GID=" + gid,
		"--env", "SBX_PROJECT=" + s.Name,
	}
	// opencode state/config + host binary (same mounts the kit gives)
	if d := home + "/.local/share/opencode"; dirExists(d) {
		args = append(args, "--volume", d+":/home/sbx/.local/share/opencode")
	}
	if d := home + "/.config/opencode"; dirExists(d) {
		args = append(args, "--volume", d+":/home/sbx/.config/opencode")
	}
	if b := home + "/.opencode/bin/opencode"; fileExists(b) {
		args = append(args, "--volume", b+":/usr/local/bin/opencode:ro")
	}
	if f := home + "/.gitconfig"; fileExists(f) {
		args = append(args, "--volume", f+":/home/sbx/.gitconfig:ro")
	}
	if d := home + "/.ssh"; dirExists(d) {
		args = append(args, "--volume", d+":/home/sbx/.ssh:ro")
	}
	if v, ok := RecipeStacks[s.Stack]; ok {
		args = append(args, "--volume", v+":/"+cachePath(s.Stack))
	}
	for _, m := range s.Mounts {
		spec := m.Source + ":" + m.Target
		if strings.EqualFold(m.Mode, "ro") {
			spec += ":ro"
		}
		args = append(args, "--volume", spec)
	}
	keys := make([]string, 0, len(s.Env))
	for k := range s.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "--env", k+"="+s.Env[k])
	}
	if len(s.Ports) > 0 {
		args = append(args, "--label", envLabel+".ports="+strings.Join(s.Ports, ","))
	}
	for _, p := range s.Ports {
		args = append(args, "--publish", p)
	}
	args = append(args, RecipeImageName(s.Stack))
	if out, err := RunTO(60*time.Second, "docker", args...); err != nil {
		return cn, wrapErr(err, out)
	}
	if err := StartContainer(cn); err != nil {
		return cn, fmt.Errorf("created but start failed: %w", err)
	}
	audit.Record("sandbox.spawn", cn, "stack="+s.Stack)
	return cn, nil
}

const envLabel = "dev.sbx"

// homeUIDGID resolves the daemon's host user (matches the kit's user full stop).
func homeUIDGID() (uid, gid, home string) {
	uid = strconv.Itoa(os.Getuid())
	gid = strconv.Itoa(os.Getgid())
	home = os.Getenv("HOME")
	if home == "" {
		if h, err := os.UserHomeDir(); err == nil {
			home = h
		}
	}
	return uid, gid, home
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

func cachePath(stack string) string {
	switch stack {
	case "node":
		return "home/sbx/.npm"
	case "python":
		return "home/sbx/.cache/pip"
	case "go":
		return "home/sbx/go"
	case "rust":
		return "usr/local/cargo"
	}
	return "tmp/cache-" + stack
}
