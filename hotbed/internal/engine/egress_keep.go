package engine

// The egress keeper: daemon-side companion of the allowlist DNS fence
// (internal/dnsfence). The fence lives in the engine netns (proxy relay on
// Docker Desktop, the daemon itself on native engines) and appends one
// {sandbox, ip} JSON line per freshly resolved allowlist domain to the
// state dir. The keeper — which CAN reach every sandbox via docker —
// tails that file and installs the matching iptables ACCEPT inside the
// sandbox netns, so TCP to just-answered IPs opens within seconds.
//
// It also maintains the fence's {ip: sandbox} attribution snapshot the
// fence reads per query (fenced containers resolve through the fence, so
// the source IP → sandbox mapping is all the fence needs to judge them).

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"hotbed/internal/dnsfence"
)

// keeperEvent mirrors dnsfence's event line format (decoupled on purpose).
type keeperEvent struct {
	Sandbox string `json:"sandbox"`
	IP      string `json:"ip"`
}

// ipSnapshotFile is the attribution snapshot the fence reads.
type ipSnapshotFile struct {
	IPs map[string]string `json:"ips"`
}

// StartEgressKeeper launches the background keeper goroutine. Best-effort
// by design: docker hops, missing fences or unreadable state must never
// wedge the daemon — failures are swallowed per tick, retried on arrival.
func StartEgressKeeper() {
	go makerLoop()
}

func makerLoop() {
	ipTick := 0 // attribution snapshot refresh counter
	var off int64
	for {
		time.Sleep(1 * time.Second)
		off = consumeEgressEvents(off)
		if ipTick++; ipTick >= 30 { // every ~30s
			ipTick = 0
			_ = SnapshotSandboxIPs()
		}
	}
}

// consumeEgressEvents reads fresh fence events from off, applies new
// per-IP ACCEPT rules, and returns the next read offset.
func consumeEgressEvents(off int64) int64 {
	f, err := os.Open(dnsfence.EventsPath())
	if err != nil {
		return 0 // absent (yet): restart from zero
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return off
	}
	if fi.Size() < off {
		off = 0 // truncated/rotated: reread everything (idempotent anyway)
	}
	if fi.Size() == off {
		return off // nothing new
	}
	if _, err := f.Seek(off, 0); err != nil {
		return off
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 64*1024)
	for sc.Scan() {
		var ev keeperEvent
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		key := ev.Sandbox + "|" + ev.IP
		if applied[key] {
			continue
		}
		// single goroutine owns this loop: no lock needed for the map
		if ApplyEgressIP(ev.Sandbox, ev.IP) == nil {
			applied[key] = true
		}
	}
	pos, err := f.Seek(0, 2)
	if err != nil {
		return off
	}
	trimEgressEvents(fi.Size())
	return pos
}

// trimEgressEvents caps the shared events file once it grows past a few
// MB (the keeper has consumed it at that point; the relay's freshest lines
// may re-add — harmless duplicates).
func trimEgressEvents(size int64) {
	if size < 5*1024*1024 {
		return
	}
	_ = os.Truncate(dnsfence.EventsPath(), 0)
}

// applied tracks ACCEPTs installed this daemon life (per sandbox+ip); a
// fence re-apply bakes its own upstream resolution, stale memory just
// causes harmless `-C || -I` idempotence.
var applied = map[string]bool{}

// SnapshotSandboxIPs writes the fence's {ip: sandbox-name} attribution
// snapshot for all running kit sandboxes (best-effort; the fence fails
// closed for sources missing from it).
func SnapshotSandboxIPs() error {
	rows, err := ListSandboxContainers()
	if err != nil {
		return err
	}
	m := map[string]string{}
	for _, r := range rows {
		name := trimName(r.Names)
		if name == "" || r.State != "running" {
			continue
		}
		if ip, e := ContainerIP(name); e == nil && ip != "" {
			m[ip] = name
		}
	}
	out, err := json.Marshal(ipSnapshotFile{IPs: m})
	if err != nil {
		return err
	}
	path := dnsfence.IPsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ContainerIP returns the container's IPv4 on its default network.
func ContainerIP(name string) (string, error) {
	out, err := RunTO(8*time.Second, "docker", "inspect", "--format",
		"{{.NetworkSettings.IPAddress}}", name)
	return trimName(out), err
}

// trimName strips container-name prefixes (/) and inspect whitespace.
func trimName(s string) string {
	for len(s) > 0 && (s[0] == '/') {
		s = s[1:]
	}
	return trimWhitespace(s)
}

func trimWhitespace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t' || s[0] == '\r' || s[0] == '\n') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t' || s[len(s)-1] == '\r' || s[len(s)-1] == '\n') {
		s = s[:len(s)-1]
	}
	return s
}

// keeperNudge refreshes the attribution snapshot right away (fence install
// path) so the very first lookups attribute correctly.
func keeperNudge() {
	if err := SnapshotSandboxIPs(); err != nil {
		fmt.Fprintf(os.Stderr, "\033[33;1mhotbedd:\033[0m egress ip snapshot: %v\n", err)
	}
}
