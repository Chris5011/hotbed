package api

// Custom images (issue #1, engine/images*.go) over REST:
//
//	GET  /api/images                       container + VM images, recent jobs
//	POST /api/images/build   {spec}        declarative image (job)
//	POST /api/images/rebuild {"name"}      from the stored spec (job)
//	POST /api/images/save    {"sandbox","kind","name"}  container or shut-off VM (job)
//	POST /api/images/rm      {"name","vm"}
//
// Builds and saves take minutes, so they run as background jobs (one at a
// time per image name) whose state and last log lines the Images page
// polls; jobs live in memory only.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"hotbed/internal/engine"
	"hotbed/internal/sandbox"
)

type imageJob struct {
	ID       string    `json:"id"`
	Kind     string    `json:"kind"` // build | rebuild | save | save-vm
	Name     string    `json:"name"`
	Source   string    `json:"source,omitempty"`
	State    string    `json:"state"` // running | done | failed
	Error    string    `json:"error,omitempty"`
	Log      []string  `json:"log"`
	Scrubbed []string  `json:"scrubbed,omitempty"`
	Started  time.Time `json:"started"`
	Ended    time.Time `json:"ended,omitempty"`
}

var (
	jobsMu sync.Mutex
	jobs   = map[string]*imageJob{}
	jobSeq int
)

// logLine appends to a job's log (last 200 lines kept).
func (j *imageJob) logLine(s string) {
	jobsMu.Lock()
	defer jobsMu.Unlock()
	for _, l := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		j.Log = append(j.Log, l)
	}
	if len(j.Log) > 200 {
		j.Log = j.Log[len(j.Log)-200:]
	}
}

// Write lets build output stream into the job log.
func (j *imageJob) Write(p []byte) (int, error) { j.logLine(string(p)); return len(p), nil }

// startJob runs fn in the background unless a job for name still runs.
func startJob(kind, name, source string, fn func(j *imageJob) ([]string, error)) (*imageJob, error) {
	jobsMu.Lock()
	for _, j := range jobs {
		if j.Name == name && j.State == "running" {
			jobsMu.Unlock()
			return nil, fmt.Errorf("a %s job for %s is already running", j.Kind, name)
		}
	}
	jobSeq++
	j := &imageJob{ID: fmt.Sprintf("j%d", jobSeq), Kind: kind, Name: name, Source: source, State: "running", Started: time.Now()}
	jobs[j.ID] = j
	jobsMu.Unlock()
	go func() {
		scrubbed, err := fn(j)
		jobsMu.Lock()
		defer jobsMu.Unlock()
		j.Scrubbed, j.Ended = scrubbed, time.Now()
		if err != nil {
			j.State, j.Error = "failed", err.Error()
		} else {
			j.State = "done"
		}
	}()
	return j, nil
}

func jobList() []imageJob {
	jobsMu.Lock()
	defer jobsMu.Unlock()
	out := make([]imageJob, 0, len(jobs))
	for _, j := range jobs {
		c := *j
		c.Log = append([]string(nil), j.Log...)
		if len(c.Log) > 30 {
			c.Log = c.Log[len(c.Log)-30:]
		}
		out = append(out, c)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Started.After(out[b].Started) })
	if len(out) > 10 {
		out = out[:10]
	}
	return out
}

func (s *Server) imagesRoute(w http.ResponseWriter, r *http.Request) {
	action := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/images"), "/")
	if action == "" {
		if r.Method != http.MethodGet {
			httpError(w, 405, "GET only")
			return
		}
		cs, err := engine.ListCustomImages()
		if err != nil {
			cs = nil
		}
		okJSON(w, map[string]any{"containers": cs, "vms": engine.ListVMImages(), "jobs": jobList()})
		return
	}
	if r.Method != http.MethodPost {
		httpError(w, 405, "POST only")
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	var job *imageJob
	var err error
	switch action {
	case "build":
		var spec engine.ImageSpec
		if err = json.Unmarshal(body, &spec); err != nil {
			httpError(w, 400, "invalid JSON: "+err.Error())
			return
		}
		if err = spec.Validate(); err != nil {
			httpError(w, 400, err.Error())
			return
		}
		job, err = startJob("build", spec.Name, spec.From, func(j *imageJob) ([]string, error) {
			return nil, engine.BuildCustomImage(spec, j)
		})
	case "rebuild":
		var in struct{ Name string }
		_ = json.Unmarshal(body, &in)
		spec, e := engine.LoadImageSpec(in.Name)
		if e != nil {
			httpError(w, 404, "no stored spec for "+in.Name)
			return
		}
		job, err = startJob("rebuild", spec.Name, spec.From, func(j *imageJob) ([]string, error) {
			return nil, engine.BuildCustomImage(spec, j)
		})
	case "save":
		var in struct{ Sandbox, Kind, Name string }
		_ = json.Unmarshal(body, &in)
		sb, found := s.Collector.Find(in.Kind, in.Sandbox)
		if !found {
			httpError(w, 404, "sandbox "+in.Kind+"/"+in.Sandbox+" not found")
			return
		}
		if !engine.ValidImageName(in.Name) {
			httpError(w, 400, "image name: lowercase letters, digits, . _ - (start with a letter, max 40)")
			return
		}
		if sb.Kind == sandbox.KindVM {
			job, err = startJob("save-vm", in.Name, sb.Name, func(j *imageJob) ([]string, error) {
				return engine.SaveVMImage(sb.Name, in.Name, j.logLine)
			})
		} else {
			job, err = startJob("save", in.Name, sb.Name, func(j *imageJob) ([]string, error) {
				return engine.SaveContainerImage(sb.Name, in.Name, j.logLine)
			})
		}
	case "rm":
		var in struct {
			Name string
			VM   bool
		}
		_ = json.Unmarshal(body, &in)
		if in.VM {
			err = engine.RemoveVMImage(in.Name)
		} else {
			err = engine.RemoveCustomImage(in.Name)
		}
		if err != nil {
			httpError(w, 400, err.Error())
			return
		}
		okJSON(w, map[string]any{"ok": true})
		return
	default:
		httpError(w, 404, "unknown images action "+action)
		return
	}
	if err != nil {
		httpError(w, 409, err.Error())
		return
	}
	okJSON(w, map[string]any{"ok": true, "job": job.ID})
}
