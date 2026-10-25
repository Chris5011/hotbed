package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"sbxd/internal/audit"
	"sbxd/internal/env"
)

// VirshArgs prefixes a virsh invocation for the system URI.
func VirshArgs(args ...string) []string {
	return append([]string{"-c", "qemu:///system"}, args...)
}

// LibvirtUp checks that the system libvirt daemon answers.
func LibvirtUp() bool {
	out, err := Run("virsh", VirshArgs("uri")...)
	return err == nil && strings.HasPrefix(strings.TrimSpace(out), "qemu:///system")
}

// LibvirtDomain describes one row of `virsh list --all`.
type LibvirtDomain struct {
	ID    string
	Name  string
	State string
}

// LibvirtList returns all libvirt domains.
func LibvirtList() ([]LibvirtDomain, error) {
	out, err := Run("virsh", VirshArgs("list", "--all")...)
	if err != nil {
		return nil, err
	}
	var doms []LibvirtDomain
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		if _, err := strconv.Atoi(f[0]); err != nil {
			if f[0] != "-" {
				continue // header / separator
			}
			f[0] = "" // shut-off domains have no live Id (virsh prints "-")
		}
		doms = append(doms, LibvirtDomain{ID: f[0], Name: f[1], State: strings.Join(f[2:], " ")})
	}
	return doms, nil
}

var ipRe = regexp.MustCompile(`(\d+\.\d+\.\d+\.\d+)(/\d+)?`)

// LibvirtLease extracts the first IPv4 lease of a domain.
func LibvirtLease(domain string) string {
	out, err := Run("virsh", VirshArgs("domifaddr", domain)...)
	if err != nil {
		return ""
	}
	if m := ipRe.FindStringSubmatch(out); m != nil {
		return m[1]
	}
	return ""
}

// virtSample is the parsed piece of `virsh domstats` we keep per domain.
type virtSample struct {
	cpuNs  uint64
	memCur uint64    // KiB (balloon.current)
	memMax uint64    // KiB (balloon.maximum)
	at     time.Time // when the sample was taken (for CPU% deltas)
}

var (
	virtMu   sync.Mutex
	virtDims = map[string]virtSample{} // last raw sample per domain
	virtPct  = map[string]float64{}    // last computed CPU% per domain
)

// VMCPUPct returns the last computed CPU% for a domain (0 until 2nd sample).
func VMCPUPct(domain string) float64 {
	virtMu.Lock()
	defer virtMu.Unlock()
	return virtPct[domain]
}

// VMStats returns memory numbers (GiB) for a domain and advances the CPU
// sample: cum. cpu.time (ns) compared against the previous fetch yields %.
func VMStats(domain string) (memCurGiB, memMaxGiB float64) {
	out, err := RunTO(10*time.Second, "virsh",
		VirshArgs("domstats", "--cpu-total", "--balloon", domain)...)
	if err != nil {
		return 0, 0
	}
	var st virtSample
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "cpu.time":
			st.cpuNs, _ = strconv.ParseUint(v, 10, 64)
		case "balloon.current":
			st.memCur, _ = strconv.ParseUint(v, 10, 64)
		case "balloon.maximum":
			st.memMax, _ = strconv.ParseUint(v, 10, 64)
		}
	}
	if st.memMax == 0 {
		return 0, 0 // domain not running / domstats came up empty
	}
	virtMu.Lock()
	if prev, ok := virtDims[domain]; ok && st.cpuNs >= prev.cpuNs && !prev.at.IsZero() {
		if dt := time.Since(prev.at).Seconds(); dt > 0.2 { // ignore back-to-back fetches
			virtPct[domain] = float64(st.cpuNs-prev.cpuNs) / dt / 1e7 // ns/sec -> % of one core
		}
	}
	st.at = time.Now()
	virtDims[domain] = st
	virtMu.Unlock()
	return float64(st.memCur) / (1 << 20), float64(st.memMax) / (1 << 20)
}

// VirshStart / VirshStop use the kit CLI for identical semantics (ACPI dance).
func VMStart(name string, backend string) error {
	args := []string{"start", name}
	return runKitVM(backend, args)
}

func VMStop(name string, backend string) error {
	args := []string{"stop", name}
	return runKitVM(backend, args)
}

// VMRemove fully deletes a VM through the kit's own destroy semantics
// (`sbxvm.sh destroy NAME --yes`): container + disk + snapshots for the
// docker backend, destroy/snapshot-delete/undefine + files for libvirt.
// Destructive by design — callers own confirmation (API confirm flag / CLI
// --yes; the kit script itself also refuses non-interactive runs).
func VMRemove(name string, backend string) error {
	if err := runKitVM(backend, []string{"destroy", name, "--yes"}); err != nil {
		return err
	}
	audit.Record("sandbox.rm", name, fmt.Sprintf("VM destroyed (%s backend, disk + snapshots removed)", backend))
	return nil
}

// VMAlive reports engine-level existence of a sandbox VM (docker backend:
// its sbxvmd-* container; libvirt: the sbxvm-* domain). Engine errors read
// as "not alive" — create flows surface the engine problem separately.
func VMAlive(name, backend string) bool {
	alive, err := EngineAlive("vm", name, backend)
	return err == nil && alive
}

