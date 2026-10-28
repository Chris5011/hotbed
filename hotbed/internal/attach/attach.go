// Package attach manages persistent tmux sessions for sandboxes and
// pre-rolls every connection string variant (tmux, one-shot, Windows shell).
package attach

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"hotbed/internal/agents"
	"hotbed/internal/audit"
	"hotbed/internal/cfgsync"
	"hotbed/internal/engine"
	"hotbed/internal/env"
	"hotbed/internal/sandbox"
)

// Session name for a sandbox's persistent tmux session.
func SessionName(name string) string {
	return "hotbed-" + name
}

// MigrateSessions renames pre-v0.5 tmux sessions ("sbx-…") to the new design
// ("hotbed-…" + everything after the old prefix). Renaming keeps attachment
// state intact (an attached client stays attached, its title bar phrase
// updated), and without it a renamed daemon couldn't find/reuse/kills the
// persistent sessions it owns. Idempotent; tmux absent is a no-op.
func MigrateSessions() {
	if !TmuxPresent() {
		return
	}
	out, err := engine.Run("bash", "-c", "tmux ls -F '#S' 2>/dev/null || true")
	if err != nil || out == "" {
		return
	}
	for _, s := range strings.Split(strings.TrimSpace(out), "\n") {
		s = strings.TrimSpace(s)
		if !strings.HasPrefix(s, "sbx-") {
			continue
		}
		nn := "hotbed-" + strings.TrimPrefix(s, "sbx-")
		if HasSession(nn) {
			_, _ = engine.Run("tmux", "kill-session", "-t", s) // collision: stale legacy copy
			continue
		}
		_, _ = engine.Run("tmux", "rename-session", "-t", s, nn)
	}
}

// TmuxPresent checks the tmux CLI.
func TmuxPresent() bool { return engine.LookPath("tmux") }

// HasSession reports whether the session exists.
func HasSession(sess string) bool {
	out, err := engine.Run("bash", "-c", "tmux has-session -t "+shellQuote(sess)+" 2>/dev/null && echo yes || echo no")
	return err == nil && strings.Contains(out, "yes")
}

// KillSession removes a stale session (used when the sandbox was recreated).
func KillSession(sess string) { _, _ = engine.Run("tmux", "kill-session", "-t", sess) }

// EnsureSession idempotently creates a detached tmux session that keeps a
// live connection (opencode TUI or shell) into the sandbox.
func EnsureSession(sb sandbox.Sandbox, mode string, port string) (string, error) {
	if !TmuxPresent() {
		return "", fmt.Errorf("tmux not found (sudo apt install tmux)")
	}
	if rest, ok := strings.CutPrefix(mode, "agent:"); ok {
		id, resume := strings.CutSuffix(rest, ":resume")
		if id != "opencode" {
			return ensureAgentSession(sb, id, resume)
		}
		// the builtin module keeps its classic wiring (VMs too)
		mode = "opencode"
		if resume {
			mode = "opencode-resume"
		}
	}
	sess := SessionName(sb.Name)
	if HasSession(sess) {
		// still alive? if the pane died the session dies with it — trust tmux.
		// Sessions only carry the env they were born with: when the synced
		// proxy target changed since creation (or the session predates sync
		// entirely), respawn shell-mode panes once so the attach env lands.
		// Never respawn over a running opencode TUI.
		if !sessionEnvMatched(sess, sb.Name) && rollSafe(sb, mode) {
			KillSession(sess)
			audit.Record("attach.roll", sb.Name, "shell session respawned (attach env out of sync with the synced proxy target)")
		} else {
			return sess, nil
		}
	}

	uid, gid := hostID()
	var inner string
	switch sb.Kind {
	case sandbox.KindContainer:
		xdg := ""
		// shell mode counts too: opencode started at the "Open Terminal"
		// prompt should use the synced proxy config (per-sandbox key), not
		// the bind-mounted direct provider — otherwise the burn ledger
		// stays empty by construction. tmux sessions roll on attach, so
		// the env lands with this attach.
		if mode == "opencode" || mode == "opencode-resume" || mode == "shell" {
			if tg, ok := cfgsync.Lookup(sb.Name); ok && tg.XDGOverride {
				xdgBase, _ := cnXdgValue(sb.Name)
				xdg = "-e XDG_CONFIG_HOME=" + xdgBase + " "
			}
			// sessions in the workspace: every OpenCode start must carry the
			// DB path (its background service keeps the first one it saw)
			xdg += opencodePersistFlags(sb)
		}
		switch mode {
		case "opencode":
			inner = fmt.Sprintf("docker exec -it -u %s:%s -w /workspace -e TERM=xterm-256color %s%s opencode", uid, gid, xdg, sb.Name)
		case "opencode-resume":
			inner = fmt.Sprintf("docker exec -it -u %s:%s -w /workspace -e TERM=xterm-256color %s%s opencode --continue", uid, gid, xdg, sb.Name)
		default:
			inner = fmt.Sprintf("docker exec -it -u %s:%s -w /workspace -e TERM=xterm-256color %s%s bash", uid, gid, xdg, sb.Name)
		}
	case sandbox.KindVM:
		ssh := SSHCommand(sb, port)
		if ssh == "" {
			return "", fmt.Errorf("no ssh endpoint known for %s yet (wait for the lease/port)", sb.Name)
		}
		switch mode {
		case "opencode":
			inner = ssh + " -- " + shellQuote(vmOpencode(""))
		case "opencode-resume":
			inner = ssh + " -- " + shellQuote(vmOpencode("--continue"))
		default:
			inner = ssh
		}
	default:
		return "", fmt.Errorf("unknown kind %q", sb.Kind)
	}

	outer := []string{"new-session", "-d", "-s", sess, "bash", "-c", inner}
	if out, err := engine.Run("tmux", outer...); err != nil {
		return "", fmt.Errorf("tmux new-session: %v (%s)", err, strings.TrimSpace(out))
	}
	markSession(sess, sb.Name)
	return sess, nil
}

