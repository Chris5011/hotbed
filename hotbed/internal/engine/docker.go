package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"hotbed/internal/audit"
	"hotbed/internal/env"
)

// ContainerRow is one entry of `docker ps --format '{{json .}}'`.
type ContainerRow struct {
	ID        string            `json:"ID"`
	Names     string            `json:"Names"`
	Image     string            `json:"Image"`
	State     string            `json:"State"`
	Status    string            `json:"Status"`
	CreatedAt string            `json:"CreatedAt"`
	Ports     string            `json:"Ports"`
	Labels    map[string]string `json:"Labels"`
}

// ContainerStat is one entry of `docker stats --no-stream --format '{{json .}}'`.
type ContainerStat struct {
	Name    string `json:"Name"`
	CPUPerc string `json:"CPUPerc"`
	MemPerc string `json:"MemPerc"`
	MemUse  string `json:"MemUsage"`
	NetIO   string `json:"NetIO"`
	BlockIO string `json:"BlockIO"`
}

func RunJSON(name string, args ...string) ([]map[string]any, error) {
	return RunJSONTO(15*time.Second, name, args...)
}

func RunJSONTO(to time.Duration, name string, args ...string) ([]map[string]any, error) {
	out, err := RunTO(to, name, args...)
	if err != nil && len(out) == 0 {
		return nil, err
	}
	var rows []map[string]any
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			continue // tolerate stray non-json lines
		}
		rows = append(rows, m)
	}
	return rows, nil
}

// ListSandboxContainers returns all labeled hbd sandboxes (docker): both the
// current dev.hotbed label and the pre-rename dev.sbx label, deduped by
// container name — legacy sandboxes (a live dashboard session, say) stay
// visible and manageable across the v0.5 rename.
func ListSandboxContainers() ([]ContainerRow, error) {
	seen := map[string]bool{}
	var out []ContainerRow
	for _, filter := range []string{"label=" + env.ContainerLabel, "label=" + env.LegacyContainerLabel} {
		rows, err := listDockerRows(filter)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			key := r.Names
			if key == "" { // tolerate echo-free rows
				continue
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, r)
		}
	}
	return out, nil
}

// ListVMDockerContainers returns docker-backend VM containers (hotbedvmd-*).
func ListVMDockerContainers() ([]ContainerRow, error) {
	return listDockerRows("name=" + env.VMDockerPrefix)
}

func listDockerRows(filter string) ([]ContainerRow, error) {
	rows, err := RunJSON("docker", "ps", "-a",
		"--filter", filter, "--format", "{{json .}}")
	if err != nil {
		return nil, err
	}
	var out []ContainerRow
	for _, m := range rows {
		if r := rowFromMap(m); r != nil {
			out = append(out, *r)
		}
	}
	return out, nil
}

// ContainerHome reports the sandbox user's home dir inside a container.
// v0.4-era images hold user "sbx" (/home/sbx); v0.5+ images hold "hbd"
// (/home/hbd). Runtime in-container work (attach XDG probes, synced-config
// writes) must hit the home the target container actually has — probing a
// fixed path would silently detach legacy sandboxes from the proxy.
func ContainerHome(container string) string {
	if _, err := Run("docker", "exec", container, "test", "-d", "/home/sbx"); err == nil {
		return "/home/sbx"
	}
	if _, err := Run("docker", "exec", container, "test", "-d", "/home/hbd"); err == nil {
		return "/home/hbd"
	}
	return "/home/hbd" // fresh/future images (and healthy default)
}

