package engine

// Custom images (issue #1): new sandboxes can start from more than the
// kit stacks.
//
//	spec      a declarative image (ImageSpec: from + apt + run + env),
//	          built FROM a kit stack (or another custom image) into
//	          hotbed-custom:<name>; the spec is kept in the config dir so
//	          the image can be rebuilt
//	snapshot  "save sandbox as image": a running container's filesystem
//	          becomes hotbed-custom:<name> — scrubbed of secrets first
//	          (ScrubScript) and flattened (export | import), so no layer
//	          below still carries what the scrub deleted
//
// Both are used as stack "custom:<name>" (wizard, recipes, API). VM base
// images live in images_vm.go.

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"hotbed/internal/audit"
	"hotbed/internal/env"
)

// CustomRepo is the docker repository of every custom image.
const CustomRepo = "hotbed-custom"

// CustomPrefix marks a custom image where a stack is expected.
const CustomPrefix = "custom:"

var imageNameRe = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,39}$`)

// ValidImageName: lowercase, starts with a letter, <= 40 chars (a docker
// tag and a file name at once).
func ValidImageName(n string) bool { return imageNameRe.MatchString(n) }

// CustomStack splits "custom:<name>" (ok=false for kit stacks).
func CustomStack(stack string) (string, bool) {
	n, ok := strings.CutPrefix(stack, CustomPrefix)
	return n, ok && n != ""
}

// CustomImageRef is the docker reference of a custom image.
func CustomImageRef(name string) string { return CustomRepo + ":" + name }

// KnownStack reports whether stack can spawn a container: a kit stack, or
// a custom image that exists locally.
func KnownStack(stack string) bool {
	if n, ok := CustomStack(stack); ok {
		_, err := RunTO(15*time.Second, "docker", "image", "inspect", CustomImageRef(n))
		return err == nil
	}
	_, ok := RecipeStacks[stack]
	return ok || stack == "base"
}

// rootStack is the kit stack an image ultimately builds on (cache volume,
// cache path): itself for kit stacks, the dev.hotbed.base label otherwise.
func rootStack(stack string) string {
	n, ok := CustomStack(stack)
	if !ok {
		return stack
	}
	out, err := RunTO(15*time.Second, "docker", "image", "inspect", "--format",
		`{{index .Config.Labels "dev.hotbed.base"}}`, CustomImageRef(n))
	if err != nil {
		return "base"
	}
	if b := strings.TrimSpace(out); b != "" {
		return b
	}
	return "base"
}

// ------------------------------------------------------------------ spec ---

// ImageSpec is a declarative custom image.
type ImageSpec struct {
	Name        string            `json:"name"`
	From        string            `json:"from"`          // kit stack or custom:<name>
	Apt         []string          `json:"apt,omitempty"` // Debian packages
	Run         []string          `json:"run,omitempty"` // shell steps, as root, in order
	Env         map[string]string `json:"env,omitempty"`
	Description string            `json:"description,omitempty"`
}

var (
	aptRe = regexp.MustCompile(`^[a-z0-9][a-z0-9+.~-]*(=[A-Za-z0-9+.~:-]+)?$`)
	envRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// Validate checks a spec before anything is built.
func (s ImageSpec) Validate() error {
	if !ValidImageName(s.Name) {
		return fmt.Errorf("image name %q: lowercase letters, digits, . _ - (start with a letter, max 40)", s.Name)
	}
	if n, ok := CustomStack(s.From); ok {
		if n == s.Name {
			return fmt.Errorf("image %s cannot build from itself", s.Name)
		}
	} else if _, ok := RecipeStacks[s.From]; !ok && s.From != "base" {
		return fmt.Errorf("from %q: a kit stack (go|node|python|rust|base) or custom:<name>", s.From)
	}
	for _, p := range s.Apt {
		if !aptRe.MatchString(p) {
			return fmt.Errorf("apt package %q is not a package name", p)
		}
	}
	for k := range s.Env {
		if !envRe.MatchString(k) {
			return fmt.Errorf("env name %q", k)
		}
	}
	for _, r := range s.Run {
		if strings.TrimSpace(r) == "" {
			return fmt.Errorf("empty run step")
		}
	}
	if len(s.Apt) == 0 && len(s.Run) == 0 && len(s.Env) == 0 {
		return fmt.Errorf("image %s adds nothing to %s (give apt packages, run steps or env)", s.Name, s.From)
	}
	return nil
}

// Dockerfile renders the spec (JSON exec forms: no shell quoting games).
func (s ImageSpec) Dockerfile(fromRef, root string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "FROM %s\nUSER root\n", fromRef)
	keys := make([]string, 0, len(s.Env))
	for k := range s.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v, _ := json.Marshal(s.Env[k])
		fmt.Fprintf(&b, "ENV %s=%s\n", k, v)
	}
	if len(s.Apt) > 0 {
		step := "apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends " +
			strings.Join(s.Apt, " ") + " && rm -rf /var/lib/apt/lists/*"
		j, _ := json.Marshal([]string{"/bin/bash", "-euo", "pipefail", "-c", step})
		fmt.Fprintf(&b, "RUN %s\n", j)
	}
	for _, r := range s.Run {
		j, _ := json.Marshal([]string{"/bin/bash", "-euo", "pipefail", "-c", r})
		fmt.Fprintf(&b, "RUN %s\n", j)
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%v", s)))
	fmt.Fprintf(&b, "LABEL %s.custom=%q %s.base=%q %s.origin=\"spec\" %s.spec=%q\n",
		envLabel, s.Name, envLabel, root, envLabel, envLabel, hex.EncodeToString(sum[:])[:12])
	return b.String()
}

// ImageSpecDir keeps the specs (rebuild source).
func ImageSpecDir() string { return filepath.Join(env.ConfigDir(), "images") }

// SaveImageSpec persists a spec.
func SaveImageSpec(s ImageSpec) error {
	if err := os.MkdirAll(ImageSpecDir(), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(s, "", "  ")
	return os.WriteFile(filepath.Join(ImageSpecDir(), s.Name+".json"), append(b, '\n'), 0o644)
}

// LoadImageSpec reads a stored spec.
func LoadImageSpec(name string) (ImageSpec, error) {
	var s ImageSpec
	b, err := os.ReadFile(filepath.Join(ImageSpecDir(), name+".json"))
	if err != nil {
		return s, err
	}
	return s, json.Unmarshal(b, &s)
}

// BuildCustomImage builds a spec into hotbed-custom:<name>; build output
// streams to logw.
func BuildCustomImage(s ImageSpec, logw io.Writer) error {
	if err := s.Validate(); err != nil {
		return err
	}
	fromRef, root := "", s.From
	if n, ok := CustomStack(s.From); ok {
		fromRef = CustomImageRef(n)
		if _, err := RunTO(15*time.Second, "docker", "image", "inspect", fromRef); err != nil {
			return fmt.Errorf("from %s: image %s not found", s.From, fromRef)
		}
		root = rootStack(s.From)
	} else {
		if err := RecipeImageEnsure(s.From); err != nil {
			return err
		}
		fromRef = RecipeImageName(s.From)
	}
	cmd := exec.Command("docker", "build", "--progress=plain", "-t", CustomImageRef(s.Name), "-")
	cmd.Stdin = strings.NewReader(s.Dockerfile(fromRef, root))
	cmd.Stdout, cmd.Stderr = logw, logw
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker build %s: %w", CustomImageRef(s.Name), err)
	}
	if err := SaveImageSpec(s); err != nil {
		return err
	}
	audit.Record("image.build", CustomImageRef(s.Name), "from "+s.From)
	return nil
}

// -------------------------------------------------------------- snapshot ---

// SecretPattern finds credentials a scrub must not leave behind: hotbed
// virtual keys and common provider/git token shapes (values, not names —
// library code mentioning "refresh_token" does not match).
const SecretPattern = `hbk-[a-z0-9]+-[a-z0-9-]+-[0-9a-f]{12,}|sk-ant-[A-Za-z0-9_-]{20,}|sk-proj-[A-Za-z0-9_-]{20,}|` +
	`sk-[A-Za-z0-9]{32,}|ghp_[A-Za-z0-9]{36}|github_pat_[A-Za-z0-9_]{30,}|` +
	`"(refresh|access)_token"[[:space:]]*:[[:space:]]*"[^"]{20,}"`

