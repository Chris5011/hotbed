package sandbox

import (
	"sort"
	"strings"
	"sync"

	"hotbed/internal/engine"
	"hotbed/internal/env"
)

// Collector aggregates all sandboxes across runtimes.
type Collector struct {
	mu    sync.Mutex
	last  []Sandbox
	byKey map[string]Sandbox
}

// Scan gathers the current inventory (containers + both VM backends).
//
// Docker first answers a bounded probe (DockerUp, 4 s); when the engine is
// down — or wedged on a cold/remote host — the heavy ps/stats/port fanout
// is skipped entirely instead of dragging every refresh tick: an empty
// docker section comes back promptly until the engine works again.
func (c *Collector) Scan() []Sandbox {
	// never nil: an empty inventory must cross the wire as [] , not null
	// (JSON null is what crashed the dashboard's renderList on a fresh box).
	out := make([]Sandbox, 0)
	dockerUp := engine.DockerUp()
	var stats map[string]engine.ContainerStat

	// --- bin/hbd sandboxes (docker containers) ----------------------------
	if dockerUp {
		stats = engine.DockerStats()
		rows, _ := engine.ListSandboxContainers()
		for _, r := range rows {
			name := strings.TrimPrefix(r.Names, "/")
			st := ContainerSandbox{
				Sandbox: Sandbox{
					Kind: KindContainer, Name: name, ID: shortID(r.ID), Image: r.Image,
					State: normalizeDockerState(r.State), Status: r.Status,
					Project: r.Labels["dev.hotbed.project"], Stack: r.Labels["dev.hotbed.stack"],
					CreatedAt: r.CreatedAt, Uptime: ofAge(r.CreatedAt),
				},
			}
			if s, ok := stats[name]; ok {
				st.CPUPct = engine.ParsePerc(s.CPUPerc)
				st.MemPct = engine.ParsePerc(s.MemPerc)
				st.MemUsage = s.MemUse
				st.NetIO = s.NetIO
				st.BlockIO = s.BlockIO
			}
			out = append(out, st.Sandbox)
		}
	}

	// --- docker-backend VMs (hotbedvmd containers) ---------------------------
	if dockerUp {
		vmrows, _ := engine.ListVMDockerContainers()
		for _, r := range vmrows {
			name := strings.TrimPrefix(r.Names, "/")
			vm := strings.TrimPrefix(name, env.VMDockerPrefix)
			sb := Sandbox{
				Kind: KindVM, Backend: BackendDocker, Name: vm, ID: shortID(r.ID),
				Image: r.Image, State: normalizeDockerState(r.State), Status: r.Status,
				CreatedAt: r.CreatedAt, Uptime: ofAge(r.CreatedAt), Project: "hotbedvmd store",
			}
			if s, ok := stats[name]; ok {
				sb.CPUPct = engine.ParsePerc(s.CPUPerc)
				sb.MemPct = engine.ParsePerc(s.MemPerc)
				sb.MemUsage = s.MemUse
				sb.NetIO = s.NetIO
				sb.BlockIO = s.BlockIO
			}
			if p := engine.DockerPort(name); p != "" {
				sb.Ports = append(sb.Ports, "127.0.0.1:"+p)
				sb.SSH = "SSH_CMD=ssh -i ~/.ssh/hotbedvm_key -o StrictHostKeyChecking=accept-new -p " + p + " dev@127.0.0.1"
				sb.LogHint = "serial (container stream)"
			}
			// CPU/MEM already come from `docker stats` above; the guest kernel is
			// opaque to the host for docker-backend VMs (no virsh domain exists).
			// The kit self-registers its boot: engine "running" ahead of an
			// unconfirmed boot-select signal is an honest "booting" instead —
			// containers are untouched (they boot in seconds).
			if sb.State == "running" && IsPendingBoot(vm) {
				sb.State = "booting"
				sb.Status = "guest booting (self-registered, awaiting kit confirmation)"
			}
			out = append(out, sb)
		}
	}

	// --- libvirt VMs ------------------------------------------------------
	if engine.LibvirtUp() {
		doms, _ := engine.LibvirtList()
		for _, d := range doms {
			if !strings.HasPrefix(d.Name, env.VMStdPrefix) {
				continue
			}
			name := strings.TrimPrefix(d.Name, env.VMStdPrefix)
			sb := Sandbox{
				Kind: KindVM, Backend: BackendLibvirt, Name: name,
				State: d.State, Status: d.State, Project: "libvirt domain " + d.Name,
			}
			if ip := engine.LibvirtLease(d.Name); ip != "" {
				sb.SSH = "SSH_CMD=ssh -i ~/.ssh/hotbedvm_key dev@" + ip
			}
			if d.State == "running" {
				if cur, max := engine.VMStats(d.Name); max > 0 {
					sb.MemUsage = gibStr(cur) + "GiB / " + gibStr(max) + "GiB"
					sb.MemPct = cur / max * 100
				}
				sb.CPUPct = engine.VMCPUPct(d.Name)
				sb.LogHint = "console (pty)"
			}
			if d.State == "running" && IsPendingBoot(name) {
				sb.State = "booting"
				sb.Status = "guest booting (self-registered, awaiting kit confirmation)"
			}
			out = append(out, sb)
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		if out[i].Backend != out[j].Backend {
			return out[i].Backend < out[j].Backend
		}
		return out[i].Name < out[j].Name
	})

	c.mu.Lock()
	c.last = out
	c.mu.Unlock()
	return out
}

// Snapshot returns the last successful scan (scans if never scanned).
func (c *Collector) Snapshot() []Sandbox {
	c.mu.Lock()
	l := c.last
	c.mu.Unlock()
	if l == nil {
		return c.Scan()
	}
	return l
}

// Find locates one sandbox by kind+name ("" kind = unique name).
func (c *Collector) Find(kind, name string) (Sandbox, bool) {
	for _, s := range c.Snapshot() {
		if s.Name != name {
			continue
		}
		if kind == "" || kind == s.Kind {
			return s, true
		}
	}
	return Sandbox{}, false
}

// ContainerSandbox is intentionally tiny (typed construction helper for future fields).
type ContainerSandbox struct{ Sandbox }

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func normalizeDockerState(s string) string {
	switch s {
	case "created":
		return "created"
	case "paused":
		return "paused"
	case "restarting":
		return "restarting"
	case "removing":
		return "removing"
	case "dead":
		return "dead"
	case "exited":
		return "stopped"
	default:
		return s
	}
}

func ofAge(created string) string { return AgeOf(created) }

func gibStr(kibF float64) string {
	if kibF >= 10 {
		return itoa2(int(kibF))
	}
	// one decimal
	wh := int(kibF)
	de := int((kibF - float64(wh)) * 10)
	return itoa2(wh) + "." + itoa2(de)
}

func itoa2(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
