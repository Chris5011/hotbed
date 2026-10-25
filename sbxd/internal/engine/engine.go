// Package engine wraps the low-level runtime CLIs (docker, virsh, kit scripts)
// that sbxd uses instead of linking SDKs — same philosophy as the bash kit.
package engine

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"time"
)

// Run executes a command with a 15 s timeout and trimmed output.
func Run(name string, args ...string) (string, error) {
	return RunTO(15*time.Second, name, args...)
}

// RunStdin executes a command feeding stdin, with timeout and trimmed output.
func RunStdin(to time.Duration, stdin []byte, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), to)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	if len(stdin) > 0 {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	return strings.TrimRight(string(out), "\n"), err
}

// RunTO executes a command with an explicit timeout and trimmed output.
func RunTO(to time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), to)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return strings.TrimRight(string(out), "\n"), err
}

// LookPath reports whether a binary is available.
func LookPath(bin string) bool {
	_, err := exec.LookPath(bin)
	return err == nil
}

// DockerUp checks that the docker engine answers.
func DockerUp() bool {
	out, err := Run("docker", "info", "--format", "{{.ServerVersion}}")
	if err != nil {
		return false
	}
	return strings.TrimSpace(out) != "" && !strings.Contains(out, "error")
}
