package engine

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
)

// FileTailStream tails a host file (`tail -n 300 -F`) into w. Cancel ctx to stop.
func FileTailStream(ctx context.Context, path string, w io.Writer) error {
	if _, err := os.Stat(path); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "tail", "-n", "300", "-F", path)
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
