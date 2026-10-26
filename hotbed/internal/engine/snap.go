package engine

// Snapshot listing for VM sandboxes, driven by the kit's own storage —
// display only. Save/revert/delete stay in the CLI (they stop the VM and
// have their own confirm semantics; see vm/hotbedvm.sh snapshot).

import (
	"fmt"
	"strings"
)

const (
	vmdStore = "hotbedvmd-store" // named volume holding docker-backend qcow2 disks
	vmdImage = "hotbedvmd:latest"
)

// SnapshotRow is one snapshot as the kit stores it: tag plus when.
type SnapshotRow struct {
	Tag  string `json:"tag"`
	Info string `json:"info,omitempty"`
}

// VMSnapshots lists snapshots of a VM sandbox networked through the kernel
// tooling the kit already uses: qemu-img (docker backend, via the hotbedvmd
// image) or virsh (libvirt backend). Never mutates anything.
func VMSnapshots(name, backend string) ([]SnapshotRow, error) {
	var (
		out string
		err error
	)
	switch backend {
	case "docker": // sandbox.BackendDocker (literal: engine can't import sandbox — cycle)
		out, err = RunTO(30, "docker", "run", "--rm", "-v", vmdStore+":/store", vmdImage,
			"qemu-img", "snapshot", "-l", "-U", "/store/vms/"+name+"/disk.qcow2")
		if err != nil {
			// not exists yet / no disk — honest empty for a fresh VM
			if strings.Contains(strings.ToLower(out+err.Error()), "no such file") {
				return nil, nil
			}
			return nil, err
		}
		return parseQemuSnapshotList(out), nil
	case "libvirt": // sandbox.BackendLibvirt
		out, err = RunTO(30, "virsh", "--connect", "qemu:///system",
			"snapshot-list", "hotbedvm-"+name, "--name")
		if err != nil {
			return nil, err
		}
		rows := []SnapshotRow{}
		for _, line := range strings.Split(out, "\n") {
			tag := strings.TrimSpace(line)
			if tag != "" {
				rows = append(rows, SnapshotRow{Tag: tag})
			}
		}
		return rows, nil
	default:
		return nil, fmt.Errorf("unknown VM backend %q", backend)
	}
}

// parseQemuSnapshotList keeps the data lines of `qemu-img snapshot -l`:
// "<id> <tag> <size> <date...>". The first two lines are headers.
func parseQemuSnapshotList(out string) []SnapshotRow {
	rows := []SnapshotRow{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(strings.TrimSpace(line))
		if len(f) < 2 {
			continue
		}
		if !isDigitRun(f[0]) {
			continue // header lines
		}
		row := SnapshotRow{Tag: f[1]}
		if len(f) > 2 {
			row.Info = strings.Join(f[2:], " ")
		}
		rows = append(rows, row)
	}
	return rows
}

func isDigitRun(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