// ScrubScript (POSIX sh, as root) removes what a sandbox image must not
// carry: hotbed's agent env files (virtual keys), synced OpenCode config
// and auth, agent logins, the strict egress proxy settings, shell
// history, temp files and the VM-/container-local workspace — then
// deletes every remaining file in the scanned trees whose content matches
// SecretPattern and prints "SCRUBBED <path>" for each. home is the
// sandbox user's home glob (/home/*).
func ScrubScript() string {
	return `set -u
for h in /root /home/*; do
  [ -d "$h" ] || continue
  rm -rf "$h/.config/hotbed" "$h/.config/opencode" "$h/.local/share/opencode/auth.json" \
         "$h/.claude" "$h/.claude.json" "$h/.codex" "$h/.hermes/.env" "$h/.hermes/auth.json" \
         "$h/.openclaw/credentials" "$h/.openclaw/openclaw.json" "$h/.aider.conf.yml" "$h/.env" \
         "$h/.git-credentials" "$h/.netrc" "$h/.docker/config.json" "$h/.npmrc" "$h/.pypirc" \
         "$h/.bash_history" "$h/.zsh_history" "$h/.python_history" "$h/.node_repl_history" \
         "$h/.lesshst" "$h/.viminfo" "$h/.wget-hsts" "$h/.ssh/known_hosts" 2>/dev/null
done
rm -f /etc/profile.d/hotbed-egress.sh /etc/apt/apt.conf.d/90hotbed-egress
[ -f /etc/environment ] && sed -i '/^# hotbed-egress begin$/,/^# hotbed-egress end$/d' /etc/environment
rm -f /etc/sudoers.d/hotbed-u* 2>/dev/null
if [ -d /workspace ] && ! mountpoint -q /workspace 2>/dev/null; then
  find /workspace -mindepth 1 -maxdepth 1 -exec rm -rf {} + 2>/dev/null
fi
rm -rf /tmp/* /tmp/.[!.]* /var/tmp/* 2>/dev/null
for d in /root /home /etc /opt /srv /var/lib /usr/local/etc /usr/local/share; do
  [ -d "$d" ] || continue
  grep -rIlE --exclude-dir=node_modules --exclude-dir=site-packages --exclude-dir=.cache '` + SecretPattern + `' "$d" 2>/dev/null
done | while IFS= read -r f; do rm -f "$f" && echo "SCRUBBED $f"; done
true
`
}

