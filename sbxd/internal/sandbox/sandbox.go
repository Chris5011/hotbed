// Package sandbox defines the unified sandbox entity collected from the
// container side (bin/sbx) and both sbxvm backends (libvirt, docker/sbxvmd).
package sandbox

import "time"

// Kind classifies the runtime hosting a sandbox.
const (
	KindContainer = "container" // bin/sbx docker sandbox
	KindVM        = "vm"        // sbxvm VM (see Backend)
)

// Backend (only set when Kind == vm).
const (
	BackendLibvirt = "libvirt"
	BackendDocker  = "docker"
)

// ConnInfo carries every pre-rolled way to reach a sandbox.
type ConnInfo struct {
	TmuxSession string `json:"tmuxSession,omitempty"`
	TmuxAttach  string `json:"tmuxAttach,omitempty"` // run inside WSL shell
	ShellCmd    string `json:"shellCmd,omitempty"`   // one-shot, WSL shell
	BackendCmd  string `json:"backendCmd,omitempty"` // runs opencode TUI (one-shot)
	WTCommand   string `json:"wtCommand,omitempty"`  // works from Windows cmd/pwsh
	AttachHTTP  string `json:"attachHttp,omitempty"` // POST target to (re)spawn tmux session
}

// Sandbox is the unified entity the dashboard/CLI render.
type Sandbox struct {
	Kind      string   `json:"kind"`              // container|vm
	Backend   string   `json:"backend,omitempty"` // vm only: libvirt|docker
	Name      string   `json:"name"`
	ID        string   `json:"id,omitempty"` // docker container id (short)
	Image     string   `json:"image,omitempty"`
	State     string   `json:"state"`            // running|exited|stopped|paused|shut off...
	Status    string   `json:"status,omitempty"` // human status string
	Project   string   `json:"project,omitempty"`
	Stack     string   `json:"stack,omitempty"`
	Ports     []string `json:"ports,omitempty"` // published host ports (VMs/docker)
	SSH       string   `json:"ssh,omitempty"`   // ready-made ssh target, for host cmds
	CreatedAt string   `json:"createdAt,omitempty"`
	Uptime    string   `json:"uptime,omitempty"`

	// live stats (best-effort)
	CPUPct   float64 `json:"cpuPercent,omitempty"`
	MemPct   float64 `json:"memPercent,omitempty"`
	MemUsage string  `json:"memUsage,omitempty"`
	NetIO    string  `json:"netIO,omitempty"`
	BlockIO  string  `json:"blockIO,omitempty"`

	// log availability hint for the UI
	LogHint string `json:"logHint,omitempty"`
}

// AgeOf returns a coarse human age from a createdAt string (RFC3339 or layout varies by source).
func AgeOf(s string) string {
	if s == "" {
		return ""
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05 -0700 MST", time.RFC3339Nano} {
		if t, err := time.Parse(layout, s); err == nil {
			d := time.Since(t)
			switch {
			case d.Hours() > 48:
				return fmtAges(int(d.Hours()/24), "d")
			case d.Hours() >= 1:
				return fmtAges(int(d.Hours()), "h")
			case d.Minutes() >= 1:
				return fmtAges(int(d.Minutes()), "m")
			default:
				return fmtAges(int(d.Seconds()), "s")
			}
		}
	}
	return ""
}

func fmtAges(n int, unit string) string {
	if n <= 0 {
		return "now"
	}
	return itoa(n) + unit
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
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
