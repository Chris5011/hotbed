package api

// POST /api/sandboxes/<kind>/<name>/ide {"launch": true} — open the
// sandbox in VS Code (issue #2, internal/ide). Answers the folder URI, the
// ssh host alias (VMs) and, when VS Code lacks the needed extension, the
// command that installs it (hotbed never installs it itself).

import (
	"encoding/json"
	"io"
	"net/http"

	"hotbed/internal/agents"
	"hotbed/internal/attach"
	"hotbed/internal/audit"
	"hotbed/internal/ide"
	"hotbed/internal/sandbox"
)

func (s *Server) ideRoute(w http.ResponseWriter, r *http.Request, sb sandbox.Sandbox) {
	if r.Method != http.MethodPost {
		httpError(w, 405, "POST only")
		return
	}
	var in struct {
		Launch bool `json:"launch"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&in)
	var env map[string]string
	if sb.Kind == sandbox.KindContainer {
		env = attach.ContainerShellEnv(sb)
	}
	res, err := ide.Open(sb, func() (agents.Box, error) { return attach.BoxFor(sb, sshPort(sb)) }, env, in.Launch)
	if err != nil {
		httpError(w, 500, err.Error())
		return
	}
	if res.Launched {
		audit.Record("ide.open", sb.Name, res.URI)
	}
	okJSON(w, map[string]any{"ok": true, "ide": res})
}