// VerifyScript prints "LEAK <path>" for files still matching SecretPattern.
func VerifyScript() string {
	return `for d in /root /home /etc /opt /srv /var/lib /usr/local/etc /usr/local/share /workspace; do
  [ -d "$d" ] || continue
  grep -rIlE --exclude-dir=node_modules --exclude-dir=site-packages --exclude-dir=.cache '` + SecretPattern + `' "$d" 2>/dev/null
done | sed 's/^/LEAK /'
for h in /root /home/*; do [ -e "$h/.config/hotbed" ] && echo "LEAK $h/.config/hotbed"; done
true
`
}

// SaveContainerImage snapshots a container into hotbed-custom:<name>.
// The live sandbox is never modified (docker commit pauses it briefly);
// scrubbing happens on a throwaway, network-less copy, and the result is
// flattened so the deleted files exist in no layer. Image config (env,
// entrypoint, workdir) comes from the kit stack image — the sandbox's own
// --env (recipe values, possibly secrets) is not carried over. Returns
// the scrubbed-file report.
func SaveContainerImage(container, name string, logf func(string)) ([]string, error) {
	if !ValidImageName(name) {
		return nil, fmt.Errorf("image name %q: lowercase letters, digits, . _ - (start with a letter, max 40)", name)
	}
	stack := strings.TrimSpace(mustOut(RunTO(10*time.Second, "docker", "inspect", "--format",
		`{{index .Config.Labels "dev.hotbed.stack"}}`, container)))
	if stack == "" {
		return nil, fmt.Errorf("%s is not a hotbed sandbox container (no dev.hotbed.stack label)", container)
	}
	root := rootStack(stack)
	base := RecipeImageName(stack) // env/entrypoint of the image it runs (spec env included), never the container's own --env
	var cfg struct {
		Env        []string
		Entrypoint []string
		Cmd        []string
		WorkingDir string
	}
	raw, err := RunTO(15*time.Second, "docker", "image", "inspect", "--format", "{{json .Config}}", base)
	if err != nil {
		return nil, fmt.Errorf("base image %s: %w", base, err)
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &cfg); err != nil {
		return nil, fmt.Errorf("base image config: %w", err)
	}

	tag := randTag()
	tmpImg, tmpCt := "hotbed-save-tmp:"+tag, "hotbed-save-"+tag
	defer func() {
		_, _ = RunTO(60*time.Second, "docker", "rm", "-f", tmpCt)
		_, _ = RunTO(60*time.Second, "docker", "image", "rm", "-f", tmpImg)
	}()
	logf("committing " + container + " (paused for the copy)")
	if out, err := RunTO(10*time.Minute, "docker", "commit", "--pause=true", container, tmpImg); err != nil {
		return nil, fmt.Errorf("commit: %w: %s", err, strings.TrimSpace(out))
	}
	if out, err := RunTO(60*time.Second, "docker", "run", "-d", "--name", tmpCt, "--network", "none",
		"--entrypoint", "sleep", tmpImg, "infinity"); err != nil {
		return nil, fmt.Errorf("scrub container: %w: %s", err, strings.TrimSpace(out))
	}
	logf("scrubbing secrets")
	out, err := RunTO(10*time.Minute, "docker", "exec", "-u", "root", tmpCt, "sh", "-c", ScrubScript())
	if err != nil {
		return nil, fmt.Errorf("scrub: %w: %s", err, tailLines(out, 4))
	}
	var scrubbed []string
	for _, l := range strings.Split(out, "\n") {
		if p, ok := strings.CutPrefix(strings.TrimSpace(l), "SCRUBBED "); ok {
			scrubbed = append(scrubbed, p)
		}
	}
	if leaks := leaksIn(RunTO(5*time.Minute, "docker", "exec", "-u", "root", tmpCt, "sh", "-c", VerifyScript())); len(leaks) > 0 {
		return scrubbed, fmt.Errorf("secrets still present after the scrub (%s) - not saving", strings.Join(leaks, ", "))
	}
	_, _ = RunTO(60*time.Second, "docker", "stop", "-t", "1", tmpCt)

	logf("flattening into " + CustomImageRef(name))
	changes := []string{}
	for _, e := range cfg.Env {
		changes = append(changes, "ENV "+e)
	}
	if len(cfg.Entrypoint) > 0 {
		j, _ := json.Marshal(cfg.Entrypoint)
		changes = append(changes, "ENTRYPOINT "+string(j))
	}
	if len(cfg.Cmd) > 0 {
		j, _ := json.Marshal(cfg.Cmd)
		changes = append(changes, "CMD "+string(j))
	}
	if cfg.WorkingDir != "" {
		changes = append(changes, "WORKDIR "+cfg.WorkingDir)
	}
	changes = append(changes, fmt.Sprintf("LABEL %s.custom=%q %s.base=%q %s.origin=\"snapshot\" %s.from=%q",
		envLabel, name, envLabel, root, envLabel, envLabel, container))
	if err := exportImport(tmpCt, CustomImageRef(name), changes); err != nil {
		return scrubbed, err
	}
	// last word: the image that will actually be used
	if leaks := leaksIn(RunTO(5*time.Minute, "docker", "run", "--rm", "--network", "none", "--entrypoint", "sh",
		CustomImageRef(name), "-c", VerifyScript())); len(leaks) > 0 {
		_, _ = RunTO(60*time.Second, "docker", "image", "rm", "-f", CustomImageRef(name))
		return scrubbed, fmt.Errorf("saved image failed verification (%s) - removed", strings.Join(leaks, ", "))
	}
	audit.Record("image.save", CustomImageRef(name), fmt.Sprintf("from %s (%d file(s) scrubbed)", container, len(scrubbed)))
	return scrubbed, nil
}

