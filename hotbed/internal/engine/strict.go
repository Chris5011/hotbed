package engine

// The `strict` egress preset (issue #4).
//
// Every other preset is a set of iptables rules INSIDE the sandbox netns
// (or guest): root in the sandbox — every agent runs with sudo — can flush
// them. strict moves the enforcement out of the sandbox entirely:
//
//	containers  leave every docker network and join hotbed-strict-<owner>,
//	            an --internal network: no route to the internet, the host
//	            or the docker bridge. Its only other member is the egress
//	            gateway container sbx-egress-<owner> (also on the default
//	            bridge), which runs `hotbedd egress-gateway`: the allowlist
//	            HTTP(S) proxy (egressproxy, :3128) and a plain forwarder to
//	            the LLM proxy. Sandboxes reach it by its network alias
//	            hotbed-egress (docker's embedded DNS on the internal net).
//	libvirt     (strict_libvirt.go) the guest NIC moves to an isolated
//	            libvirt network; the daemon serves the proxy on its bridge.
//
// What changes inside the sandbox is only convenience: HTTP(S)_PROXY /
// NO_PROXY in /etc/profile.d + /etc/environment, apt's proxy config.
// Removing them just breaks the sandbox's own outbound, never the fence.
// docker-backend VMs (vmd) need their published ssh port and are refused,
// as is every container that publishes ports (an --internal network still
// keeps docker's port publishing alive — verified — which would be a way in
// from the host side the preset cannot account for).

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"hotbed/internal/audit"
	"hotbed/internal/env"
)

// StrictAlias is the gateway's name on strict networks (container side).
const StrictAlias = "hotbed-egress"

// StrictProxyPort is the egress proxy port on the gateway / libvirt bridge.
const StrictProxyPort = "3128"

// StrictNetwork is this state set's internal docker network.
func StrictNetwork() string { return "hotbed-strict-" + env.OwnerLabel()[:6] }

// StrictGateway is this state set's egress gateway container.
func StrictGateway() string { return "sbx-egress-" + env.OwnerLabel()[:6] }

// StrictProxyEnv is the proxy environment a strict sandbox gets; host is
// the gateway (container alias / libvirt bridge IP). The LLM proxy and
// loopback bypass the egress proxy (NO_PROXY).
func StrictProxyEnv(host string) map[string]string {
	p := "http://" + host + ":" + StrictProxyPort
	no := "localhost,127.0.0.1,::1," + host
	return map[string]string{
		"HTTP_PROXY": p, "HTTPS_PROXY": p, "http_proxy": p, "https_proxy": p,
		"NO_PROXY": no, "no_proxy": no,
		"NODE_USE_ENV_PROXY": "1", // node >= 24 fetch honors the vars only with this
	}
}

// strictEnvScript writes (or with env == nil removes) the proxy settings.
func strictEnvScript(envs map[string]string) string {
	const prof, envf, apt = "/etc/profile.d/hotbed-egress.sh", "/etc/environment", "/etc/apt/apt.conf.d/90hotbed-egress"
	var b strings.Builder
	// /etc/environment: drop our block, re-add (pam_env sessions, ssh)
	b.WriteString("[ -f " + envf + " ] && sed -i '/^# hotbed-egress begin$/,/^# hotbed-egress end$/d' " + envf + "; ")
	if envs == nil {
		b.WriteString("rm -f " + prof + " " + apt + "; true")
		return b.String()
	}
	keys := []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy", "NO_PROXY", "no_proxy", "NODE_USE_ENV_PROXY"}
	b.WriteString("{ echo '# hotbed strict egress: the only way out is this proxy (allowlist, DNS tab)'; ")
	for _, k := range keys {
		b.WriteString(fmt.Sprintf("echo 'export %s=%s'; ", k, envs[k]))
	}
	b.WriteString("} > " + prof + "; chmod 644 " + prof + "; ")
	b.WriteString("{ echo '# hotbed-egress begin'; ")
	for _, k := range keys {
		b.WriteString(fmt.Sprintf("echo '%s=%s'; ", k, envs[k]))
	}
	b.WriteString("echo '# hotbed-egress end'; } >> " + envf + "; ")
	b.WriteString("[ -d /etc/apt/apt.conf.d ] && printf 'Acquire::http::Proxy \"%s\";\\nAcquire::https::Proxy \"%s\";\\n' > " + apt + "; true")
	return fmt.Sprintf(b.String(), envs["HTTP_PROXY"], envs["HTTPS_PROXY"])
}