// LibvirtSerialCapture reports whether the sbxvm-<name> domain carries a
// file-backed serial device, i.e. whether its console output is being
// captured into vm/vms/<name>/serial.log at all. Domains created before
// serial capture was wired on the daemon side answer false — the dashboard
// Logs tab then says so honestly instead of streaming an eternal empty file.
// Matched against `type='file'`, not `type="file"`, and only within the
// <serial> element (disks are file-typed too).
func LibvirtSerialCapture(name string) bool {
	out, err := Run("virsh", VirshArgs("dumpxml", env.VMStdPrefix+name)...)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "<serial") && strings.Contains(line, `type='file'`) {
			return true
		}
	}
	return false
}

// VMJournalStream tails the guest's systemd journal through the kit's own
// ssh channel (`sbxvm.sh ssh NAME -- journalctl -f …`) — backend-independent,
// since the kit resolves the ssh endpoint itself (libvirt lease IPs and
// docker port mappings). Works even where the host-side serial capture is
// unreadable (libvirt hosts that run qemu privileged produce root:0600
// capture files the daemon cannot open). Ends when the ssh process exits
// (guest stopped/rebooting/journalctl quirk) or ctx is canceled — the SSE
// caller loops with backoff while the guest boots. Kit/ssh stderr is
// deliberately dropped (banners, connection diagnostics).
func VMJournalStream(ctx context.Context, backend, name string, w io.Writer) error {
	kit := env.Root()
	script := kit + "/vm/sbxvm.sh"
	full := []string{"env", "SBXVM_BACKEND=" + backend, "bash", script,
		"ssh", name, "--", "journalctl", "-f", "-n", "300", "--no-pager"}
	cmd := exec.CommandContext(ctx, full[0], full[1:]...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	_, copyErr := io.Copy(w, stdout)
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
	if copyErr != nil && !errors.Is(copyErr, io.EOF) && ctx.Err() == nil {
		return copyErr
	}
	return nil
}

// EngineAlive answers "does the engine actually still hold this sandbox" —
// collector rows can lag a refresh cycle, and `docker rm -f` treats an
// already-deleted container as success, so a destructive action re-verifies
// before claiming anything happened. Used by the delete route for honest
// 404s on double deletes.
func EngineAlive(kind, name, backend string) (bool, error) {
	notFound := func(out string) bool {
		low := strings.ToLower(out)
		return strings.Contains(low, "no such") ||
			strings.Contains(low, "not found") ||
			strings.Contains(low, "failed to get domain")
	}
	switch kind {
	case "container":
		out, err := RunTO(5*time.Second, "docker", "inspect", "-f", "{{.State.Status}}", name)
		if err != nil {
			if notFound(out) {
				return false, nil // engine answered: it is gone
			}
			return false, wrapErr(err, out) // engine itself failed
		}
		return true, nil
	default: // vm
		if backend == "docker" {
			out, err := RunTO(5*time.Second, "docker", "inspect", "-f", "{{.State.Status}}", env.VMDockerPrefix+name)
			if err != nil {
				if notFound(out) {
					return false, nil
				}
				return false, wrapErr(err, out)
			}
			return true, nil
		}
		out, err := Run("virsh", VirshArgs("dominfo", env.VMStdPrefix+name)...)
		if err != nil {
			if notFound(out) {
				return false, nil
			}
			return false, wrapErr(err, out)
		}
		return true, nil
	}
}

// runKitVM shells the kit's sbxvm CLI so semantics stay identical
// (libvirt: ACPI + forced stop; docker backend: docker stop -t 75 …).
func runKitVM(backend string, args []string) error {
	return runKitVMTO(backend, args, 150*time.Second)
}

// runKitVMTO runs a kit verb with a custom timeout — VM create blocks
// until the guest is seeded, provisioned and booted (minutes on first boot).
// extraEnv seeds additional variables (e.g. SBXVM_SERIAL_FILE=1) into the
// kit call.
func runKitVMTO(backend string, args []string, to time.Duration, extraEnv ...string) error {
	kit := env.Root()
	script := kit + "/vm/sbxvm.sh"
	full := []string{"env", "SBXVM_BACKEND=" + backend}
	full = append(full, extraEnv...)
	full = append(full, "bash", script)
	full = append(full, args...)
	_, err := RunTO(to, full[0], full[1:]...)
	return err
}

// VMCreate provisions and boots a new VM through the kit's own pipeline
// (`sbxvm.sh create NAME --cpu N --mem MB --disk GB [--share DIR]`).
// Blocking by design: the kit handles image seeding, provisioning and boot
// end-to-end. share "" = VM-local /workspace only (kit default).
// SBXVM_SERIAL_FILE=1 makes the kit attach a file-backed serial device so
// the guest console is captured to vm/vms/<name>/serial.log — the dashboard
// Logs tab tails that capture (docker-backed VMs capture by design inside
// the sbxvmd image; this flag covers the libvirt backend).
func VMCreate(name, backend string, cpu, memMB, diskGB int, share string) error {
	args := []string{"create", name,
		"--cpu", strconv.Itoa(cpu),
		"--mem", strconv.Itoa(memMB),
		"--disk", strconv.Itoa(diskGB)}
	if s := strings.TrimSpace(share); s != "" {
		args = append(args, "--share", s)
	}
	if err := runKitVMTO(backend, args, 15*time.Minute, "SBXVM_SERIAL_FILE=1"); err != nil {
		return err
	}
	shareTxt := strings.TrimSpace(share)
	if shareTxt == "" {
		shareTxt = "(vm-local /workspace)"
	}
	audit.Record("sandbox.spawn", name, fmt.Sprintf("VM created (%s backend, cpu=%d mem=%dMB disk=%dGB share=%s)", backend, cpu, memMB, diskGB, shareTxt))
	return nil
}