// AgentSessionName is the tmux session of one agent module in a sandbox —
// separate from the shell/opencode session so agents run side by side.
func AgentSessionName(name, id string) string { return SessionName(name) + "-" + id }

// opencodePersistFlags returns docker -e flags pointing the builtin
// OpenCode's session DB into the workspace — only once sync created the
// state folder (a fresh or unmounted workspace keeps OpenCode's default).
func opencodePersistFlags(sb sandbox.Sandbox) string {
	m, err := agents.Get("opencode")
	if err != nil {
		return ""
	}
	env := agents.PersistEnv(m)
	if len(env) == 0 {
		return ""
	}
	if _, err := engine.Run("docker", "exec", sb.Name, "test", "-d", agents.StateDir(m.ID)); err != nil {
		return ""
	}
	var b strings.Builder
	for _, k := range sortedKeys(env) {
		b.WriteString("-e " + k + "=" + shellQuote(env[k]) + " ")
	}
	return b.String()
}

// vmOpencode is the guest-side OpenCode start: the persistent DB env is
// exported when the workspace state folder exists.
func vmOpencode(args string) string {
	cmd := "cd /workspace 2>/dev/null; "
	if m, err := agents.Get("opencode"); err == nil {
		env := agents.PersistEnv(m)
		if len(env) > 0 {
			cmd += "if [ -d " + shellQuote(agents.StateDir(m.ID)) + " ]; then export"
			for _, k := range sortedKeys(env) {
				cmd += " " + k + "=" + shellQuote(env[k])
			}
			cmd += "; fi; "
		}
	}
	return strings.TrimSpace(cmd + "opencode " + args)
}

// agentCommand is the docker exec line launching an agent module
// (resume: the manifest's continue-last-conversation command).
func agentCommand(sb sandbox.Sandbox, id string, resume bool) (string, error) {
	m, err := agents.Get(id)
	if err != nil {
		return "", err
	}
	switch sb.Kind {
	case sandbox.KindContainer:
		uid, gid := hostID()
		home := engine.ContainerHome(sb.Name)
		return fmt.Sprintf("docker exec -it -u %s:%s -w /workspace -e TERM=xterm-256color %s bash -lc %s",
			uid, gid, sb.Name, shellQuote(agents.LaunchScript(home, m, resume))), nil
	case sandbox.KindVM:
		ssh := SSHCommand(sb, "")
		if ssh == "" {
			return "", fmt.Errorf("no ssh endpoint known for %s yet (wait for the lease/port)", sb.Name)
		}
		// -t: the agent is a TUI; ssh would not allocate a pty for a command
		return strings.Replace(ssh, "ssh ", "ssh -t ", 1) + " -- " +
			shellQuote("bash -lc "+shellQuote(agents.LaunchScript(vmHome, m, resume))), nil
	}
	return "", fmt.Errorf("unknown sandbox kind %q", sb.Kind)
}

