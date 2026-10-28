package engine

// Egress targets: where a sandbox's fence rules live.
//
//	container  the sandbox container's netns (docker exec / netns helper)
//	vmd        a docker-backend VM: qemu runs user-mode networking inside
//	           its hotbedvmd-<name> container, so every guest packet leaves
//	           through that container's netns — the container fence applies
//	           unchanged, from OUTSIDE the guest
//	libvirt    a libvirt guest sits behind the host's NAT; host iptables
//	           would need root, so the rules go INTO the guest over the kit's
//	           ssh channel (sudo). Weaker: root inside the guest can lift them
//	           (documented; the fence is a convenience, not a boundary).
//
// Records (config keys, fence attribution, audit) always use the sandbox
// name as the dashboard shows it; only the rule placement differs.

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"hotbed/internal/env"
)

type egressTarget struct {
	name      string // sandbox name
	kind      string // container | vmd | libvirt
	container string // netns holder (container, vmd)
	ip        string // libvirt guest address
	gw        string // DNAT destination of the fence (cached per target)
}

var (
	targetMu    sync.Mutex
	targetCache = map[string]targetEntry{}
)

type targetEntry struct {
	t  egressTarget
	at time.Time
}

// resolveEgressTarget finds where a sandbox's rules go (cached ~30s: the
// keeper resolves per fence event).
func resolveEgressTarget(name string) (egressTarget, error) {
	targetMu.Lock()
	if e, ok := targetCache[name]; ok && time.Since(e.at) < 30*time.Second {
		targetMu.Unlock()
		return e.t, nil
	}
	targetMu.Unlock()
	t, err := lookupEgressTarget(name)
	if err != nil {
		return t, err
	}
	targetMu.Lock()
	targetCache[name] = targetEntry{t: t, at: time.Now()}
	targetMu.Unlock()
	return t, nil
}

func lookupEgressTarget(name string) (egressTarget, error) {
	if !strings.HasPrefix(name, env.VMDockerPrefix) {
		if _, err := RunTO(8*time.Second, "docker", "inspect", "--format", "{{.Id}}", name); err == nil {
			return egressTarget{name: name, kind: "container", container: name}, nil
		}
	}
	if _, err := RunTO(8*time.Second, "docker", "inspect", "--format", "{{.Id}}", env.VMDockerPrefix+name); err == nil {
		return egressTarget{name: name, kind: "vmd", container: env.VMDockerPrefix + name}, nil
	}
	if LibvirtUp() {
		dom := env.VMStdPrefix + name
		if ip := LibvirtLease(dom); ip != "" {
			return egressTarget{name: name, kind: "libvirt", ip: ip}, nil
		}
		if _, err := Run("virsh", VirshArgs("dominfo", dom)...); err == nil {
			return egressTarget{}, fmt.Errorf("egress %s: libvirt guest has no DHCP lease yet (still booting?)", name)
		}
	}
	return egressTarget{}, fmt.Errorf("egress %s: no such container or VM", name)
}

// forgetTarget drops a cached resolution (sandbox recreated / deleted).
func forgetTarget(name string) {
	targetMu.Lock()
	delete(targetCache, name)
	targetMu.Unlock()
}

// sshArgs is the kit's ssh channel into a libvirt guest, unattended.
func (t egressTarget) sshArgs(remote string) []string {
	return guestSSH("dev@"+t.ip, "", remote)
}

func guestSSH(dest, port, remote string) []string {
	home, _ := os.UserHomeDir()
	a := []string{"-i", home + "/.ssh/hotbedvm_key", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10",
		"-o", "StrictHostKeyChecking=accept-new"}
	if port != "" {
		a = append(a, "-p", port)
	}
	return append(a, dest, remote)
}

// flushGuestDNS empties a VM guest's resolver cache (systemd-resolved)
// after its fence changed: answers cached under the old rules would
// otherwise bypass the fence's record — a lock-down showed no "refused"
// lookups because the guest never asked again. Best-effort; containers
// have no caching resolver.
func (t egressTarget) flushGuestDNS() {
	const cmd = "sudo resolvectl flush-caches 2>/dev/null || true"
	switch t.kind {
	case "libvirt":
		_, _ = RunTO(20*time.Second, "ssh", t.sshArgs(cmd)...)
	case "vmd":
		if p := DockerPort(t.container); p != "" {
			_, _ = RunTO(20*time.Second, "ssh", guestSSH("dev@127.0.0.1", p, cmd)...)
		}
	}
}

// run executes an iptables script where the rules live.
func (t egressTarget) run(script string) (string, error) {
	if t.kind == "libvirt" {
		return RunTO(45*time.Second, "ssh", t.sshArgs("sudo sh -c "+shQuoteE(script))...)
	}
	return egressRun(t.container, script)
}

// resolver runs a resolv.conf script in the sandbox's own filesystem.
// libvirt guests resolve through systemd-resolved's loopback stub, whose
// upstream queries the nat side catches anyway — nothing to rewrite there.
func (t egressTarget) resolver(script string) error {
	if t.kind == "libvirt" {
		return nil
	}
	return egressResolverExec(t.container, script)
}

// fenceGW is where the target's DNS gets DNAT'ed to: the docker bridge
// gateway for container netns (relay fence on Docker Desktop, daemon fence
// on native engines), the guest's default gateway for libvirt (the
// daemon's own fence listener on the host side of the libvirt bridge).
func (t *egressTarget) fenceGW() string {
	if t.gw != "" {
		return t.gw
	}
	if t.kind == "libvirt" {
		out, err := RunTO(20*time.Second, "ssh", t.sshArgs("ip -4 route show default")...)
		if err == nil {
			if f := strings.Fields(out); len(f) >= 3 && f[0] == "default" && f[1] == "via" {
				t.gw = f[2]
			}
		}
	} else {
		t.gw = BridgeGateway()
	}
	return t.gw
}

// skipLoopback: libvirt guests talk to a loopback DNS stub — DNAT'ing
// 127.0.0.0/8 there would turn the stub's own traffic into martians.
func (t egressTarget) skipLoopback() bool { return t.kind == "libvirt" }

// startID changes whenever the target's rules were wiped by a restart.
func (t egressTarget) startID() (id string, running bool, err error) {
	if t.kind == "libvirt" {
		out, err := RunTO(20*time.Second, "ssh", t.sshArgs("cat /proc/sys/kernel/random/boot_id")...)
		if err != nil {
			return "", false, err
		}
		return strings.TrimSpace(out), true, nil
	}
	out, err := RunTO(8*time.Second, "docker", "inspect", "--format", "{{.State.Running}} {{.State.StartedAt}}", t.container)
	if err != nil {
		return "", false, err
	}
	r, started, _ := strings.Cut(strings.TrimSpace(out), " ")
	return started, r == "true", nil
}

// EgressStartID is the restart stamp the daemon's re-applier compares.
func EgressStartID(name string) (string, bool, error) {
	t, err := resolveEgressTarget(name)
	if err != nil {
		return "", false, err
	}
	return t.startID()
}

// EgressKind reports where a sandbox's rules go (container|vmd|libvirt).
func EgressKind(name string) (string, error) {
	t, err := resolveEgressTarget(name)
	return t.kind, err
}

func shQuoteE(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