// exportImport pipes `docker export ct` into `docker import` (one layer).
func exportImport(ct, ref string, changes []string) error {
	exp := exec.Command("docker", "export", ct)
	args := []string{"import"}
	for _, c := range changes {
		args = append(args, "--change", c)
	}
	imp := exec.Command("docker", append(args, "-", ref)...)
	pr, pw := io.Pipe()
	exp.Stdout = pw
	imp.Stdin = pr
	var eerr, ierr bytes.Buffer
	exp.Stderr, imp.Stderr = &eerr, &ierr
	if err := imp.Start(); err != nil {
		return err
	}
	expErr := exp.Run()
	pw.CloseWithError(expErr)
	impErr := imp.Wait()
	if expErr != nil {
		return fmt.Errorf("docker export: %w: %s", expErr, strings.TrimSpace(eerr.String()))
	}
	if impErr != nil {
		return fmt.Errorf("docker import: %w: %s", impErr, strings.TrimSpace(ierr.String()))
	}
	return nil
}

func leaksIn(out string, err error) []string {
	var l []string
	for _, s := range strings.Split(out, "\n") {
		if p, ok := strings.CutPrefix(strings.TrimSpace(s), "LEAK "); ok {
			l = append(l, p)
		}
	}
	if err != nil && len(l) == 0 {
		l = append(l, "verification failed to run: "+err.Error())
	}
	return l
}