// ensureAgentSession starts (or reuses) the tmux session running an agent.
func ensureAgentSession(sb sandbox.Sandbox, id string, resume bool) (string, error) {
	inner, err := agentCommand(sb, id, resume)
	if err != nil {
		return "", err
	}
	sess := AgentSessionName(sb.Name, id)
	if HasSession(sess) {
		return sess, nil
	}
	if out, err := engine.Run("tmux", "new-session", "-d", "-s", sess, "bash", "-c", inner); err != nil {
		return "", fmt.Errorf("tmux new-session: %v (%s)", err, strings.TrimSpace(out))
	}
	return sess, nil
}

// Session env stamp: sessions carry the env they were created with. The
// stamp records the synced proxy target's mtime at creation ("none" when
// unsynced). On attach, a mismatch (target synced/updated after the pane
// was born, or a pre-marker session) tells the daemon to respawn the pane
// once — bare shells only — so the synced-proxy env actually lands.
func markSession(sess, sbName string) {
	_, _ = engine.Run("tmux", "set-environment", "-t", sess, "HBD_ENV", envStamp(sbName))
}

func envStamp(sbName string) string {
	if mt, ok := cfgsync.LastSync(sbName); ok {
		return strconv.FormatInt(mt.Unix(), 10)
	}
	return "none"
}

func sessionEnvMatched(sess, sbName string) bool {
	out, _ := engine.Run("tmux", "show-environment", "-t", sess, "HBD_ENV")
	return strings.TrimSpace(string(out)) == "HBD_ENV="+envStamp(sbName)
}

// rollSafe says whether respawning this pane may lose user work. Shell-mode
// container panes are plain prompts by design — they only carry the env they
// were born with, so they are respawned once to adopt synced-proxy env.
// Mode "opencode" panes and VMs are left alone: a VM's guest config is
// replaced by sync directly, and an opencode TUI is a user's active task.
func rollSafe(sb sandbox.Sandbox, mode string) bool {
	return sb.Kind == sandbox.KindContainer && mode == "shell" && !hasRunningOpencode(sb.Name)
}

// hasRunningOpencode reports whether an opencode process is alive inside the
// sandbox container — the common case of work in progress in a shell pane.
func hasRunningOpencode(name string) bool {
	out, _ := engine.Run("docker", "exec", name, "pgrep", "-x", "opencode")
	return strings.TrimSpace(out) != ""
}

// SSHCommand returns a ready-to-run ssh command into a VM sandbox.
// The collector pre-rolls sb.SSH as "SSH_CMD=<complete command>" — reuse it.
func SSHCommand(sb sandbox.Sandbox, portOverride string) string {
	if sb.Kind != sandbox.KindVM {
		return ""
	}
	if strings.HasPrefix(sb.SSH, "SSH_CMD=") {
		return strings.TrimPrefix(sb.SSH, "SSH_CMD=")
	}
	// fallback: resolve the endpoint on the fly
	key := os.Getenv("HOME") + "/.ssh/hotbedvm_key"
	if sb.Backend == sandbox.BackendDocker {
		p := portOverride
		if p == "" {
			p = engine.DockerPort(env.VMDockerPrefix + sb.Name)
		}
		if p == "" {
			return ""
		}
		return "ssh -i " + key + " -o StrictHostKeyChecking=accept-new -p " + p + " dev@127.0.0.1"
	}
	return ""
}

