// Package ide opens hotbed sandboxes in VS Code (issue #2).
//
//	containers  Dev Containers "attach to running container": the folder
//	            URI vscode-remote://attached-container+<hex json>/workspace
//	            (needs the ms-vscode-remote.remote-containers extension;
//	            hotbed only reports it missing, it never installs it)
//	VMs         Remote-SSH to the ssh host alias hotbed-<name>, defined in
//	            hotbed's own ssh config file hotbed.conf, which the user's
//	            ~/.ssh/config pulls in with one Include line. The connection
//	            rides `hotbedd ide-pipe NAME` as ProxyCommand (stdio <->
//	            guest:22), so it follows lease changes (strict egress) and
//	            works from Windows, where guest addresses do not route.
//
// On WSL the editor is the Windows VS Code: hotbed generates a separate
// key %USERPROFILE%\.ssh\hotbed_ide with the Windows ssh-keygen and
// authorizes it in the guest — the VM key never leaves the distro. On a
// native Linux desktop the kit's VM key is used directly. The guest's host
// key is read over the kit's own ssh channel and pinned in
// hotbed_known_hosts (StrictHostKeyChecking yes).
package ide

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"hotbed/internal/agents"
	"hotbed/internal/engine"
	"hotbed/internal/env"
	"hotbed/internal/sandbox"
)

// Extension ids VS Code needs per sandbox kind.
const (
	ExtContainers = "ms-vscode-remote.remote-containers"
	ExtSSH        = "ms-vscode-remote.remote-ssh"
)

// Folder is what VS Code opens inside every sandbox.
const Folder = "/workspace"

// HostAlias is a VM's ssh host name in hotbed.conf.
func HostAlias(vm string) string { return "hotbed-" + vm }

// ContainerURI is the Dev Containers attach URI for a running container.
func ContainerURI(container string) string {
	j := fmt.Sprintf(`{"containerName":"/%s"}`, container)
	return "vscode-remote://attached-container+" + hex.EncodeToString([]byte(j)) + Folder
}

// SSHURI is the Remote-SSH folder URI for a VM.
func SSHURI(vm string) string { return "vscode-remote://ssh-remote+" + HostAlias(vm) + Folder }

// InstallHint is the command that adds a missing extension.
func InstallHint(ext string) string { return "code --install-extension " + ext }

// ------------------------------------------------------------ platform ---

const cmdExe = "/mnt/c/Windows/System32/cmd.exe"

// OnWSL reports whether the editor side is Windows (WSL interop present).
func OnWSL() bool {
	if _, err := os.Stat("/proc/sys/fs/binfmt_misc/WSLInterop"); err != nil {
		if _, err := os.Stat("/proc/sys/fs/binfmt_misc/WSLInterop-late"); err != nil {
			return false
		}
	}
	_, err := os.Stat(cmdExe)
	return err == nil
}

// win runs a Windows command line through cmd.exe (cwd C:\ — cmd refuses
// UNC working directories) and returns its output without CRs.
func win(to time.Duration, args ...string) (string, error) {
	c := exec.Command(cmdExe, append([]string{"/c"}, args...)...)
	c.Dir = "/mnt/c"
	out, err := runTO(c, to)
	return strings.ReplaceAll(out, "\r", ""), err
}

func runTO(c *exec.Cmd, to time.Duration) (string, error) {
	c.Stdin = nil
	done := make(chan struct{})
	var out []byte
	var err error
	go func() { out, err = c.CombinedOutput(); close(done) }()
	select {
	case <-done:
		return string(out), err
	case <-time.After(to):
		if c.Process != nil {
			_ = c.Process.Kill()
		}
		return string(out), fmt.Errorf("%s: timed out after %s", filepath.Base(c.Path), to)
	}
}

// code runs the VS Code CLI on the editor side.
func code(to time.Duration, args ...string) (string, error) {
	if OnWSL() {
		return win(to, append([]string{"code"}, args...)...)
	}
	if _, err := exec.LookPath("code"); err != nil {
		return "", fmt.Errorf("VS Code CLI `code` not found on PATH")
	}
	return runTO(exec.Command("code", args...), to)
}