// rowFromMap builds a ContainerRow from a raw `docker ps --format '{{json .}}'`
// map, tolerating version drift: Names/Ports can be string or array and this
// Docker Desktop renders Labels as a comma-joined "k=v,k=v" string.
func rowFromMap(m map[string]any) *ContainerRow {
	getStr := func(k string) string {
		switch v := m[k].(type) {
		case string:
			return v
		case []any: // Names/Ports as array in some docker versions
			for _, e := range v {
				if s, ok := e.(string); ok {
					return strings.TrimPrefix(s, "/")
				}
			}
		}
		return ""
	}
	r := &ContainerRow{
		ID:        getStr("ID"),
		Names:     getStr("Names"),
		Image:     getStr("Image"),
		State:     getStr("State"),
		Status:    getStr("Status"),
		CreatedAt: getStr("CreatedAt"),
		Ports:     getStr("Ports"),
		Labels:    map[string]string{},
	}
	switch lv := m["Labels"].(type) {
	case map[string]any:
		for k, v := range lv {
			r.Labels[k], _ = v.(string)
		}
	case string:
		for _, kv := range strings.Split(lv, ",") {
			if k, val, ok := strings.Cut(strings.TrimSpace(kv), "="); ok {
				r.Labels[k] = val
			}
		}
	}
	if r.Names == "" && r.ID == "" {
		return nil
	}
	return r
}

// DockerStats returns one-shot stats for every running container.
// `docker stats` samples every container; on busy Docker Desktop installs
// that can take noticeably longer than a plain ps — generous ceiling.
func DockerStats() map[string]ContainerStat {
	rows, err := RunJSONTO(8*time.Second, "docker", "stats", "--no-stream", "--format", "{{json .}}")
	out := map[string]ContainerStat{}
	if err != nil {
		return out
	}
	for _, m := range rows {
		b, _ := json.Marshal(m)
		var s ContainerStat
		if json.Unmarshal(b, &s) != nil {
			continue
		}
		if s.Name != "" {
			out[s.Name] = s
		}
	}
	return out
}

// ParsePerc strips a trailing % and parses a float ("0.16%" -> 0.16).
func ParsePerc(s string) float64 {
	v, _ := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "%")), 64)
	return v
}

// DockerPort resolves the host port published for containerPort 22 (docker-backend VMs).
func DockerPort(name string) string {
	out, err := Run("docker", "port", name, "22")
	if err != nil {
		return ""
	}
	// "127.0.0.1:5857" or "0.0.0.0:5857\n [[::]]:5857"
	best := ""
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if i := strings.LastIndex(line, ":"); i >= 0 {
			if best == "" || strings.HasPrefix(line, "127.0.0.1") {
				best = line[i+1:]
			}
		}
	}
	return best
}

// Detail summarizes `docker inspect` for the UI.
type Detail struct {
	Name    string            `json:"name"`
	Image   string            `json:"image"`
	State   string            `json:"state"`
	Status  string            `json:"status"`
	Created string            `json:"createdAt"`
	Workdir string            `json:"workdir"`
	Env     []string          `json:"env"` // masked
	Mounts  []Mount           `json:"mounts"`
	Labels  map[string]string `json:"labels"`
}

// Mount is a bind/volume mount.
type Mount struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Type   string `json:"type"`
	RW     bool   `json:"rw"`
}

// InspectContainer collects details for one docker container.
func InspectContainer(name string) (*Detail, error) {
	out, err := RunJSON("docker", "inspect", name)
	if err != nil || len(out) == 0 {
		return nil, fmt.Errorf("inspect %s: not found", name)
	}
	m := out[0]
	detail := &Detail{Labels: map[string]string{}}

	if s, ok := m["Name"].(string); ok {
		detail.Name = strings.TrimPrefix(s, "/")
	}
	if cf, ok := m["Config"].(map[string]any); ok {
		detail.Image, _ = cf["Image"].(string)
		detail.Workdir, _ = cf["WorkingDir"].(string)
		if envList, ok := cf["Env"].([]any); ok {
			for _, e := range envList {
				if s, ok := e.(string); ok {
					detail.Env = append(detail.Env, MaskEnv(s))
				}
			}
		}
		if lb, ok := cf["Labels"].(map[string]any); ok {
			for k, v := range lb {
				s, _ := v.(string)
				detail.Labels[k] = s
			}
		}
	}
	if st, ok := m["State"].(map[string]any); ok {
		detail.State, _ = st["Status"].(string)
		if b, ok := st["StartedAt"].(string); ok {
			detail.Created = b
		}
		detail.Status = detail.State
	}
	if created, ok := m["Created"].(string); ok && detail.Created == "" {
		detail.Created = created
	}
	if ms, ok := m["Mounts"].([]any); ok {
		for _, mm := range ms {
			mm2, _ := mm.(map[string]any)
			mo := Mount{}
			if s, ok := mm2["Type"].(string); ok {
				mo.Type = s
			}
			if s, ok := mm2["Source"].(string); ok {
				mo.Source = s
			}
			if s, ok := mm2["Destination"].(string); ok {
				mo.Target = s
			}
			if b, ok := mm2["RW"].(bool); ok {
				mo.RW = b
			}
			detail.Mounts = append(detail.Mounts, mo)
		}
	}
	return detail, nil
}

