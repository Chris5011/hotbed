package engine

// VM base images (issue #1): a shut-off VM's disk becomes a standalone,
// generalized qcow2 that new VMs (either backend) boot from —
// `hotbedvm create NAME --image IMG`.
//
// The source VM is never modified: its disk (overlay + master) is
// flattened into a copy with `qemu-img convert`, and only the copy is
// generalized offline with virt-sysprep (machine-id, ssh host keys, user
// ssh dirs, logs, …), cloud-init's instance state is dropped (new VMs
// provision from their own seed again) and ScrubScript runs inside it.
// A throwaway overlay is then checked with VerifyScript; a leak deletes
// the image.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"hotbed/internal/audit"
	"hotbed/internal/env"
)

// VMImageDir holds the custom VM images (kit tree: libvirt-qemu reads it).
func VMImageDir() string { return filepath.Join(env.Root(), "vm", "images", "custom") }

// VMImage describes one VM base image.
type VMImage struct {
	Name    string `json:"name"`
	From    string `json:"from"`    // source VM
	Backend string `json:"backend"` // backend of the source (either can boot it)
	Created string `json:"created"`
	SizeMB  int64  `json:"size_mb"`
}

// SaveVMImage generalizes a shut-off VM into VMImageDir()/<name>.qcow2.
func SaveVMImage(vm, name string, logf func(string)) ([]string, error) {
	if !ValidImageName(name) {
		return nil, fmt.Errorf("image name %q: lowercase letters, digits, . _ - (start with a letter, max 40)", name)
	}
	dir := VMImageDir()
	out := filepath.Join(dir, name+".qcow2")
	if _, err := os.Stat(out); err == nil {
		return nil, fmt.Errorf("VM image %s already exists - remove it first", name)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	_ = os.Chmod(dir, 0o755)
	tmp := filepath.Join(dir, "."+name+".tmp.qcow2")
	defer os.Remove(tmp)

	backend := ""
	switch {
	case LibvirtUp() && libvirtDomainExists(env.VMStdPrefix+vm):
		backend = "libvirt"
		st, _ := RunTO(10*time.Second, "virsh", VirshArgs("domstate", env.VMStdPrefix+vm)...)
		if !strings.Contains(st, "shut off") {
			return nil, fmt.Errorf("%s is %s - shut it down first (a running disk is not consistent)", vm, strings.TrimSpace(st))
		}
		src := filepath.Join(env.Root(), "vm", "vms", vm, "disk.qcow2")
		logf("flattening " + vm + "'s disk (overlay + base) into a standalone copy")
		if o, err := RunTO(30*time.Minute, "qemu-img", "convert", "-O", "qcow2", "-U", src, tmp); err != nil {
			return nil, fmt.Errorf("qemu-img convert: %w: %s", err, strings.TrimSpace(o))
		}
	default:
		ct := env.VMDockerPrefix + vm
		run, err := RunTO(10*time.Second, "docker", "inspect", "--format", "{{.State.Running}}", ct)
		if err != nil {
			return nil, fmt.Errorf("VM %s not found (no libvirt domain, no %s container)", vm, ct)
		}
		if strings.TrimSpace(run) == "true" {
			return nil, fmt.Errorf("%s is running - stop it first (a running disk is not consistent)", vm)
		}
		backend = "docker"
		logf("flattening " + vm + "'s disk out of the hotbedvmd store")
		if o, err := RunTO(30*time.Minute, "docker", "run", "--rm", "-v", vmdStore+":/store:ro", "-v", dir+":/out",
			"--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), "--entrypoint", "qemu-img", vmdImage,
			"convert", "-O", "qcow2", "-U", "/store/vms/"+vm+"/disk.qcow2", "/out/"+filepath.Base(tmp)); err != nil {
			return nil, fmt.Errorf("qemu-img convert (hotbedvmd store): %w: %s", err, strings.TrimSpace(o))
		}
	}

	logf("generalizing (virt-sysprep: machine-id, ssh host keys, cloud-init state, secrets)")
	script := filepath.Join(dir, "."+name+".scrub.sh")
	if err := os.WriteFile(script, []byte(ScrubScript()+vmIdentityReset), 0o600); err != nil {
		return nil, err
	}
	defer os.Remove(script)
	sp := exec.Command("virt-sysprep", "-a", tmp, "--operations", "defaults",
		"--delete", "/var/lib/cloud/instances", "--delete", "/var/lib/cloud/instance",
		"--delete", "/var/lib/cloud/data", "--delete", "/var/log/cloud-init*.log")
	sp.Env = append(os.Environ(), "LIBGUESTFS_BACKEND=direct")
	if so, err := runCmdTO(sp, 30*time.Minute); err != nil {
		return nil, fmt.Errorf("virt-sysprep: %w: %s", err, tailLines(so, 6))
	}
	// the scrub runs through guestfish, whose `sh` hands back the output
	// (virt-sysprep --run swallows it: no list of scrubbed files)
	gf := exec.Command("guestfish", "-a", tmp, "-i", "upload", script, "/root/.hotbed-scrub.sh", ":",
		"sh", "sh /root/.hotbed-scrub.sh; rm -f /root/.hotbed-scrub.sh")
	gf.Env = append(os.Environ(), "LIBGUESTFS_BACKEND=direct")
	so, err := runCmdTO(gf, 30*time.Minute)
	if err != nil {
		return nil, fmt.Errorf("scrub (guestfish): %w: %s", err, tailLines(so, 6))
	}
	var scrubbed []string
	for _, l := range strings.Split(so, "\n") {
		if p, ok := strings.CutPrefix(strings.TrimSpace(l), "SCRUBBED "); ok {
			scrubbed = append(scrubbed, p)
		}
	}
	if leaks, err := verifyVMImage(tmp); err != nil || len(leaks) > 0 {
		if err == nil {
			err = fmt.Errorf("secrets still present (%s)", strings.Join(leaks, ", "))
		}
		return scrubbed, fmt.Errorf("verification of %s: %w - not saving", name, err)
	}
	if err := os.Rename(tmp, out); err != nil {
		return scrubbed, err
	}
	_ = os.Chmod(out, 0o644)
	meta, _ := json.MarshalIndent(VMImage{Name: name, From: vm, Backend: backend,
		Created: time.Now().UTC().Format(time.RFC3339)}, "", "  ")
	_ = os.WriteFile(filepath.Join(dir, name+".json"), append(meta, '\n'), 0o644)
	audit.Record("image.save-vm", name, fmt.Sprintf("from %s (%s backend, %d file(s) scrubbed)", vm, backend, len(scrubbed)))
	return scrubbed, nil
}

// vmIdentityReset runs last inside the copy: virt-sysprep empties the
// machine-id first, but running guest commands afterwards (the scrub) left
// a fresh one behind — every VM from the image then shared it (verified).
// Empty (not missing) makes systemd generate a new id on each first boot;
// dbus falls back to /etc/machine-id.
const vmIdentityReset = `: > /etc/machine-id
rm -f /var/lib/dbus/machine-id
`

// vmIdentityCheck flags an image that would clone its identity.
const vmIdentityCheck = `[ -s /etc/machine-id ] && echo "LEAK /etc/machine-id (identity not reset)"
[ -s /var/lib/dbus/machine-id ] && echo "LEAK /var/lib/dbus/machine-id (identity not reset)"
ls /etc/ssh/ssh_host_*_key 2>/dev/null | sed 's/^/LEAK /'
true
`

// verifyVMImage runs VerifyScript on a throwaway overlay of img.
func verifyVMImage(img string) ([]string, error) {
	ov := img + ".verify.qcow2"
	defer os.Remove(ov)
	if o, err := RunTO(time.Minute, "qemu-img", "create", "-f", "qcow2", "-F", "qcow2", "-b", img, ov); err != nil {
		return nil, fmt.Errorf("overlay: %w: %s", err, strings.TrimSpace(o))
	}
	vs := img + ".verify.sh"
	if err := os.WriteFile(vs, []byte(VerifyScript()+vmIdentityCheck), 0o600); err != nil {
		return nil, err
	}
	defer os.Remove(vs)
	c := exec.Command("guestfish", "-a", ov, "-i", "upload", vs, "/tmp/hotbed-verify.sh", ":", "sh", "sh /tmp/hotbed-verify.sh")
	c.Env = append(os.Environ(), "LIBGUESTFS_BACKEND=direct")
	out, err := runCmdTO(c, 15*time.Minute)
	if err != nil {
		return nil, fmt.Errorf("guestfish: %w: %s", err, tailLines(out, 4))
	}
	return leaksIn(out, nil), nil
}

func libvirtDomainExists(dom string) bool {
	_, err := RunTO(10*time.Second, "virsh", VirshArgs("dominfo", dom)...)
	return err == nil
}

func runCmdTO(c *exec.Cmd, to time.Duration) (string, error) {
	done := make(chan struct{})
	var out []byte
	var err error
	go func() { out, err = c.CombinedOutput(); close(done) }()
	select {
	case <-done:
		return string(out), err
	case <-time.After(to):
		if c.Process != nil {
			_ = c.Process.Kill()
		}
		<-done
		return string(out), fmt.Errorf("timed out after %s", to)
	}
}

// ListVMImages lists the VM base images.
func ListVMImages() []VMImage {
	files, _ := filepath.Glob(filepath.Join(VMImageDir(), "*.qcow2"))
	var list []VMImage
	for _, f := range files {
		n := strings.TrimSuffix(filepath.Base(f), ".qcow2")
		if strings.HasPrefix(n, ".") {
			continue // in progress
		}
		im := VMImage{Name: n}
		if b, err := os.ReadFile(filepath.Join(VMImageDir(), n+".json")); err == nil {
			_ = json.Unmarshal(b, &im)
		}
		if fi, err := os.Stat(f); err == nil {
			im.SizeMB = fi.Size() >> 20
		}
		list = append(list, im)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return list
}

// VMImageExists reports whether a VM base image is present.
func VMImageExists(name string) bool {
	_, err := os.Stat(filepath.Join(VMImageDir(), name+".qcow2"))
	return ValidImageName(name) && err == nil
}

// RemoveVMImage deletes a VM base image unless a VM still boots from it.
func RemoveVMImage(name string) error {
	if !VMImageExists(name) {
		return fmt.Errorf("VM image %s not found", name)
	}
	f := filepath.Join(VMImageDir(), name+".qcow2")
	var users []string
	disks, _ := filepath.Glob(filepath.Join(env.Root(), "vm", "vms", "*", "disk.qcow2"))
	for _, d := range disks {
		if o, _ := RunTO(20*time.Second, "qemu-img", "info", "-U", "--output=json", d); strings.Contains(o, f) {
			users = append(users, filepath.Base(filepath.Dir(d)))
		}
	}
	if o, err := RunTO(60*time.Second, "docker", "run", "--rm", "-v", vmdStore+":/store:ro", "--entrypoint", "sh",
		vmdImage, "-c", "for d in /store/vms/*/disk.qcow2; do [ -f \"$d\" ] && qemu-img info -U \"$d\" | grep -q '/store/images/"+name+".qcow2' && basename $(dirname $d); done; true"); err == nil {
		users = append(users, strings.Fields(o)...)
	}
	if len(users) > 0 {
		return fmt.Errorf("VM image %s is the base of %s - delete those VMs first", name, strings.Join(users, ", "))
	}
	if err := os.Remove(f); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(VMImageDir(), name+".json"))
	_, _ = RunTO(60*time.Second, "docker", "run", "--rm", "-v", vmdStore+":/store", "--entrypoint", "rm",
		vmdImage, "-f", "/store/images/"+name+".qcow2")
	audit.Record("image.rm-vm", name, "removed")
	return nil
}
