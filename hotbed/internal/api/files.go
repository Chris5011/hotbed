package api

// GET /api/sandboxes/<kind>/<name>/files?path=/workspace — one directory of
// a running sandbox's filesystem (container: docker exec; VM: ssh), for the
// dashboard's Files tab. Listing only: names, types, sizes, times — never
// file contents, no upload or download. Runs as root inside the sandbox so
// the view shows what is there, not just what the sandbox user may read.

import (
	"fmt"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"hotbed/internal/attach"
	"hotbed/internal/sandbox"
)

const filesMax = 5000 // entries per directory listing

type fileEnt struct {
	Name   string `json:"name"`
	Type   string `json:"type"`           // dir | file | link | other
	Dir    bool   `json:"dir"`            // a directory, or a link to one
	Size   int64  `json:"size"`           // bytes (files)
	MTime  int64  `json:"mtime"`          // unix seconds
	Target string `json:"link,omitempty"` // symlink target
}

// sortEntries: folders first, then visible before hidden, then
// case-insensitive by name (byte order put ".x" and "Z" before "a").
func sortEntries(list []fileEnt) {
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if a.Dir != b.Dir {
			return a.Dir
		}
		ah, bh := strings.HasPrefix(a.Name, "."), strings.HasPrefix(b.Name, ".")
		if ah != bh {
			return !ah
		}
		al, bl := strings.ToLower(a.Name), strings.ToLower(b.Name)
		if al != bl {
			return al < bl
		}
		return a.Name < b.Name
	})
}

// cleanSandboxPath accepts an absolute path and normalizes it.
func cleanSandboxPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		p = "/workspace"
	}
	if !strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\x00\n\r") {
		return "", fmt.Errorf("path must be absolute")
	}
	return path.Clean(p), nil
}

// listScript prints one entry per line: type, type-after-link, size,
// mtime, name, link target - tab separated (GNU find, Debian guests).
func listScript(dir string) string {
	q := "'" + strings.ReplaceAll(dir, "'", `'\''`) + "'"
	return `d=` + q + `
[ -e "$d" ] || { echo "HBDERR no such directory"; exit 0; }
[ -d "$d" ] || { echo "HBDERR not a directory"; exit 0; }
cd "$d" 2>/dev/null || { echo "HBDERR permission denied"; exit 0; }
echo "HBDOK"
find . -mindepth 1 -maxdepth 1 -printf '%y\t%Y\t%s\t%T@\t%f\t%l\n' 2>/dev/null | head -n ` + strconv.Itoa(filesMax+1) + `
`
}

func parseListing(out string) ([]fileEnt, bool, error) {
	lines := strings.Split(strings.ReplaceAll(out, "\r", ""), "\n")
	start := -1
	for i, l := range lines {
		if e, ok := strings.CutPrefix(l, "HBDERR "); ok {
			return nil, false, fmt.Errorf("%s", e)
		}
		if l == "HBDOK" {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return nil, false, fmt.Errorf("listing failed")
	}
	var list []fileEnt
	for _, l := range lines[start:] {
		f := strings.SplitN(l, "\t", 6)
		if len(f) < 5 || f[4] == "" {
			continue
		}
		e := fileEnt{Name: f[4]}
		switch f[0] {
		case "d":
			e.Type = "dir"
		case "f":
			e.Type = "file"
		case "l":
			e.Type = "link"
		default:
			e.Type = "other"
		}
		e.Dir = f[0] == "d" || (f[0] == "l" && f[1] == "d")
		e.Size, _ = strconv.ParseInt(f[2], 10, 64)
		if t, err := strconv.ParseFloat(f[3], 64); err == nil {
			e.MTime = int64(t)
		}
		if len(f) == 6 {
			e.Target = f[5]
		}
		list = append(list, e)
	}
	truncated := len(list) > filesMax
	if truncated {
		list = list[:filesMax]
	}
	sortEntries(list)
	return list, truncated, nil
}

func (s *Server) filesRoute(w http.ResponseWriter, r *http.Request, sb sandbox.Sandbox) {
	if r.Method != http.MethodGet {
		httpError(w, 405, "GET only")
		return
	}
	if sb.State != "running" {
		httpError(w, 409, sb.Name+" is "+sb.State+" - start it to browse its files")
		return
	}
	dir, err := cleanSandboxPath(r.URL.Query().Get("path"))
	if err != nil {
		httpError(w, 400, err.Error())
		return
	}
	box, err := attach.BoxFor(sb, sshPort(sb))
	if err != nil {
		httpError(w, 500, err.Error())
		return
	}
	out, err := box.RunRoot([]byte(listScript(dir)), nil, 30*time.Second)
	if err != nil && !strings.Contains(out, "HBDOK") && !strings.Contains(out, "HBDERR") {
		httpError(w, 502, "listing "+dir+": "+err.Error())
		return
	}
	list, truncated, err := parseListing(out)
	if err != nil {
		httpError(w, 404, dir+": "+err.Error())
		return
	}
	if list == nil {
		list = []fileEnt{}
	}
	parent := ""
	if dir != "/" {
		parent = path.Dir(dir)
	}
	okJSON(w, map[string]any{"path": dir, "parent": parent, "entries": list, "truncated": truncated})
}