// ConnInfo rolls every connection variant for the UI/CLI.
func ConnInfo(sb sandbox.Sandbox, mode string, port string) sandbox.ConnInfo {
	if rest, ok := strings.CutPrefix(mode, "agent:"); ok {
		id, resume := strings.CutSuffix(rest, ":resume")
		if id != "opencode" {
			ci := sandbox.ConnInfo{AttachHTTP: "/api/sandboxes/" + sb.Kind + "/" + sb.Name + "/attach"}
			if inner, err := agentCommand(sb, id, resume); err == nil {
				ci.TmuxSession = AgentSessionName(sb.Name, id)
				ci.TmuxAttach = "tmux attach -t " + ci.TmuxSession
				ci.BackendCmd = inner
				ci.WTCommand = WTCommand(inner)
			}
			return ci
		}
		mode = "opencode"
	}
	ci := sandbox.ConnInfo{}
	uid, gid := hostID()
	sess := SessionName(sb.Name)
	attachWSL := "tmux attach -t " + sess
	ci.TmuxSession = sess
	ci.TmuxAttach = attachWSL

	var inner string
	switch sb.Kind {
	case sandbox.KindContainer:
		if mode == "" {
			mode = "shell"
		}
		// one-shot variants get the synced-proxy XDG override too, or the
		// Windows Terminal path would read the bind-mounted direct config.
		xdg := ""
		if tg, ok := cfgsync.Lookup(sb.Name); ok && tg.XDGOverride {
			xdgBase, _ := cnXdgValue(sb.Name)
			xdg = "-e XDG_CONFIG_HOME=" + xdgBase + " "
		}
		xdg += opencodePersistFlags(sb)
		ci.BackendCmd = fmt.Sprintf("docker exec -it -u %s:%s -w /workspace -e TERM=xterm-256color %s%s opencode", uid, gid, xdg, sb.Name)
		ci.ShellCmd = fmt.Sprintf("docker exec -it -u %s:%s -w /workspace -e TERM=xterm-256color %s%s bash", uid, gid, xdg, sb.Name)
		inner = "docker exec -it -u " + uid + ":" + gid + " -w /workspace -e TERM=xterm-256color " + xdg + sb.Name + " bash"
	case sandbox.KindVM:
		ssh := SSHCommand(sb, port)
		ci.ShellCmd = ssh
		inner = ssh
		if mode == "opencode" {
			ci.BackendCmd = ssh + " -- " + shellQuote(vmOpencode(""))
			inner = ci.BackendCmd
		}
	}
	if inner != "" {
		ci.WTCommand = WTCommand(inner)
	}
	ci.AttachHTTP = "/api/sandboxes/" + sb.Kind + "/" + sb.Name + "/attach"
	return ci
}

// WTCommand produces a command line that opens Windows Terminal (from any
// Windows shell) and attaches to the sandbox inside tmux.
func WTCommand(insideTmux string) string {
	distro := os.Getenv("WSL_DISTRO_NAME")
	if distro == "" {
		distro = "kali-linux"
	}
	return fmt.Sprintf("wt.exe new-tab --title hbd -- wsl.exe -d %s -- bash -lc %q",
		distro, insideTmux)
}

// SpawnTerminal launches the Windows Terminal tab that attaches to a session
// (best-effort: requires WSL interop and a Windows Terminal install).
func SpawnTerminal(inside string) error {
	distro := os.Getenv("WSL_DISTRO_NAME")
	if distro == "" {
		distro = "kali-linux"
	}
	if r := runtime.GOOS; r != "linux" {
		return fmt.Errorf("terminal spawn only supported on WSL (goos=%s)", r)
	}
	// wt.exe is on PATH via WSL interop — but the resident daemon runs from a
	// systemd unit whose PATH is minimal, and wt.exe is a per-user WindowsApps
	// shim anyway (not in System32). Probe absolute locations before giving
	// up: cmd.exe is always /mnt/c/Windows/System32/cmd.exe, wt.exe follows
	// the /mnt/c/Users/*/AppData/Local/Microsoft/WindowsApps layout.
	if engine.LookPath("wt.exe") {
		cmd := exec.Command("wt.exe", "new-tab", "--title", "hbd", "--",
			"wsl.exe", "-d", distro, "--", "bash", "-lc", inside)
		return cmd.Start()
	}
	for _, wt := range windowsAppsWT() {
		cmd := exec.Command(wt, "new-tab", "--title", "hbd", "--",
			"wsl.exe", "-d", distro, "--", "bash", "-lc", inside)
		return cmd.Start()
	}
	if engine.LookPath("cmd.exe") {
		cmd := exec.Command("cmd.exe", "/C", "start", "", "wsl.exe", "-d", distro, "--", "bash", "-lc", inside)
		return cmd.Start()
	}
	if _, err := os.Stat("/mnt/c/Windows/System32/cmd.exe"); err == nil {
		cmd := exec.Command("/mnt/c/Windows/System32/cmd.exe", "/C", "start", "", "wsl.exe",
			"-d", distro, "--", "bash", "-lc", inside)
		return cmd.Start()
	}
	return fmt.Errorf("no wt.exe / cmd.exe interop available — use the copy-connection command instead")
}

