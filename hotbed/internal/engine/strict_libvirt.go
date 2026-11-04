package engine

// strict for libvirt guests: the guest's NIC is re-pointed (live, same
// MAC) from its NAT network to hotbed-strict-<owner>, an isolated libvirt
// network — no <forward>, so libvirt routes nothing out of it. The host
// keeps reaching the guest on the bridge (ssh: attach, the LLM proxy's
// ssh -R tunnel), and the daemon serves the egress proxy on the bridge IP
// (cli serve: StrictLibvirtListen). A link down/up makes the guest DHCP
// on the new network.

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"hotbed/internal/audit"
	"hotbed/internal/env"
)

// StrictLibvirtNetwork is this state set's isolated libvirt network.
func StrictLibvirtNetwork() string { return "hotbed-strict-" + env.OwnerLabel()[:6] }

func strictBridge() string { return "virbr-hbs" + env.OwnerLabel()[:6] } // 15 chars max

// StrictLibvirtBridgeIP is the host address on the isolated network ("" =
// network not defined/active).
func StrictLibvirtBridgeIP() string {
	out, err := RunTO(10*time.Second, "virsh", VirshArgs("net-dumpxml", StrictLibvirtNetwork())...)
	if err != nil {
		return ""
	}
	if m := regexp.MustCompile(`<ip address='([0-9.]+)'`).FindStringSubmatch(out); m != nil {
		return m[1]
	}
	return ""
}

func libvirtStrictRefusal(_ string) string {
	if !LibvirtUp() {
		return "libvirt is not reachable"
	}
	return ""
}

// freeSubnet picks a 192.168.N.0/24 no host route or libvirt network uses.
func freeSubnet() (string, error) {
	used := map[string]bool{}
	if ifs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range ifs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil {
				used[ipn.IP.Mask(net.CIDRMask(24, 32)).String()] = true
			}
		}
	}
	out, _ := RunTO(10*time.Second, "ip", "-4", "route")
	for _, m := range regexp.MustCompile(`(\d+\.\d+\.\d+)\.\d+/\d+`).FindAllStringSubmatch(out, -1) {
		used[m[1]+".0"] = true
	}
	for n := 240; n < 255; n++ {
		if c := fmt.Sprintf("192.168.%d.0", n); !used[c] {
			return fmt.Sprintf("192.168.%d", n), nil
		}
	}
	return "", fmt.Errorf("no free 192.168.240-254.0/24 subnet for the strict network")
}

// ensureStrictLibvirtNetwork defines + starts (autostart) the network.
func ensureStrictLibvirtNetwork() error {
	n := StrictLibvirtNetwork()
	if out, err := RunTO(10*time.Second, "virsh", VirshArgs("net-info", n)...); err == nil {
		if !regexp.MustCompile(`Active:\s+yes`).MatchString(out) {
			if o, err := RunTO(30*time.Second, "virsh", VirshArgs("net-start", n)...); err != nil {
				return fmt.Errorf("strict network %s start: %w: %s", n, err, strings.TrimSpace(o))
			}
		}
		return nil
	}
	pfx, err := freeSubnet()
	if err != nil {
		return err
	}
	xml := fmt.Sprintf(`<network><name>%s</name><bridge name='%s' stp='on' delay='0'/>`+
		`<ip address='%s.1' netmask='255.255.255.0'><dhcp><range start='%s.10' end='%s.250'/></dhcp></ip></network>`,
		n, strictBridge(), pfx, pfx, pfx)
	f, err := os.CreateTemp("", "hotbed-strict-net-*.xml")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, _ = f.WriteString(xml)
	f.Close()
	for _, step := range [][]string{{"net-define", f.Name()}, {"net-autostart", n}, {"net-start", n}} {
		if o, err := RunTO(30*time.Second, "virsh", VirshArgs(step...)...); err != nil {
			return fmt.Errorf("strict network %s (%s): %w: %s", n, step[0], err, strings.TrimSpace(o))
		}
	}
	audit.Record("strict.network", n, "isolated libvirt network "+pfx+".0/24 (no forward)")
	return nil
}

// guestIface is the domain's first network interface (mac, source net).
func guestIface(dom string) (mac, source string, err error) {
	out, err := RunTO(10*time.Second, "virsh", VirshArgs("domiflist", dom)...)
	if err != nil {
		return "", "", fmt.Errorf("domiflist %s: %w", dom, err)
	}
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		// Interface Type Source Model MAC
		if len(f) >= 5 && f[1] == "network" {
			return f[4], f[2], nil
		}
	}
	return "", "", fmt.Errorf("%s has no libvirt-network interface (bridged/direct NICs cannot go strict)", dom)
}

func libvirtOnStrict(name string) (bool, error) {
	_, src, err := guestIface(env.VMStdPrefix + name)
	return src == StrictLibvirtNetwork(), err
}

