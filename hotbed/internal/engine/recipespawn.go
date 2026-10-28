package engine

// Recipe spawn: declarative sandboxes created straight through the docker
// CLI with the same container shape `bin/hbd` produces, but a
// recipe-controlled name (`hotbed-<recipe.name>`): identical labels, envs,
// opencode auth mounts, git/ssh identity mounts and the per-stack package
// cache volume, plus the recipe's own mounts/envs/ports.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"hotbed/internal/audit"
	"hotbed/internal/env"
)

// RecipeImageName is the kit image for a stack ("hotbed:<stack>").
func RecipeImageName(stack string) string { return "hotbed:" + stack }

// RecipeImageExists reports whether the kit image for a stack is built.
func RecipeImageExists(stack string) bool {
	_, err := RunTO(15*time.Second, "docker", "image", "inspect", RecipeImageName(stack))
	return err == nil
}

// RecipeImageEnsure makes sure the kit image for stack exists locally,
// building it through the shared containers/Dockerfile when it does not —
// same flags `bin/hbd build <stack>` uses (one-time per stack, layers
// cached afterwards). 'hotbed:<stack>' is a kit-built image with no registry
// upstream, so a fresh box's dashboard create would otherwise dangle on
// 'pull access denied for hbd' from the docker CLI.
func RecipeImageEnsure(stack string) error {
	if _, err := RunTO(15*time.Second, "docker", "image", "inspect", RecipeImageName(stack)); err == nil {
		return nil
	}
	root := env.Root()
	df := filepath.Join(root, "containers", "Dockerfile")
	if _, err := os.Stat(df); err != nil {
		return fmt.Errorf("image %s not found locally and no build files at %s — build it once from the kit tree: bin/hbd build %s",
			RecipeImageName(stack), root, stack)
	}
	audit.Record("image.build", RecipeImageName(stack), "missing — built during create ("+stack+")")
	args := []string{
		"build",
		"--label", envLabel + ".recipe=" + recipeStamp(),
		"--target", stack,
		"-t", RecipeImageName(stack), "-f", df, root,
	}
	out, err := RunTO(20*time.Minute, "docker", args...)
	if err != nil {
		return fmt.Errorf("building %s failed (one-time per stack; manual: bin/hbd build %s): %s",
			RecipeImageName(stack), stack, tailLines(out, 4))
	}
	return nil
}

// recipeStamp: 8-hex sha256 of containers/Dockerfile, the kit recipe's
// identity. Both build paths stamp it onto the image (bin/hbd build and
// this daemon one), so a later check can tell a pre-provenance or
// recipe-drifted base from a fresh one — the quiet trap where a stale
// previously-built hotbed:<stack> kept serving containers that no longer match
// the kit tree (the classic "caching problem").
func recipeStamp() string {
	b, err := os.ReadFile(filepath.Join(env.Root(), "containers", "Dockerfile"))
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:8]
}

// imageRecipeStamp reads an image's dev.hotbed.recipe label. Empty return =
// unstamped (built before provenance existed) or unreadable.
func imageRecipeStamp(image string) string {
	out, err := RunTO(15*time.Second, "docker", "image", "inspect",
		"--format", `{{index .Config.Labels "dev.hotbed.recipe"}}`, image)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// tailLines is the honest-error helper: build logs are long, the lines that
// matter are the last ones.
func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(strings.TrimSpace(s), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " • ")
}

// RecipeStacks lists the stacks the kit knows (from bin/hbd STACKS).
var RecipeStacks = map[string]string{ // stack -> named cache volume
	"node":   "hotbed-npm",
	"python": "hotbed-pip",
	"go":     "hotbed-go",
	"rust":   "hotbed-cargo",
}

// RecipeSpawn is the resolved content of an hbd.yaml for `recipe up`.
type RecipeSpawn struct {
	Name    string            // recipe.name (container becomes hotbed-<name>)
	Stack   string            // required kit stack
	Mounts  []Bind            // recipe mounts, paths pre-expanded & validated
	Env     map[string]string // recipe env
	Ports   []string          // HOST:CT publishes (advertised as label too)
	Project string            // label dev.hotbed.project (where the recipe lives)
}