// windowsAppsWT returns existing wt.exe paths for every Windows user profile
// mounted under /mnt/c/Users (empty when astro-turfed: no Windows mount).
func windowsAppsWT() []string {
	out := []string{}
	if m, _ := filepath.Glob("/mnt/c/Users/*/AppData/Local/Microsoft/WindowsApps/wt.exe"); m != nil {
		out = append(out, m...)
	}
	return out
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func hostID() (uid, gid string) {
	if u, err := user.Current(); err == nil {
		return u.Uid, u.Gid
	}
	return "1000", "1000"
}

// --- write transports (used by cfgsync) -------------------------------------

// Kit layout paths mirrored from bin/hbd (container) and hotbedvm provisioning (VM).
// Container paths are computed per container below: v0.4 images hold the
// "sbx" user (/home/sbx), v0.5+ images "hbd" (/home/hbd) — same FILE layout
// under either base, so legacy sandboxes keep their synced proxy config.
func cnXdgValue(container string) (xdgValue, cfgPath string) {
	base := engine.ContainerHome(container)
	return base + "/.config/opencode-sbx",
		base + "/.config/opencode-sbx/opencode/opencode.json"
}

const (
	cfgsyncVMPath = "/home/dev/.config/opencode/opencode.json"
)

// WriteTransport returns a file-writer into the sandbox plus the canonical
// opencode config path for its kind.
func WriteTransport(sb sandbox.Sandbox, port string) (cfgsync.Transport, string, error) {
	uid, gid := hostID()
	switch sb.Kind {
	case sandbox.KindContainer:
		_, path := cnXdgValue(sb.Name)
		return containerWriter(sb.Name, uid, gid), path, nil
	case sandbox.KindVM:
		ssh := SSHCommand(sb, port)
		if ssh == "" {
			return nil, "", fmt.Errorf("no ssh endpoint known for %s (is it running?)", sb.Name)
		}
		return sshWriter(ssh), cfgsyncVMPath, nil
	}
	return nil, "", fmt.Errorf("unknown kind %q", sb.Kind)
}

type writerFuncT func(path string, payload []byte) error

func (f writerFuncT) WriteRemote(path string, payload []byte) error { return f(path, payload) }

func remoteTee(bin string, fields []string, path string, payload []byte, to time.Duration) error {
	script := "mkdir -p \"$(dirname " + shellQuote(path) + ")\" && cat > " + shellQuote(path) + " && echo synced"
	out, err := engine.RunStdin(to, payload, bin, append(fields, "bash", "-c", script)...)
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(out))
	}
	return nil
}

func containerWriter(cn, uid, gid string) cfgsync.Transport {
	return writerFuncT(func(path string, payload []byte) error {
		return remoteTee("docker", []string{"exec", "-i", "-u", uid + ":" + gid, cn}, path, payload, 30*time.Second)
	})
}

func sshWriter(sshCmd string) cfgsync.Transport {
	f := strings.Fields(sshCmd)
	return writerFuncT(func(path string, payload []byte) error {
		if len(f) < 2 {
			return fmt.Errorf("malformed ssh command")
		}
		// ssh joins argv into one remote command line; quoting the script
		// keeps the remote `bash -c` receiving it as exactly one argument.
		args := append(append([]string{}, f[1:]...), "bash", "-c", shellQuote(remoteTeeScript(path)))
		return remoteTeeSSH(f[0], args, payload)
	})
}

func remoteTeeScript(path string) string {
	return "mkdir -p \"$(dirname " + shellQuote(path) + ")\" && cat > " + shellQuote(path) + " && echo synced"
}

// --- proxy reachability --------------------------------------------------------

// TunnelPIDFile remembers the ssh -R tunnel per sandbox
// (~/.local/state/hotbed/tunnel-<sandbox>.pid).
func TunnelPIDFile(sandbox string) string {
	return filepath.Join(env.StateDir(), "tunnel-"+env.SafeName(sandbox)+".pid")
}