// repoint moves the NIC to network src (live + persistent), bouncing the
// link so the guest re-runs DHCP there, then waits for the new lease.
func repoint(dom, mac, src string) (string, error) {
	f, err := os.CreateTemp("", "hotbed-strict-if-*.xml")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	for _, state := range []string{"down", "up"} {
		xml := fmt.Sprintf(`<interface type='network'><mac address='%s'/><source network='%s'/>`+
			`<model type='virtio'/><link state='%s'/></interface>`, mac, src, state)
		if err := os.WriteFile(f.Name(), []byte(xml), 0o600); err != nil {
			return "", err
		}
		if o, err := RunTO(30*time.Second, "virsh", VirshArgs("update-device", dom, f.Name(), "--live", "--config")...); err != nil {
			return "", fmt.Errorf("re-point %s to %s: %w: %s", dom, src, err, strings.TrimSpace(o))
		}
		time.Sleep(2 * time.Second)
	}
	pfx := ""
	if out, err := RunTO(10*time.Second, "virsh", VirshArgs("net-dumpxml", src)...); err == nil {
		if m := regexp.MustCompile(`<ip address='(\d+\.\d+\.\d+)\.`).FindStringSubmatch(out); m != nil {
			pfx = m[1] + "."
		}
	}
	for i := 0; i < 45; i++ {
		if ip := LibvirtLease(dom); ip != "" && (pfx == "" || strings.HasPrefix(ip, pfx)) {
			return ip, nil
		}
		time.Sleep(2 * time.Second)
	}
	return "", fmt.Errorf("%s got no lease on %s within 90s", dom, src)
}

// dropTunnelScript ends the guest side of the LLM proxy's ssh -R tunnel
// before the guest changes address: the old sshd session would otherwise
// keep holding 127.0.0.1:<proxy port> in the guest (verified), so every
// new tunnel fails "remote port forwarding failed" until it times out.
// The host-side ssh sees the close and exits; the re-sync builds a new one.
// port is the configured proxy.port - the tunnel listens on that, not on
// the default (with a custom port the old listener used to survive).
func dropTunnelScript(port string) string {
	if port == "" {
		port = proxyFencePort()
	}
	return fmt.Sprintf("for p in $(ss -ltnpH 'sport = :%s' 2>/dev/null | grep -o 'pid=[0-9]*' | cut -d= -f2 | sort -u); do "+
		"case \"$(cat /proc/$p/comm 2>/dev/null)\" in sshd*) kill $p;; esac; done; true", port)
}

func strictPrevNetPath(name string) string {
	return filepath.Join(env.StateDir(), "strict", name+".libvirt")
}

func applyStrictLibvirt(t egressTarget, proxyPort string) error {
	dom := env.VMStdPrefix + t.name
	if err := ensureStrictLibvirtNetwork(); err != nil {
		return err
	}
	mac, src, err := guestIface(dom)
	if err != nil {
		return err
	}
	bridge := StrictLibvirtBridgeIP()
	if bridge == "" {
		return fmt.Errorf("strict %s: network %s has no address", t.name, StrictLibvirtNetwork())
	}
	// in-guest rules of an earlier preset + proxy env, while still reachable
	if out, err := t.run(egressResetScript()); err != nil {
		return fmt.Errorf("strict %s: clearing old rules: %w: %s", t.name, err, strings.TrimSpace(out))
	}
	_ = t.resolver(egressResolverRestore())
	if out, err := t.run(strictEnvScript(StrictProxyEnv(bridge))); err != nil {
		return fmt.Errorf("strict %s: proxy env: %w: %s", t.name, err, strings.TrimSpace(out))
	}
	if src != StrictLibvirtNetwork() {
		_, _ = t.run(dropTunnelScript(proxyPort))
		_ = os.MkdirAll(filepath.Dir(strictPrevNetPath(t.name)), 0o755)
		_ = os.WriteFile(strictPrevNetPath(t.name), []byte(src), 0o600)
		ip, err := repoint(dom, mac, StrictLibvirtNetwork())
		if err != nil {
			return err
		}
		t.ip = ip
	}
	forgetTarget(t.name)
	keeperNudge()
	audit.Record("egress.set", t.name, "strict (isolated libvirt network "+StrictLibvirtNetwork()+", proxy "+bridge+":"+StrictProxyPort+")")
	return nil
}

func leaveStrictLibvirt(t egressTarget, proxyPort string) error {
	dom := env.VMStdPrefix + t.name
	mac, src, err := guestIface(dom)
	if err != nil || src != StrictLibvirtNetwork() {
		return err
	}
	back := "default"
	if b, err := os.ReadFile(strictPrevNetPath(t.name)); err == nil && strings.TrimSpace(string(b)) != "" {
		back = strings.TrimSpace(string(b))
	}
	_, _ = t.run(strictEnvScript(nil))
	_, _ = t.run(dropTunnelScript(proxyPort))
	if _, err := repoint(dom, mac, back); err != nil {
		return err
	}
	_ = os.Remove(strictPrevNetPath(t.name))
	forgetTarget(t.name)
	keeperNudge()
	audit.Record("egress.set", t.name, "left strict (back on libvirt network "+back+")")
	return nil
}
