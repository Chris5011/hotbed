package engine

// Boot restart: Docker Desktop starts containers with a restart policy as
// soon as its engine is up — on a WSL host that can be before the distro
// has published its files to Docker Desktop's bind-mount mirror. A sandbox
// that bind-mounts a *file* from WSL (e.g. ~/.gitconfig) then fails to
// create ("error mounting … not a directory", exit 127), and docker never
// retries a container that failed to create. Seen on every second reboot.
// At daemon start (WSL is up by then) hotbed starts exactly those again:
// sandboxes with a restart policy whose last start failed on a mount.

import (
	"strings"
	"time"

	"hotbed/internal/audit"
)

// bootRestartable: the last start failed on a bind mount and docker was
// told to keep the container running. A container the user stopped has an
// empty State.Error and is never touched.
func bootRestartable(stateErr, policy string) bool {
	if policy != "unless-stopped" && policy != "always" {
		return false
	}
	e := strings.ToLower(stateErr)
	return strings.Contains(e, "error mounting") || strings.Contains(e, "bind source path does not exist") ||
		(strings.Contains(e, "mount") && (strings.Contains(e, "not a directory") || strings.Contains(e, "no such file or directory")))
}

// BootRestartFailedMounts retries such sandboxes for a few minutes (WSL may
// still be publishing its paths) and reports what it started. Background.
func BootRestartFailedMounts(logf func(string)) {
	pending := map[string]bool{}
	for attempt := 0; attempt < 8; attempt++ {
		if attempt > 0 {
			time.Sleep(20 * time.Second)
		}
		for _, label := range []string{envLabel, "dev.sbx"} {
			out, err := RunTO(15*time.Second, "docker", "ps", "-a", "--filter", "label="+label,
				"--filter", "status=exited", "--filter", "status=created", "--format", "{{.Names}}")
			if err != nil {
				continue
			}
			for _, n := range strings.Fields(out) {
				st, err := RunTO(10*time.Second, "docker", "inspect", "--format",
					"{{.HostConfig.RestartPolicy.Name}}|{{.State.Error}}", n)
				if err != nil {
					continue
				}
				policy, serr, _ := strings.Cut(strings.TrimSpace(st), "|")
				if bootRestartable(serr, policy) {
					pending[n] = true
				}
			}
		}
		for n := range pending {
			if o, err := RunTO(60*time.Second, "docker", "start", n); err != nil {
				if attempt == 7 {
					audit.Record("sandbox.boot-restart", n, "gave up: "+strings.TrimSpace(o))
					logf(n + ": still failing to start on a mount - " + strings.TrimSpace(o))
				}
				continue
			}
			delete(pending, n)
			audit.Record("sandbox.boot-restart", n, "started again (its boot start had failed on a bind mount)")
			logf(n + ": started again (its boot start had failed on a bind mount)")
		}
		if attempt > 0 && len(pending) == 0 {
			return
		}
	}
}