// SpawnRecipeContainer creates+starts the sandbox. The returned name is the
// container the sandbox will be known as; the returned warn ("" when clean)
// tells whether a pre-existing stack image predates/mismatches the current
// kit recipe — the "caching problem": a stale hotbed:<stack> serves containers
// whose provisioning no longer matches the tree.
func SpawnRecipeContainer(s RecipeSpawn) (string, string, error) {
	cn := "hotbed-" + s.Name
	if _, err := Run("docker", "inspect", cn); err == nil {
		return cn, "", fmt.Errorf("container %s already exists (recipe names must be unique; `hbd rm` or docker rm it first)", cn)
	}
	uid, gid, home := homeUIDGID()
	if s.Stack == "" {
		return cn, "", fmt.Errorf("stack: required to spawn (`stack: go|node|python|rust|base`)")
	}
	existed := RecipeImageExists(s.Stack)
	if err := RecipeImageEnsure(s.Stack); err != nil {
		return cn, "", err
	}
	warn := ""
	if existed {
		if st := imageRecipeStamp(RecipeImageName(s.Stack)); st != recipeStamp() {
			if st == "" {
				warn = fmt.Sprintf("image %s predates recipe stamping (no provenance label) — if provisioning looks stale, rebuild it once: bin/hbd build %s --no-cache",
					RecipeImageName(s.Stack), s.Stack)
			} else {
				warn = fmt.Sprintf("image %s was built from an older kit recipe (stamp %s, current %s) — rebuild it once: bin/hbd build %s --no-cache",
					RecipeImageName(s.Stack), st, recipeStamp(), s.Stack)
			}
			audit.Record("image.stale", RecipeImageName(s.Stack), "serving from a "+(map[bool]string{true: "drifted", false: "unstamped"}[st != ""])+" image")
		}
	}
	if v, ok := RecipeStacks[s.Stack]; ok {
		if _, err := RunTO(10*time.Second, "docker", "volume", "inspect", v); err != nil {
			if _, e2 := RunTO(10*time.Second, "docker", "volume", "create", v); e2 != nil {
				return cn, warn, fmt.Errorf("cache volume %s: %w", v, e2)
			}
		}
	}
	args := []string{"create", "--name", cn, "--restart", "unless-stopped",
		"--workdir", "/workspace",
		"--label", envLabel + "=1",
		"--label", env.OwnerLabelKey + "=" + env.OwnerLabel(),
		"--label", envLabel + ".project=" + s.Project,
		"--label", envLabel + ".stack=" + s.Stack, "--env", "HBD_UID=" + uid,
		"--env", "HBD_GID=" + gid,
		"--env", "HBD_PROJECT=" + s.Name,
	}
	// opencode state/config + host binary (same mounts the kit gives)
	if d := home + "/.local/share/opencode"; dirExists(d) {
		args = append(args, "--volume", d+":/home/hbd/.local/share/opencode")
	}
	if d := home + "/.config/opencode"; dirExists(d) {
		args = append(args, "--volume", d+":/home/hbd/.config/opencode")
	}
	if b := home + "/.opencode/bin/opencode"; fileExists(b) {
		args = append(args, "--volume", b+":/usr/local/bin/opencode:ro")
	}
	if f := home + "/.gitconfig"; fileExists(f) {
		args = append(args, "--volume", f+":/home/hbd/.gitconfig:ro")
	}
	if d := home + "/.ssh"; dirExists(d) {
		args = append(args, "--volume", d+":/home/hbd/.ssh:ro")
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
		return cn, warn, wrapErr(err, out)
	}
	if err := StartContainer(cn); err != nil {
		return cn, warn, fmt.Errorf("created but start failed: %w", err)
	}
	// nothing bind-mounted at /workspace: the image's root-owned directory
	// must still be writable for the sandbox user (agents work there).
	// Images built with the current entrypoint do this themselves; this
	// covers the ones already on disk.
	mounted := false
	for _, m := range s.Mounts {
		if strings.TrimRight(m.Target, "/") == "/workspace" {
			mounted = true
		}
	}
	if !mounted {
		if out, err := RunTO(20*time.Second, "docker", "exec", "-u", "root", cn,
			"chown", uid+":"+gid, "/workspace"); err != nil {
			warn = strings.TrimPrefix(warn+"; /workspace not handed to the sandbox user: "+strings.TrimSpace(out), "; ")
		}
	}
	audit.Record("sandbox.spawn", cn, "stack="+s.Stack)
	return cn, warn, nil
}

const envLabel = "dev.hotbed"

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
		return "home/hbd/.npm"
	case "python":
		return "home/hbd/.cache/pip"
	case "go":
		return "home/hbd/go"
	case "rust":
		return "usr/local/cargo"
	}
	return "tmp/cache-" + stack
}