// EnsureTunnel keeps a persistent ssh remote-forward alive that pins the
// sandbox's loopback <port> onto the host proxy port (guest 127.0.0.1:port
// → host 127.0.0.1:port). VMs have no other reliable path to the proxy in
// Docker-Desktop WSL setups. Returns the ssh process PID (spawned or kept).
func EnsureTunnel(sb sandbox.Sandbox, proxyPort string) (int, string, error) {
	if sb.Kind != sandbox.KindVM {
		return 0, "", fmt.Errorf("tunnel only applies to VMs")
	}
	ssh := SSHCommand(sb, "")
	if ssh == "" {
		return 0, "", fmt.Errorf("no ssh endpoint known for %s", sb.Name)
	}
	pidFile := TunnelPIDFile(sb.Name)
	// reuse a still-alive tunnel
	if b, err := os.ReadFile(pidFile); err == nil {
		if pid, e := strconv.Atoi(strings.TrimSpace(string(b))); e == nil && pid > 0 && procAlive(pid) {
			return pid, pidFile, nil
		}
		_ = os.Remove(pidFile)
	}
	f := strings.Fields(ssh)
	args := append([]string{}, f[1:]...)
	args = append(args,
		"-o", "ExitOnForwardFailure=yes",
		"-o", "ServerAliveInterval=25",
		"-o", "ServerAliveCountMax=3",
		"-N",
		"-R", "127.0.0.1:"+proxyPort+":127.0.0.1:"+proxyPort,
	)
	if err := os.MkdirAll(env.StateDir(), 0o700); err != nil {
		return 0, "", err
	}
	log, err := os.OpenFile(filepath.Join(env.StateDir(), "tunnel-"+env.SafeName(sb.Name)+".log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return 0, "", err
	}
	_ = os.MkdirAll(filepath.Dir(pidFile), 0o700)
	cmd := exec.Command(f[0], args...)
	if log != nil {
		cmd.Stdout, cmd.Stderr = log, log
	}
	if err := cmd.Start(); err != nil {
		return 0, "", err
	}
	pid := cmd.Process.Pid
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(pid)), 0o600); err != nil {
		return 0, "", err
	}
	return pid, pidFile, nil
}

// TunnelAlive reports whether the persisted tunnel process still exists.
func TunnelAlive(sandbox string) bool {
	b, err := os.ReadFile(TunnelPIDFile(sandbox))
	if err != nil {
		return false
	}
	pid, e := strconv.Atoi(strings.TrimSpace(string(b)))
	return e == nil && pid > 0 && procAlive(pid)
}

// KillTunnel stops a sandbox's persisted ssh tunnel (if it belongs to us
// and is still running) and drops its pid/log leftovers. The kill is
// sanity-checked against the process cmdline (must be an ssh holding the
// -R forward); a recycled pid is never signalled.
func KillTunnel(sandbox string) {
	b, _ := os.ReadFile(TunnelPIDFile(sandbox))
	if b != nil {
		if pid, e := strconv.Atoi(strings.TrimSpace(string(b))); e == nil && pid > 0 && procAlive(pid) {
			if cl, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid)); err == nil &&
				strings.Contains(string(cl), "ssh") && strings.Contains(string(cl), "-R") {
				_ = exec.Command("kill", strconv.Itoa(pid)).Run()
			}
		}
	}
	name := env.SafeName(sandbox)
	_ = os.Remove(filepath.Join(env.StateDir(), "tunnel-"+name+".pid"))
	_ = os.Remove(filepath.Join(env.StateDir(), "tunnel-"+name+".log"))
}

// SweepStaleTunnels removes tunnel pid/log files whose process is gone
// (rm'd sandboxes, suite daemons, crash leftovers). A live pid is left
// alone - a still-running tunnel keeps working after its file would go.
func SweepStaleTunnels() {
	entries, err := os.ReadDir(env.StateDir())
	if err != nil {
		return
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "tunnel-") || !strings.HasSuffix(e.Name(), ".pid") {
			continue
		}
		name := strings.TrimSuffix(strings.TrimPrefix(e.Name(), "tunnel-"), ".pid")
		b, err := os.ReadFile(TunnelPIDFile(name))
		if err != nil {
			continue
		}
		pid, e2 := strconv.Atoi(strings.TrimSpace(string(b)))
		if e2 != nil || pid <= 0 || !procAlive(pid) {
			_ = os.Remove(TunnelPIDFile(name))
			_ = os.Remove(filepath.Join(env.StateDir(), "tunnel-"+name+".log"))
		}
	}
}

