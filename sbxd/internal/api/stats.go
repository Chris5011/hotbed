package api

import (
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"sbxd/internal/env"
	"sbxd/internal/sandbox"
)

// statsRoute: GET /api/stats — data for the dashboard's Overview start
// page. Host-level metrics (CPU%, memory, disk, load, uptime, kernel) are
// read straight from /proc and statfs — stdlib only, no gopsutil, no exec.
// Sandbox counts come from the collector's cached snapshot (it refreshes
// itself every 5s; the route never re-scans docker/virsh).
//
// CPU% needs an interval, not a snapshot: a 2s sampler goroutine keeps the
// baseline hot (started lazily on the first request, via double-sample so
// even the very first call reports a real number), and the route itself
// advances the baseline — every caller measures "since the last observation".
func (s *Server) statsRoute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpError(w, 405, "GET only")
		return
	}
	resp := map[string]any{
		"ok":      true,
		"version": env.Version,
		"cpu":     cpuStats(),
		"mem":     memStats(),
		"disk":    diskStats(),
		"host":    hostInfo(),
	}
	if s.Collector != nil {
		resp["sandboxes"] = sandboxCounts(s.Collector.Snapshot())
	}
	okJSON(w, resp)
}

// ------------------------------------------------------------- CPU percent --

type cpuSnapshot struct {
	busy  float64 // busy ticks since boot
	total float64 // all ticks since boot
	last  float64 // percent over the previous interval (0 = unknown yet)
}

var (
	cpuMu     sync.Mutex
	cpuState  cpuSnapshot
	cpuWarm   sync.Once
	statsStop = make(chan struct{})
)

func sampleCPU() (busy, total float64, err error) {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 5 {
			return 0, 0, os.ErrInvalid
		}
		var sum, idle float64
		for i, v := range f[1:] { // skip the "cpu" label
			t, e := strconv.ParseFloat(v, 64)
			if e != nil {
				return 0, 0, e
			}
			sum += t
			if i == 3 || i == 4 { // idle + iowait
				idle += t
			}
		}
		return sum - idle, sum, nil
	}
	return 0, 0, os.ErrNotExist
}

func cpuNow() float64 {
	cpuWarm.Do(func() {
		// First call answers honestly right away: two samples 250ms apart,
		// then keep the baseline hot from a background sampler.
		b1, t1, e1 := sampleCPU()
		time.Sleep(250 * time.Millisecond)
		b2, t2, e2 := sampleCPU()
		cpuMu.Lock()
		if e1 == nil && e2 == nil && t2 > t1 {
			cpuState = cpuSnapshot{
				busy: b2, total: t2,
				last: 100 * (b2 - b1) / (t2 - t1),
			}
		} else if e2 == nil && t2 > 0 {
			cpuState = cpuSnapshot{busy: b2, total: t2}
		}
		cpuMu.Unlock()
		go func() {
			t := time.NewTicker(2 * time.Second)
			defer t.Stop()
			for {
				select {
				case <-statsStop:
					return
				case <-t.C:
					b, tt, err := sampleCPU()
					if err != nil {
						continue
					}
					cpuMu.Lock()
					if cpuState.total > 0 && tt > cpuState.total {
						cpuState.last = 100 * (b - cpuState.busy) / (tt - cpuState.total)
					}
					cpuState.busy, cpuState.total = b, tt
					cpuMu.Unlock()
				}
			}
		}()
	})
	b, t, err := sampleCPU()
	if err != nil {
		// /proc vanished (impossible on Linux): return the sampler's last word.
		cpuMu.Lock()
		defer cpuMu.Unlock()
		return round1(cpuState.last)
	}
	cpuMu.Lock()
	defer cpuMu.Unlock()
	if cpuState.total > 0 && t > cpuState.total {
		cpuState.last = 100 * (b - cpuState.busy) / (t - cpuState.total)
		cpuState.busy, cpuState.total = b, t
	}
	return round1(cpuState.last)
}

func cpuStats() map[string]any {
	loads := loadStats()
	return map[string]any{
		"percent": cpuNow(),
		"cores":   runtime.NumCPU(),
		"load1":   loads[0],
		"load5":   loads[1],
		"load15":  loads[2],
	}
}

func loadStats() [3]float64 {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return [3]float64{}
	}
	f := strings.Fields(string(b))
	out := [3]float64{}
	for i := 0; i < 3 && i < len(f); i++ {
		out[i], _ = strconv.ParseFloat(f[i], 64)
	}
	return out
}