var secretKeyRe = []string{"KEY", "TOKEN", "SECRET", "PASSWORD", "PASSWD", "CREDENTIAL"}

// MaskEnv masks the value of env entries whose key looks secret-bearing.
func MaskEnv(v string) string {
	i := strings.Index(v, "=")
	if i <= 0 {
		return v
	}
	key := v[:i]
	up := strings.ToUpper(key)
	for _, s := range secretKeyRe {
		if strings.Contains(up, s) {
			return key + "=<masked>"
		}
	}
	return v
}

// LogsStream streams `docker logs -f` for a container. Cancel ctx to stop.
func LogsStream(ctx context.Context, name string, tail int, w io.Writer) error {
	args := []string{"logs"}
	if tail > 0 {
		args = append(args, "--tail", strconv.Itoa(tail))
	}
	args = append(args, "-f", name)
	cmd := exec.CommandContext(ctx, "docker", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout // merge docker's stderr stream port
	if err := cmd.Start(); err != nil {
		return err
	}
	_, copyErr := io.Copy(w, stdout)
	<-ctx.Done()
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
	if copyErr != nil && !errors.Is(copyErr, io.EOF) && ctx.Err() == nil {
		return copyErr
	}
	return nil
}

// SerialStream tails a docker-backend VM's serial log inside its container.
func SerialStream(ctx context.Context, vmContainer, vmName string, w io.Writer) error {
	args := []string{"exec", vmContainer, "sh", "-c", "tail -n 400 -F /store/vms/" + vmName + "/serial.log"}
	cmd := exec.CommandContext(ctx, "docker", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return err
	}
	_, copyErr := io.Copy(w, stdout)
	<-ctx.Done()
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
	if copyErr != nil && !errors.Is(copyErr, io.EOF) && ctx.Err() == nil {
		return copyErr
	}
	return nil
}

// StopContainer / StartContainer for docker-backed runtimes.
func StopContainer(name string, timeout int) error {
	t := strconv.Itoa(timeout)
	// The stop legitimately takes up to `timeout` seconds (SIGTERM grace) —
	// the exec timeout must exceed it, or we kill the CLI mid-stop and
	// report failure for a stop that the server completes anyway.
	out, err := RunTO(time.Duration(timeout)*time.Second+10*time.Second, "docker", "stop", "-t", t, name)
	if err == nil {
		audit.Record("sandbox.stop", name, fmt.Sprintf("timeout=%ss", t))
	}
	return wrapErr(err, out)
}

func StartContainer(name string) error {
	out, err := Run("docker", "start", name)
	if err == nil {
		audit.Record("sandbox.start", name, "")
	}
	return wrapErr(err, out)
}

// RemoveContainer deletes a sandbox container outright (`docker rm -f`),
// including dev.hotbed's tmux attach-session expectations — the caller handles
// those. Container-layer data is lost; bind mounts, cache volumes and the
// burn journal are not.
func RemoveContainer(name string) error {
	out, err := Run("docker", "rm", "-f", name)
	if err == nil {
		audit.Record("sandbox.rm", name, "container deleted")
	}
	return wrapErr(err, out)
}

func wrapErr(err error, out string) error {
	if err == nil {
		return nil
	}
	o := strings.TrimSpace(out)
	if o != "" {
		return fmt.Errorf("%v: %s", err, o)
	}
	return err
}

var _ = time.Second // keep time import if trimmed
