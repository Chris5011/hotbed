package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"hotbed/internal/audit"
	"hotbed/internal/env"
)

// Container mount maintenance.
//
// Docker cannot attach or detach binds on a running container, so mount
// changes RECREATE the sandbox container: stop → rm → create with the same
// image/env/ports/labels/volumes plus the new bind set → start.
// libvirt VMs are not touched here (they get mounts via provisioning
// recipes); docker-backend VM containers (hotbedvmd-*) are refused because
// their writable layer may hold live VM state that recreation would lose.

// Bind is one host path mounted into a container (bind, not volume).
type Bind struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Mode   string `json:"mode,omitempty"` // "rw" or "ro"
}

func (b Bind) dockerSpec() string {
	s := b.Source + ":" + b.Target
	if b.Mode != "" && b.Mode != "rw" {
		s += ":" + b.Mode
	}
	return s
}

// ParseBind validates "HOST:CT[:ro]" (extra options like "rw,z" are
// normalized to plain rw/ro: the kit's sandboxes never need SELinux flags).
func ParseBind(spec string) (Bind, error) {
	parts := strings.Split(spec, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return Bind{}, fmt.Errorf("mount spec must be HOST:CT[:ro] (got %q)", spec)
	}
	b := Bind{Source: parts[0], Target: parts[1], Mode: "rw"}
	if len(parts) == 3 {
		m := strings.ToLower(strings.TrimSpace(parts[2]))
		if i := strings.Index(m, ","); i >= 0 {
			m = m[:i] // "ro,z" → "ro"
		}
		if m != "rw" && m != "ro" {
			return Bind{}, fmt.Errorf("unsupported mount mode %q (use ro or rw)", parts[2])
		}
		b.Mode = m
	}
	if !strings.HasPrefix(b.Source, "/") {
		return Bind{}, fmt.Errorf("source must be an absolute host path (got %q)", b.Source)
	}
	if !strings.HasPrefix(b.Target, "/") {
		return Bind{}, fmt.Errorf("container target must be an absolute path (got %q)", b.Target)
	}
	return b, nil
}

// --- docker inspect shapes (only the fields the recreate needs) ---

type rawPortBinding struct {
	HostIP   string `json:"HostIp"`
	HostPort string `json:"HostPort"`
}

type rawHostConfig struct {
	Binds         []string                    `json:"Binds"`
	PortBindings  map[string][]rawPortBinding `json:"PortBindings"`
	RestartPolicy struct {
		Name string `json:"Name"`
	} `json:"RestartPolicy"`
	NetworkMode string `json:"NetworkMode"`
}

type rawCfg struct {
	Image      string            `json:"Image"`
	WorkingDir string            `json:"WorkingDir"`
	User       string            `json:"User"`
	Entrypoint []string          `json:"Entrypoint"`
	Cmd        []string          `json:"Cmd"`
	Env        []string          `json:"Env"`
	Labels     map[string]string `json:"Labels"`
}

type rawMountEntry struct {
	Type        string `json:"Type"`
	Source      string `json:"Source"`
	Destination string `json:"Destination"`
	RW          bool   `json:"RW"`
}

type rawInspect struct {
	Name       string        `json:"Name"`
	Image      string        `json:"Image"`
	Config     rawCfg        `json:"Config"`
	HostConfig rawHostConfig `json:"HostConfig"`
	State      struct {
		Running bool `json:"Running"`
	} `json:"State"`
	Mounts []rawMountEntry `json:"Mounts"`
}

func inspectRaw(name string) (*rawInspect, error) {
	out, err := Run("docker", "inspect", "--format", "{{json .}}", name)
	if err != nil {
		if s := strings.TrimSpace(out); s != "" {
			return nil, fmt.Errorf("docker inspect %s: %s", name, s)
		}
		return nil, fmt.Errorf("docker inspect %s: %w", name, err)
	}
	d := &rawInspect{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), d); err != nil {
		return nil, fmt.Errorf("docker inspect %s: %w", name, err)
	}
	return d, nil
}

func modeOf(rw bool) string {
	if rw {
		return "rw"
	}
	return "ro"
}

// ContainerMounts lists the container's mounts split by kind: binds (host
// paths, controllable) and named volumes (docker-managed, carried over on
// recreate). Anonymous volumes are skipped (docker remakes them empty).
func ContainerMounts(name string) (binds []Bind, volumes []Bind, err error) {
	d, err := inspectRaw(name)
	if err != nil {
		return nil, nil, err
	}
	binds, vols := mountsOf(d)
	return binds, vols, nil
}

func mountsOf(d *rawInspect) ([]Bind, []Bind) {
	var binds, volumes []Bind
	for _, m := range d.Mounts {
		if m.Destination == "" || m.Destination == "/" {
			continue
		}
		b := Bind{Source: m.Source, Target: m.Destination, Mode: modeOf(m.RW)}
		switch m.Type {
		case "bind":
			binds = append(binds, b)
		case "volume":
			if m.Source != "" {
				b.Source = volumeNameOf(m.Source)
				volumes = append(volumes, b)
			}
		}
	}
	sort.Slice(binds, func(i, j int) bool { return binds[i].Target < binds[j].Target })
	sort.Slice(volumes, func(i, j int) bool { return volumes[i].Target < volumes[j].Target })
	return binds, volumes
}