// MissingExtension returns ext when the editor lacks it, "" otherwise (or
// when the extension list cannot be read — then VS Code reports it).
func MissingExtension(ext string) string {
	out, err := code(30*time.Second, "--list-extensions")
	if err != nil {
		return ""
	}
	for _, l := range strings.Split(out, "\n") {
		if strings.EqualFold(strings.TrimSpace(l), ext) {
			return ""
		}
	}
	return ext
}

// Launch opens a folder URI in VS Code (returns once the CLI handed off).
func Launch(uri string) error {
	out, err := code(45*time.Second, "--folder-uri", uri)
	if err != nil {
		return fmt.Errorf("launching VS Code: %w: %s", err, strings.TrimSpace(out))
	}
	return nil
}

// ------------------------------------------------------------- ssh side ---

// sshSide describes the editor's ssh client: its ~/.ssh (as a local path
// we can write), the identity to use and how ProxyCommand reaches us.
type sshSide struct {
	dir      string // ~/.ssh, local path
	identity string // IdentityFile as the editor's ssh sees it
	pub      string // public key to authorize in the guest
	proxy    string // ProxyCommand prefix (+ " NAME")
}

func editorSSH() (sshSide, error) {
	self, err := os.Executable()
	if err != nil {
		return sshSide{}, err
	}
	if !OnWSL() {
		home, _ := os.UserHomeDir()
		key := filepath.Join(home, ".ssh", "hotbedvm_key")
		pub, err := os.ReadFile(key + ".pub")
		if err != nil {
			return sshSide{}, fmt.Errorf("kit VM key %s.pub: %w", key, err)
		}
		return sshSide{dir: filepath.Join(home, ".ssh"), identity: key, pub: strings.TrimSpace(string(pub)),
			proxy: self + " ide-pipe"}, nil
	}
	prof, err := win(20*time.Second, "echo", "%USERPROFILE%")
	prof = strings.TrimSpace(prof)
	if err != nil || prof == "" || strings.Contains(prof, "%") {
		return sshSide{}, fmt.Errorf("windows profile dir: %v %s", err, prof)
	}
	lp, err := exec.Command("wslpath", "-u", prof).Output()
	if err != nil {
		return sshSide{}, fmt.Errorf("wslpath %s: %w", prof, err)
	}
	dir := filepath.Join(strings.TrimSpace(string(lp)), ".ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return sshSide{}, err
	}
	key := filepath.Join(dir, "hotbed_ide")
	if _, err := os.Stat(key); err != nil {
		// generated by the Windows ssh-keygen so the file gets the ACL
		// Windows OpenSSH insists on (owner-only)
		kg := exec.Command("/mnt/c/Windows/System32/OpenSSH/ssh-keygen.exe",
			"-q", "-t", "ed25519", "-N", "", "-C", "hotbed-ide", "-f", prof+`\.ssh\hotbed_ide`)
		kg.Dir = "/mnt/c"
		if out, err := runTO(kg, 30*time.Second); err != nil {
			return sshSide{}, fmt.Errorf("ssh-keygen (Windows): %w: %s", err, strings.TrimSpace(out))
		}
	}
	pub, err := os.ReadFile(key + ".pub")
	if err != nil {
		return sshSide{}, err
	}
	return sshSide{
		dir:      dir,
		identity: "~/.ssh/hotbed_ide",
		pub:      strings.TrimSpace(string(pub)),
		proxy:    `C:\Windows\System32\wsl.exe -d ` + distro() + " -e " + self + " ide-pipe",
	}, nil
}

