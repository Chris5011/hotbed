package attach

// agents.Box implementations: where agent modules get installed, wired
// and launched. Containers go through docker exec; VM guests of both
// backends (libvirt hotbedvm-*, docker hotbedvmd-*) through the kit's ssh
// channel as the guest user "dev" (passwordless sudo, same as the kit's
// own opencode staging).

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"hotbed/internal/agents"
	"hotbed/internal/engine"
	"hotbed/internal/sandbox"
)

const vmHome = "/home/dev"

// BoxFor returns the agent box of a sandbox.
func BoxFor(sb sandbox.Sandbox, port string) (agents.Box, error) {
	switch sb.Kind {
	case sandbox.KindContainer:
		uid, gid := hostID()
		return &cnBox{name: sb.Name, home: engine.ContainerHome(sb.Name), uid: uid, gid: gid}, nil
	case sandbox.KindVM:
		ssh := SSHCommand(sb, port)
		if ssh == "" {
			return nil, fmt.Errorf("no ssh endpoint known for %s (is it running?)", sb.Name)
		}
		return &vmBox{name: sb.Name, ssh: batchSSH(strings.Fields(ssh))}, nil
	}
	return nil, fmt.Errorf("unknown sandbox kind %q", sb.Kind)
}

type cnBox struct{ name, home, uid, gid string }

func (b *cnBox) Name() string { return b.name }
func (b *cnBox) Home() string { return b.home }

func (b *cnBox) RunRoot(script []byte, env map[string]string, to time.Duration) (string, error) {
	args := []string{"exec", "-i", "-u", "root"}
	for _, k := range sortedKeys(env) {
		args = append(args, "-e", k+"="+env[k])
	}
	return engine.RunStdin(to, script, "docker", append(args, b.name, "bash", "-s")...)
}

func (b *cnBox) RunUser(script string, to time.Duration) (string, error) {
	return engine.RunTO(to, "docker", "exec", "-u", b.uid+":"+b.gid, b.name, "bash", "-lc", script)
}

func (b *cnBox) Write(path string, data []byte) error {
	return containerWriter(b.name, b.uid, b.gid).WriteRemote(path, data)
}

type vmBox struct {
	name string
	ssh  []string // ssh argv up to and including the destination
}

func (b *vmBox) Name() string { return b.name }
func (b *vmBox) Home() string { return vmHome }

// ssh joins the remote argv into one command line: each piece is quoted.
func (b *vmBox) remote(cmd string, stdin []byte, to time.Duration) (string, error) {
	args := append(append([]string{}, b.ssh[1:]...), cmd)
	if stdin != nil {
		return engine.RunStdin(to, stdin, b.ssh[0], args...)
	}
	return engine.RunTO(to, b.ssh[0], args...)
}

func (b *vmBox) RunRoot(script []byte, env map[string]string, to time.Duration) (string, error) {
	cmd := "sudo env"
	for _, k := range sortedKeys(env) {
		cmd += " " + k + "=" + shellQuote(env[k])
	}
	return b.remote(cmd+" bash -s", script, to)
}

func (b *vmBox) RunUser(script string, to time.Duration) (string, error) {
	return b.remote("bash -lc "+shellQuote(script), nil, to)
}

func (b *vmBox) Write(path string, data []byte) error {
	return sshWriter(strings.Join(b.ssh, " ")).WriteRemote(path, data)
}

// batchSSH hardens a kit ssh argv for unattended runs: never prompt, fail
// fast on a dead guest.
func batchSSH(f []string) []string {
	if len(f) == 0 {
		return f
	}
	return append([]string{f[0], "-o", "BatchMode=yes", "-o", "ConnectTimeout=10"}, f[1:]...)
}

func sortedKeys(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