// volumeNameOf normalizes volume sources: some engines report the resolved
// /var/lib/docker/volumes/NAME/_data path instead of the plain volume name.
func volumeNameOf(src string) string {
	const mid = "/var/lib/docker/volumes/"
	if i := strings.Index(src, mid); i >= 0 {
		rest := strings.TrimSuffix(src[i+len(mid):], "/_data")
		if rest != "" && !strings.Contains(rest, "/") {
			return rest
		}
	}
	return src
}

// RebuildWithBinds recreates the container with a new bind set computed by
// transform(current). It stops, removes, re-creates (same image, env, ports,
// labels, restart policy, named volumes) and starts again. Returns the new
// bind list. Per-step progress goes to stderr so slow WSL daemons stay visible.
func RebuildWithBinds(name string, transform func([]Bind) []Bind) ([]Bind, error) {
	d, err := inspectRaw(name)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(name, env.VMDockerPrefix) {
		return nil, fmt.Errorf("refusing to recreate %s: VM-backed container (mounts there come from provisioning/recipes, not live recreation)", name)
	}
	cur, vols := mountsOf(d)
	next := transform(append([]Bind(nil), cur...))
	seen := map[string]bool{}
	for _, b := range next {
		if seen[b.Target] {
			return nil, fmt.Errorf("duplicate mount target %s", b.Target)
		}
		seen[b.Target] = true
	}
	for _, v := range vols {
		if seen[v.Target] {
			return nil, fmt.Errorf("mount target %s collides with an existing named volume", v.Target)
		}
		seen[v.Target] = true
	}

	t0 := time.Now()
	if d.State.Running {
		if err := StopContainer(name, 10); err != nil {
			return nil, fmt.Errorf("stop %s (needed before mount changes) after %s: %w", name, time.Since(t0), err)
		}
		fmt.Fprintf(os.Stderr, "hotbedd: stopped %s in %s\n", name, time.Since(t0))
	}
	t0 = time.Now()
	out, err := Run("docker", "rm", name)
	if err != nil {
		if d.State.Running {
			_, _ = Run("docker", "start", name)
		}
		return nil, wrapErr(err, out)
	}
	fmt.Fprintf(os.Stderr, "hotbedd: removed %s in %s\n", name, time.Since(t0))
	t0 = time.Now()
	if err := createContainer(d, vols, next); err != nil {
		fmt.Fprintf(os.Stderr, "hotbedd: create failed after %s: %v — retrying with the ORIGINAL mounts\n", time.Since(t0), err)
		if e2 := createContainer(d, vols, cur); e2 == nil {
			_ = StartContainer(name)
			return nil, fmt.Errorf("recreate %s failed: %w — container restored with its ORIGINAL mounts", name, err)
		}
		// both attempts failed — container is gone, tell the user how to self-serve
		return nil, fmt.Errorf("recreate %s failed: %w (the old container was removed; `hbd` or docker history can rebuild it)", name, err)
	}
	fmt.Fprintf(os.Stderr, "hotbedd: created %s in %s\n", name, time.Since(t0))
	t0 = time.Now()
	if err := StartContainer(name); err != nil {
		return next, fmt.Errorf("recreated but %s did not start: %w", name, err)
	}
	fmt.Fprintf(os.Stderr, "hotbedd: started %s in %s\n", name, time.Since(t0))
	audit.Record("mounts.set", name, mountsSummary(next))
	return next, nil
}

func mountsSummary(b []Bind) string {
	if len(b) == 0 {
		return "no binds"
	}
	parts := make([]string, 0, len(b))
	for _, m := range b {
		parts = append(parts, m.dockerSpec())
	}
	return strings.Join(parts, " ")
}

// createContainer builds a `docker create` equivalent to the old container
// plus the provided bind set.
func createContainer(d *rawInspect, vols, binds []Bind) error {
	c := d.Config
	hc := d.HostConfig
	args := []string{"create", "--name", strings.TrimPrefix(d.Name, "/")}
	if c.WorkingDir != "" && c.WorkingDir != "/" {
		args = append(args, "--workdir", c.WorkingDir)
	}
	if len(c.Entrypoint) == 1 && c.Entrypoint[0] != "" {
		args = append(args, "--entrypoint", c.Entrypoint[0])
	} // len>1 entrypoints (rare) fall back to the image default
	if c.User != "" && c.User != "root" {
		args = append(args, "--user", c.User)
	}
	for _, e := range c.Env {
		args = append(args, "--env", e)
	}
	for k, v := range c.Labels {
		args = append(args, "--label", k+"="+v)
	}
	if rp := hc.RestartPolicy.Name; rp != "" && rp != "no" {
		args = append(args, "--restart", rp)
	}
	if nm := hc.NetworkMode; nm != "" && nm != "default" && nm != "bridge" {
		args = append(args, "--network", nm)
	}
	for cn, list := range hc.PortBindings {
		for _, pb := range list {
			spec := pb.HostPort + ":" + cn
			if pb.HostIP != "" && pb.HostIP != "0.0.0.0" {
				spec = pb.HostIP + ":" + spec
			}
			args = append(args, "--publish", spec)
		}
	}
	for _, v := range vols {
		spec := "type=volume,source=" + v.Source + ",destination=" + v.Target
		if v.Mode == "ro" {
			spec += ",readonly"
		}
		args = append(args, "--mount", spec)
	}
	for _, b := range binds {
		args = append(args, "--volume", b.dockerSpec())
	}
	args = append(args, c.Image)
	if len(c.Cmd) > 0 {
		args = append(args, c.Cmd...)
	}
	out, err := RunTO(60*time.Second, "docker", args...)
	return wrapErr(err, out)
}