// distro names this WSL distribution (the systemd unit has no
// WSL_DISTRO_NAME; wslpath -w / prints \\wsl.localhost\<distro>\).
func distro() string {
	if d := os.Getenv("WSL_DISTRO_NAME"); d != "" {
		return d
	}
	if out, err := exec.Command("wslpath", "-w", "/").Output(); err == nil {
		f := strings.Split(strings.Trim(strings.TrimSpace(string(out)), `\`), `\`)
		if len(f) >= 2 {
			return f[len(f)-1]
		}
	}
	return "kali-linux"
}

// SetupVM makes Remote-SSH reach a VM: identity authorized in the guest,
// host key pinned, host block in hotbed.conf, Include line in the user's
// ssh config. Returns the host alias. b is the kit's ssh channel into it.
func SetupVM(vm string, b agents.Box) (string, error) {
	side, err := editorSSH()
	if err != nil {
		return "", err
	}
	q := shq(side.pub)
	if out, err := b.RunUser(`set -e; mkdir -p ~/.ssh; chmod 700 ~/.ssh; touch ~/.ssh/authorized_keys; chmod 600 ~/.ssh/authorized_keys
grep -qxF `+q+` ~/.ssh/authorized_keys || echo `+q+` >> ~/.ssh/authorized_keys`, 30*time.Second); err != nil {
		return "", fmt.Errorf("authorizing the editor key in %s: %w: %s", vm, err, strings.TrimSpace(out))
	}
	hk, err := b.RunUser(`cat /etc/ssh/ssh_host_ed25519_key.pub`, 20*time.Second)
	f := strings.Fields(lastLine(hk))
	if err != nil || len(f) < 2 || !strings.HasPrefix(f[0], "ssh-") {
		return "", fmt.Errorf("reading %s's host key: %v %s", vm, err, strings.TrimSpace(hk))
	}
	alias := HostAlias(vm)
	if err := replaceBlock(filepath.Join(side.dir, "hotbed_known_hosts"), alias, alias+" "+f[0]+" "+f[1]+"\n"); err != nil {
		return "", err
	}
	block := fmt.Sprintf("Host %s\n  HostName %s\n  User dev\n  IdentityFile %s\n  IdentitiesOnly yes\n"+
		"  UserKnownHostsFile ~/.ssh/hotbed_known_hosts\n  HostKeyAlias %s\n  StrictHostKeyChecking yes\n"+
		"  ProxyCommand %s %s\n", alias, vm, side.identity, alias, side.proxy, vm)
	if err := replaceBlock(filepath.Join(side.dir, "hotbed.conf"), alias, block); err != nil {
		return "", err
	}
	if err := ensureInclude(filepath.Join(side.dir, "config")); err != nil {
		return "", err
	}
	return alias, nil
}

// ForgetVM drops a deleted VM's host block and pinned host key.
func ForgetVM(vm string) {
	side := sshDirOnly()
	if side == "" {
		return
	}
	_ = replaceBlock(filepath.Join(side, "hotbed.conf"), HostAlias(vm), "")
	_ = replaceBlock(filepath.Join(side, "hotbed_known_hosts"), HostAlias(vm), "")
}

// sshDirOnly locates the editor's ~/.ssh without creating keys ("" =
// never set up).
func sshDirOnly() string {
	if !OnWSL() {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".ssh")
	}
	prof, err := win(20*time.Second, "echo", "%USERPROFILE%")
	if err != nil {
		return ""
	}
	lp, err := exec.Command("wslpath", "-u", strings.TrimSpace(prof)).Output()
	if err != nil {
		return ""
	}
	d := filepath.Join(strings.TrimSpace(string(lp)), ".ssh")
	if _, err := os.Stat(filepath.Join(d, "hotbed.conf")); err != nil {
		return ""
	}
	return d
}

// replaceBlock rewrites the "# hotbed:<key>" section of a generated file
// (content "" removes it), keeping every other section.
func replaceBlock(path, key, content string) error {
	begin, end := "# hotbed:"+key+" begin", "# hotbed:"+key+" end"
	cur, _ := os.ReadFile(path)
	var out []string
	skip := false
	for _, l := range strings.Split(strings.ReplaceAll(string(cur), "\r\n", "\n"), "\n") {
		switch {
		case l == begin:
			skip = true
		case l == end:
			skip = false
		case !skip:
			out = append(out, l)
		}
	}
	s := strings.TrimRight(strings.Join(out, "\n"), "\n")
	if s == "" {
		s = "# generated by hotbedd ide - edits inside hotbed:* sections are overwritten"
	}
	s += "\n"
	if content != "" {
		s += "\n" + begin + "\n" + strings.TrimRight(content, "\n") + "\n" + end + "\n"
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(s), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

const includeLine = "Include hotbed.conf"

// ensureInclude puts the Include line at the top of the user's ssh config
// (Include must precede the Host blocks to apply globally). The original
// is backed up once to config.hotbed-bak; nothing else changes.
func ensureInclude(path string) error {
	cur, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, l := range strings.Split(strings.ReplaceAll(string(cur), "\r\n", "\n"), "\n") {
		if strings.EqualFold(strings.TrimSpace(l), includeLine) {
			return nil
		}
	}
	if len(cur) > 0 {
		if _, err := os.Stat(path + ".hotbed-bak"); os.IsNotExist(err) {
			if err := os.WriteFile(path+".hotbed-bak", cur, 0o600); err != nil {
				return err
			}
		}
	}
	bom := ""
	body := string(cur)
	if strings.HasPrefix(body, "\ufeff") {
		bom, body = "\ufeff", strings.TrimPrefix(body, "\ufeff")
	}
	nl := "\n"
	if strings.Contains(body, "\r\n") {
		nl = "\r\n"
	}
	head := bom + "# hotbed sandboxes (VS Code Remote-SSH) - see hotbed.conf" + nl + includeLine + nl + nl
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(head+body), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ---------------------------------------------------------------- pipe ---

// PipeTarget is where `hotbedd ide-pipe NAME` connects: the guest's sshd.
func PipeTarget(vm string) (string, error) {
	if engine.LibvirtUp() {
		if ip := engine.LibvirtLease(env.VMStdPrefix + vm); ip != "" {
			return ip + ":22", nil
		}
	}
	if p := engine.DockerPort(env.VMDockerPrefix + vm); p != "" {
		return "127.0.0.1:" + p, nil
	}
	return "", fmt.Errorf("VM %s: no ssh endpoint (not running?)", vm)
}

func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		return s[i+1:]
	}
	return s
}

// ---------------------------------------------------------------- open ---

// Result is what an open request reports back (CLI + dashboard).
type Result struct {
	URI      string `json:"uri"`
	Host     string `json:"ssh_host,omitempty"` // VMs: the Remote-SSH host alias
	Missing  string `json:"missing_extension,omitempty"`
	Hint     string `json:"install_hint,omitempty"`
	Launched bool   `json:"launched"`
}

// Open prepares a running sandbox for VS Code and, with launch, opens it.
// A missing extension is reported (with its install command), never
// installed; the URI is returned either way.
// env is the container's shell environment (attach.ContainerShellEnv).
func Open(sb sandbox.Sandbox, boxFor func() (agents.Box, error), env map[string]string, launch bool) (Result, error) {
	if sb.State != "running" {
		return Result{}, fmt.Errorf("%s is %s - start it first", sb.Name, sb.State)
	}
	var r Result
	ext := ExtContainers
	switch sb.Kind {
	case sandbox.KindContainer:
		r.URI = ContainerURI(sb.Name)
		if err := EnsureAttachConfig(sb.Name, env); err != nil {
			return r, fmt.Errorf("VS Code attach config: %w", err)
		}
	case sandbox.KindVM:
		b, err := boxFor()
		if err != nil {
			return r, err
		}
		host, err := SetupVM(sb.Name, b)
		if err != nil {
			return r, err
		}
		r.URI, r.Host, ext = SSHURI(sb.Name), host, ExtSSH
	default:
		return r, fmt.Errorf("unknown sandbox kind %q", sb.Kind)
	}
	if r.Missing = MissingExtension(ext); r.Missing != "" {
		r.Hint = InstallHint(r.Missing)
		return r, nil
	}
	if launch {
		if err := Launch(r.URI); err != nil {
			return r, err
		}
		r.Launched = true
	}
	return r, nil
}

// ------------------------------------------------- dev containers config ---

// attachUser is the container's sandbox user (HBD_UID's passwd name): the
// images default to root - the entrypoint drops privileges only for its own
// process - so Dev Containers would attach as root, without the agents'
// hotbed config in that user's home.
func attachUser(container string) (string, error) {
	out, err := engine.RunTO(15*time.Second, "docker", "exec", container, "sh", "-c",
		`getent passwd "${HBD_UID:-1000}" | cut -d: -f1`)
	u := strings.TrimSpace(out)
	if err != nil || u == "" || strings.ContainsAny(u, " \t\n/") {
		return "", fmt.Errorf("sandbox user of %s: %v %s", container, err, u)
	}
	return u, nil
}

// devContainersDir is the extension's storage folder on the editor side.
func devContainersDir() (string, error) {
	if !OnWSL() {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".config", "Code", "User", "globalStorage", "ms-vscode-remote.remote-containers"), nil
	}
	ad, err := win(20*time.Second, "echo", "%APPDATA%")
	ad = strings.TrimSpace(ad)
	if err != nil || ad == "" || strings.Contains(ad, "%") {
		return "", fmt.Errorf("windows APPDATA: %v %s", err, ad)
	}
	lp, err := exec.Command("wslpath", "-u", ad).Output()
	if err != nil {
		return "", err
	}
	return filepath.Join(strings.TrimSpace(string(lp)), "Code", "User", "globalStorage", "ms-vscode-remote.remote-containers"), nil
}

// devContainerName encodes a container name the way the extension names
// its per-container files (":", "/", "%" percent-encoded, lowercase hex).
func devContainerName(n string) string {
	var b strings.Builder
	for _, r := range strings.TrimPrefix(n, "/") {
		if r == ':' || r == '/' || r == '%' {
			fmt.Fprintf(&b, "%%%x", r)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// EnsureAttachConfig writes the Dev Containers per-container ("named")
// configuration - looked up before the per-image one - so VS Code attaches
// as the sandbox user and opens /workspace. Other keys in an existing file
// (the user's own settings) are kept.
func EnsureAttachConfig(container string, env map[string]string) error {
	user, err := attachUser(container)
	if err != nil {
		return err
	}
	dir, err := devContainersDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dir, "nameConfigs"), 0o755); err != nil {
		return err
	}
	p := filepath.Join(dir, "nameConfigs", devContainerName(container)+".json")
	cfg := map[string]any{}
	if b, err := os.ReadFile(p); err == nil {
		if json.Unmarshal(b, &cfg) != nil {
			return fmt.Errorf("%s is not plain JSON - leaving it alone (set \"remoteUser\": %q there)", p, user)
		}
	}
	cfg["remoteUser"], cfg["workspaceFolder"] = user, Folder
	// remoteEnv: ours set/cleared, the user's other entries kept
	re, _ := cfg["remoteEnv"].(map[string]any)
	if re == nil {
		re = map[string]any{}
	}
	for _, k := range []string{"XDG_CONFIG_HOME", "OPENCODE_DB"} {
		delete(re, k)
	}
	for k, v := range env {
		re[k] = v
	}
	if len(re) > 0 {
		cfg["remoteEnv"] = re
	} else {
		delete(cfg, "remoteEnv")
	}
	b, _ := json.MarshalIndent(cfg, "", "\t")
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// ForgetContainer drops a deleted container's named configuration.
func ForgetContainer(container string) {
	if dir, err := devContainersDir(); err == nil {
		_ = os.Remove(filepath.Join(dir, "nameConfigs", devContainerName(container)+".json"))
	}
}