// ------------------------------------------------------------------ memory --

func memStats() map[string]any {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return nil
	}
	vals := map[string]float64{}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		k := strings.TrimSuffix(f[0], ":")
		if v, e := strconv.ParseFloat(f[1], 64); e == nil {
			switch k {
			case "MemTotal", "MemAvailable", "MemFree", "Buffers", "Cached", "SwapTotal", "SwapFree":
				vals[k] = v * 1024 // meminfo is kB
			}
		}
	}
	total := vals["MemTotal"]
	avail := vals["MemAvailable"]
	if avail == 0 { // ancient kernels without MemAvailable
		avail = vals["MemFree"] + vals["Buffers"] + vals["Cached"]
	}
	out := map[string]any{
		"total":        total,
		"available":    avail,
		"used":         total - avail,
		"percent":      round1(pctOf(total-avail, total)),
		"swap_total":   vals["SwapTotal"],
		"swap_used":    vals["SwapTotal"] - vals["SwapFree"],
		"swap_percent": round1(pctOf(vals["SwapTotal"]-vals["SwapFree"], vals["SwapTotal"])),
	}
	return out
}

// -------------------------------------------------------------------- disk --

// diskStats reports the mounts the dashboard cares about: / (host root),
// the kit tree (guest images, sbxvmd store mountpoint), /home when it is a
// separate filesystem, and the docker data root when mounted apart.
// Filesystems are de-duplicated by device id.
func diskStats() []map[string]any {
	kitRoot := env.Root()
	roots := []string{"/", "/home", "/var/lib/docker", kitRoot}
	seen := map[uint64]bool{}
	out := []map[string]any{}
	for _, path := range roots {
		if path == "" {
			continue
		}
		fi, err := os.Stat(path)
		if err != nil || !fi.IsDir() {
			continue
		}
		dev := fi.Sys().(*syscall.Stat_t).Dev
		if seen[dev] {
			continue
		}
		seen[dev] = true
		var st syscall.Statfs_t
		if err := syscall.Statfs(path, &st); err != nil {
			continue
		}
		if st.Blocks == 0 {
			continue
		}
		bsize := uint64(st.Bsize)
		total := st.Blocks * bsize
		used := (st.Blocks - st.Bfree) * bsize
		avail := st.Bavail * bsize
		out = append(out, map[string]any{
			"mount":     path,
			"total":     total,
			"used":      used,
			"available": avail,
			"percent":   round1(100 * float64(used) / float64(total)),
		})
	}
	return out
}

// -------------------------------------------------------------------- host --

func hostInfo() map[string]any {
	up := map[string]any{"kernel": "", "uptime_seconds": 0.0, "cpu_model": ""}
	if b, err := os.ReadFile("/proc/uptime"); err == nil {
		f := strings.Fields(string(b))
		if len(f) > 0 {
			if v, e := strconv.ParseFloat(f[0], 64); e == nil {
				up["uptime_seconds"] = v
			}
		}
	}
	if b, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		up["kernel"] = strings.TrimSpace(string(b))
	}
	if b, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "model name") {
				if _, v, ok := strings.Cut(line, ":"); ok {
					up["cpu_model"] = strings.TrimSpace(v)
					break
				}
			}
		}
	}
	return up
}

// ------------------------------------------------------------- sandbox sum --

// sandboxCounts partitions the cached inventory for the overview cards.
func sandboxCounts(all []sandbox.Sandbox) map[string]any {
	c := map[string]int{}
	for _, sb := range all {
		running := sb.State == "running" || sb.State == "booting"
		switch sb.Kind {
		case "container":
			c["ct_total"]++
			if sb.State == "running" {
				c["ct_run"]++
			}
		case "vm":
			if sb.Backend == "libvirt" {
				c["vl_total"]++
				if running {
					c["vl_run"]++
				}
			} else {
				c["vd_total"]++ // docker backend (encompasses sbxvmd helpers)
				if sb.State == "running" {
					c["vd_run"]++
				}
			}
		}
	}
	return map[string]any{
		"containers":  map[string]int{"running": c["ct_run"], "total": c["ct_total"]},
		"vms_libvirt": map[string]int{"running": c["vl_run"], "total": c["vl_total"]},
		"vms_docker":  map[string]int{"running": c["vd_run"], "total": c["vd_total"]},
	}
}

func round1(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return float64(int(v*10+0.5)) / 10
}

func pctOf(part, total float64) float64 {
	if total <= 0 {
		return 0
	}
	return 100 * part / total
}