// PublishedPorts lists a container's host port bindings ("" = none).
func PublishedPorts(container string) string {
	out, _ := RunTO(8*time.Second, "docker", "port", container)
	return strings.TrimSpace(out)
}

// containerNetworks lists the docker networks a container is attached to.
func containerNetworks(name string) ([]string, string, error) {
	out, err := RunTO(8*time.Second, "docker", "inspect", "--format",
		"{{.HostConfig.NetworkMode}}|{{range $k, $v := .NetworkSettings.Networks}}{{$k}} {{end}}", name)
	if err != nil {
		return nil, "", fmt.Errorf("inspect %s: %w", name, err)
	}
	mode, nets, _ := strings.Cut(strings.TrimSpace(out), "|")
	return strings.Fields(nets), mode, nil
}

// StrictRefusal explains why a sandbox cannot be strict ("" = it can).
func StrictRefusal(name string) string {
	t, err := resolveEgressTarget(name)
	if err != nil {
		return err.Error()
	}
	switch t.kind {
	case "vmd":
		return "docker-backend VMs are reached over a published ssh port, which an isolated network cannot carry - use a libvirt VM or a container for strict"
	case "libvirt":
		return libvirtStrictRefusal(name)
	}
	if p := PublishedPorts(t.container); p != "" {
		return "the container publishes ports (" + strings.ReplaceAll(p, "\n", ", ") +
			") - published ports stay reachable from the host even on an isolated network, so strict cannot vouch for this sandbox; recreate it without -p"
	}
	_, mode, err := containerNetworks(t.container)
	if err != nil {
		return err.Error()
	}
	if mode == "host" || strings.HasPrefix(mode, "container:") {
		return "the container shares another network namespace (" + mode + ") - it cannot be moved onto an isolated network"
	}
	return ""
}

// ------------------------------------------------------------- network ---

// ensureStrictNetwork creates the internal network on first use.
func ensureStrictNetwork() error {
	n := StrictNetwork()
	if _, err := RunTO(8*time.Second, "docker", "network", "inspect", n); err == nil {
		return nil
	}
	if out, err := RunTO(30*time.Second, "docker", "network", "create", "--internal",
		"--label", "dev.hotbed.strict="+env.OwnerLabel()[:6], n); err != nil {
		return fmt.Errorf("strict network %s: %w: %s", n, err, strings.TrimSpace(out))
	}
	audit.Record("strict.network", n, "internal docker network created")
	return nil
}

// EnsureStrictGateway makes sure the egress gateway runs this daemon's
// build, on the default bridge and the strict network, forwarding
// :proxyPort to llmTarget ("ip:port" of the LLM proxy as seen from the
// default bridge — the relay on Docker Desktop, the daemon elsewhere).
func EnsureStrictGateway(proxyPort, llmTarget string) error {
	if err := ensureStrictNetwork(); err != nil {
		return err
	}
	name := StrictGateway()
	want := relayStamp() + "|" + llmTarget
	if exists, running := relayRunning(name); exists {
		if running && gatewayStamp(name) == want {
			return ensureGatewayOnStrict(name)
		}
		_, _ = RunTO(30*time.Second, "docker", "rm", "-f", name)
	}
	bin, err := os.Executable()
	if err != nil {
		return fmt.Errorf("gateway binary: %w", err)
	}
	image, err := pickRelayImage()
	if err != nil {
		return err
	}
	args := []string{
		"run", "-d", "--name", name,
		"--network", "bridge",
		"--restart", "unless-stopped",
		"--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		"-e", "HOTBEDD_CONFIG_DIR=/etc/hotbedd",
		"-e", "HOTBEDD_STATE_DIR=/state", // ip snapshot in, dns-seen records out
		"-e", "HOTBEDD_GATEWAY=" + want,
		"-v", env.ConfigDir() + ":/etc/hotbedd:ro",
		"-v", env.StateDir() + ":/state",
		"-v", bin + ":/usr/local/bin/hotbedd:ro",
		"--entrypoint", "/usr/local/bin/hotbedd",
		image, "egress-gateway",
		"--listen", ":" + StrictProxyPort,
		"--forward", ":" + proxyPort + "=" + llmTarget,
	}
	if out, err := RunTO(90*time.Second, "docker", args...); err != nil {
		return fmt.Errorf("egress gateway spawn: %w: %s", err, strings.TrimSpace(out))
	}
	if err := ensureGatewayOnStrict(name); err != nil {
		return err
	}
	audit.Record("strict.gateway", name, fmt.Sprintf("image=%s llm=%s", image, llmTarget))
	return nil
}