func mustOut(s string, _ error) string { return s }

func randTag() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ---------------------------------------------------------------- listing ---

// CustomImage is one custom container image.
type CustomImage struct {
	Name    string `json:"name"`
	Ref     string `json:"ref"`
	Origin  string `json:"origin"` // spec | snapshot
	Base    string `json:"base"`   // kit stack underneath
	From    string `json:"from,omitempty"`
	Size    string `json:"size"`
	Created string `json:"created"`
	HasSpec bool   `json:"has_spec"`
}

// ListCustomImages lists hotbed-custom:* images.
func ListCustomImages() ([]CustomImage, error) {
	out, err := RunTO(20*time.Second, "docker", "image", "ls", CustomRepo, "--format",
		`{{.Tag}}|{{.Size}}|{{.CreatedAt}}`)
	if err != nil {
		return nil, err
	}
	var list []CustomImage
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.SplitN(l, "|", 3)
		if len(f) < 3 || f[0] == "" || f[0] == "<none>" {
			continue
		}
		ci := CustomImage{Name: f[0], Ref: CustomImageRef(f[0]), Size: f[1], Created: f[2]}
		lab := mustOut(RunTO(15*time.Second, "docker", "image", "inspect", "--format",
			`{{index .Config.Labels "dev.hotbed.origin"}}|{{index .Config.Labels "dev.hotbed.base"}}|{{index .Config.Labels "dev.hotbed.from"}}`, ci.Ref))
		if p := strings.SplitN(strings.TrimSpace(lab), "|", 3); len(p) == 3 {
			ci.Origin, ci.Base, ci.From = p[0], p[1], p[2]
		}
		if _, err := os.Stat(filepath.Join(ImageSpecDir(), ci.Name+".json")); err == nil {
			ci.HasSpec = true
		}
		list = append(list, ci)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return list, nil
}

// RemoveCustomImage deletes an image (refused while a container uses it)
// and its spec.
func RemoveCustomImage(name string) error {
	if !ValidImageName(name) {
		return fmt.Errorf("bad image name %q", name)
	}
	if out, _ := RunTO(15*time.Second, "docker", "ps", "-a", "--filter", "ancestor="+CustomImageRef(name),
		"--format", "{{.Names}}"); strings.TrimSpace(out) != "" {
		return fmt.Errorf("image %s is used by %s - remove those sandboxes first", name, strings.Join(strings.Fields(out), ", "))
	}
	if out, err := RunTO(60*time.Second, "docker", "image", "rm", CustomImageRef(name)); err != nil &&
		!strings.Contains(out, "No such image") {
		return fmt.Errorf("docker image rm: %w: %s", err, strings.TrimSpace(out))
	}
	_ = os.Remove(filepath.Join(ImageSpecDir(), name+".json"))
	audit.Record("image.rm", CustomImageRef(name), "removed")
	return nil
}