// ProbeReach checks whether the sandbox itself can open a TCP connection to
// host:port — used to pick a reachable base URL for containers (and to
// verify VM tunnels). Docker Desktop's WSL setup blocks normal host routes,
// so sync probes a candidate list instead of trusting one guess.
func ProbeReach(sb sandbox.Sandbox, host, port string) error {
	script := fmt.Sprintf(
		`timeout 4 bash -c "</dev/tcp/%s/%s" >/dev/null 2>&1 && echo REACHABLE`,
		shellQuote(host), shellQuote(port))
	switch sb.Kind {
	case sandbox.KindContainer:
		out, err := engine.Run("docker", "exec", sb.Name, "bash", "-c", script)
		if err != nil || !strings.Contains(out, "REACHABLE") {
			return fmt.Errorf("%s unreachable from sandbox %s", host, sb.Name)
		}
		return nil
	case sandbox.KindVM:
		ssh := SSHCommand(sb, "")
		if ssh == "" {
			return fmt.Errorf("no ssh endpoint known for %s", sb.Name)
		}
		f := strings.Fields(ssh)
		args := append(append([]string{}, f[1:]...), "bash", "-c", shellQuote(script))
		out, err := engine.Run(f[0], args...)
		if err != nil || !strings.Contains(out, "REACHABLE") {
			return fmt.Errorf("%s unreachable from sandbox %s", host, sb.Name)
		}
		return nil
	}
	return fmt.Errorf("unknown kind %q", sb.Kind)
}

func procAlive(pid int) bool {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	return err == nil && len(b) > 0
}

// ResolveProxyURL decides which proxy base URL a sandbox can actually reach
// and returns (baseURL, via, err):
//   - VMs: a persistent ssh -R tunnel (guest loopback → host proxy); verified
//     live across hotbedvmd-in-docker and libvirt backends. Persisted PID so a
//     daemon restart can re-ensure it (hotbedd StartTunnels).
//   - containers: probed candidates in order — WSL eth0 IP, then
//     host.docker.internal (published-port path) — first reachable wins.
func ResolveProxyURL(sb sandbox.Sandbox, proxyPort string) (string, string, error) {
	if sb.Kind == sandbox.KindVM {
		if _, _, err := EnsureTunnel(sb, proxyPort); err != nil {
			return "", "", fmt.Errorf("tunnel for %s: %w", sb.Name, err)
		}
		return "http://127.0.0.1:" + proxyPort + "/v1", "ssh tunnel (guest loopback)", nil
	}
	var lastErr error
	lastVia := ""
	for _, host := range []string{cfgsync.HostIP(), "host.docker.internal"} {
		if host == "" {
			continue
		}
		if err := ProbeReach(sb, host, proxyPort); err == nil {
			if host == "host.docker.internal" {
				lastVia = "host.docker.internal (published port)"
			} else {
				lastVia = "direct (WSL eth0 IP)"
			}
			return "http://" + host + ":" + proxyPort + "/v1", lastVia, nil
		} else {
			lastErr = err
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no reachable route")
	}
	// Docker Desktop's engine runs in its own VM whose containers cannot
	// reach this distro — spawn (or reuse) the in-engine proxy relay and
	// route through the engine's bridge gateway instead. Every declined or
	// failed attempt is audited so "why no relay?" is always answerable.
	if sb.Kind == sandbox.KindContainer {
		switch {
		case env.RelayDisabled():
			audit.Record("relay.skip", sb.Name, "disabled (HOTBEDD_RELAY_DISABLE=1)")
		case !engine.IsDockerDesktop():
			audit.Record("relay.skip", sb.Name, "engine not detected as Docker Desktop — no in-engine relay")
		default:
			if gw, e := engine.EnsureRelay(proxyPort); e != nil {
				audit.Record("relay.skip", sb.Name, "EnsureRelay: "+e.Error())
			} else if pr := ProbeReach(sb, gw, proxyPort); pr != nil {
				audit.Record("relay.skip", sb.Name, "relay up but "+gw+":"+proxyPort+" unreachable from sandbox: "+pr.Error())
			} else {
				return fmt.Sprintf("http://%s:%s/v1", gw, proxyPort),
					"hotbedd proxy relay (in-engine, " + gw + ")", nil
			}
		}
	}
	return "", "", fmt.Errorf("proxy unreachable from container %s (%v) — options: run the sandbox with HBD_NET=host, or a bridge-setup WSL docker (non-Docker-Desktop)", sb.Name, lastErr)
}

// remoteTeeSSH pipes payload into a pre-built ssh argument vector.
func remoteTeeSSH(bin string, args []string, payload []byte) error {
	out, err := engine.RunStdin(40*time.Second, payload, bin, args...)
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(out))
	}
	return nil
}