func gatewayStamp(name string) string {
	out, _ := RunTO(8*time.Second, "docker", "inspect", "--format",
		"{{range .Config.Env}}{{println .}}{{end}}", name)
	for _, l := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(l, "HOTBEDD_GATEWAY="); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func ensureGatewayOnStrict(name string) error {
	nets, _, err := containerNetworks(name)
	if err != nil {
		return err
	}
	for _, n := range nets {
		if n == StrictNetwork() {
			return nil
		}
	}
	if out, err := RunTO(30*time.Second, "docker", "network", "connect", "--alias", StrictAlias,
		StrictNetwork(), name); err != nil {
		return fmt.Errorf("gateway onto %s: %w: %s", StrictNetwork(), err, strings.TrimSpace(out))
	}
	return nil
}

// ------------------------------------------------------------- switch ---

// strictNetsPath remembers the networks a container left for strict.
func strictNetsPath(name string) string {
	return filepath.Join(env.StateDir(), "strict", name+".json")
}

// ApplyStrict moves a sandbox onto its isolated network (idempotent).
// Containers: in-netns rules from an earlier preset are reset first (a DNS
// DNAT to the old fence would break the alias lookup), then every docker
// network is swapped for the strict one.
func ApplyStrict(name string) error {
	if why := StrictRefusal(name); why != "" {
		return fmt.Errorf("strict %s refused: %s", name, why)
	}
	t, err := resolveEgressTarget(name)
	if err != nil {
		return err
	}
	if t.kind == "libvirt" {
		return applyStrictLibvirt(t)
	}
	if out, err := t.run(egressResetScript()); err != nil {
		return fmt.Errorf("strict %s: clearing old rules: %w: %s", name, err, strings.TrimSpace(out))
	}
	_ = t.resolver(egressResolverRestore())
	nets, _, err := containerNetworks(t.container)
	if err != nil {
		return err
	}
	var left []string
	onStrict := false
	for _, n := range nets {
		if n == StrictNetwork() {
			onStrict = true
			continue
		}
		left = append(left, n)
	}
	if !onStrict {
		if out, err := RunTO(30*time.Second, "docker", "network", "connect", StrictNetwork(), t.container); err != nil {
			return fmt.Errorf("strict %s: joining %s: %w: %s", name, StrictNetwork(), err, strings.TrimSpace(out))
		}
	}
	if len(left) > 0 {
		prev := readStrictNets(name)
		b, _ := json.Marshal(append(prev, left...))
		_ = os.MkdirAll(filepath.Dir(strictNetsPath(name)), 0o755)
		_ = os.WriteFile(strictNetsPath(name), b, 0o600)
		for _, n := range left {
			if out, err := RunTO(30*time.Second, "docker", "network", "disconnect", n, t.container); err != nil {
				return fmt.Errorf("strict %s: leaving %s: %w: %s", name, n, err, strings.TrimSpace(out))
			}
		}
	}
	if out, err := egressResolverExecOut(t.container, strictEnvScript(StrictProxyEnv(StrictAlias))); err != nil {
		return fmt.Errorf("strict %s: proxy env: %w: %s", name, err, strings.TrimSpace(out))
	}
	keeperNudge() // attribution: the gateway judges by the new address
	forgetTarget(name)
	audit.Record("egress.set", name, "strict (isolated network "+StrictNetwork()+", gateway "+StrictGateway()+")")
	return nil
}

// LeaveStrict puts a strict sandbox back on its previous networks (default
// bridge when unknown) and removes the proxy env. A no-op when not strict.
func LeaveStrict(name string) error {
	t, err := resolveEgressTarget(name)
	if err != nil {
		return err
	}
	if t.kind == "libvirt" {
		return leaveStrictLibvirt(t)
	}
	if t.kind != "container" {
		return nil
	}
	nets, _, err := containerNetworks(t.container)
	if err != nil {
		return err
	}
	on := false
	for _, n := range nets {
		if n == StrictNetwork() {
			on = true
		}
	}
	if !on {
		return nil
	}
	back := readStrictNets(name)
	if len(back) == 0 {
		back = []string{"bridge"}
	}
	for _, n := range back {
		if out, err := RunTO(30*time.Second, "docker", "network", "connect", n, t.container); err != nil &&
			!strings.Contains(out, "already exists") {
			return fmt.Errorf("leave strict %s: rejoining %s: %w: %s", name, n, err, strings.TrimSpace(out))
		}
	}
	if out, err := RunTO(30*time.Second, "docker", "network", "disconnect", StrictNetwork(), t.container); err != nil {
		return fmt.Errorf("leave strict %s: %w: %s", name, err, strings.TrimSpace(out))
	}
	_ = os.Remove(strictNetsPath(name))
	_, _ = egressResolverExecOut(t.container, strictEnvScript(nil))
	keeperNudge()
	forgetTarget(name)
	audit.Record("egress.set", name, "left strict (back on "+strings.Join(back, ", ")+")")
	return nil
}

// StrictInstalled reports whether a sandbox is currently isolated.
func StrictInstalled(name string) (bool, error) {
	t, err := resolveEgressTarget(name)
	if err != nil {
		return false, err
	}
	if t.kind == "libvirt" {
		return libvirtOnStrict(t.name)
	}
	nets, _, err := containerNetworks(t.container)
	if err != nil {
		return false, err
	}
	return len(nets) == 1 && nets[0] == StrictNetwork(), nil
}

// IsStrictContainer reports whether a container sits on the strict network
// (proxy URL resolution: the LLM proxy is the gateway then).
func IsStrictContainer(name string) bool {
	nets, _, err := containerNetworks(name)
	if err != nil {
		return false
	}
	for _, n := range nets {
		if n == StrictNetwork() {
			return true
		}
	}
	return false
}

func readStrictNets(name string) []string {
	var v []string
	if b, err := os.ReadFile(strictNetsPath(name)); err == nil {
		_ = json.Unmarshal(b, &v)
	}
	return v
}

func egressResolverExecOut(name, script string) (string, error) {
	return RunTO(20*time.Second, "docker", "exec", "-u", "root", name, "sh", "-c", script)
}

// StrictSettled reports whether a sandbox's isolation matches want — for a
// libvirt guest also whether its lease already is on the matching network
// (the re-sync watcher must not ride the address being switched away).
// Sandboxes that cannot be resolved count as settled (nothing to wait for).
func StrictSettled(name string, want bool) bool {
	t, err := resolveEgressTarget(name)
	if err != nil {
		return true
	}
	switch t.kind {
	case "container":
		return IsStrictContainer(t.container) == want
	case "libvirt":
		on, err := libvirtOnStrict(name)
		if err != nil || on != want {
			return err != nil
		}
		ip := LibvirtLease(env.VMStdPrefix + name)
		br := StrictLibvirtBridgeIP()
		if ip == "" || br == "" {
			return ip != "" && !want
		}
		if same := strings.HasPrefix(ip, br[:strings.LastIndex(br, ".")+1]); same != want {
			return false
		}
		// the lease shows up before the guest's link answers ("no route")
		c, err := net.DialTimeout("tcp", net.JoinHostPort(ip, "22"), 3*time.Second)
		if err != nil {
			return false
		}
		c.Close()
		return true
	}
	return true
}
