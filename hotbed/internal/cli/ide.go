package cli

// `hotbedd ide NAME` opens a sandbox in VS Code (issue #2, internal/ide);
// `hotbedd ide-pipe NAME` is the ssh ProxyCommand behind the VM host
// aliases (stdio <-> the guest's sshd).

import (
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"hotbed/internal/agents"
	"hotbed/internal/attach"
	"hotbed/internal/ide"
)

func ideCmd(args []string) error {
	name := firstArg(args)
	if name == "" || strings.HasPrefix(name, "-") {
		return fmt.Errorf("usage: hotbedd ide NAME [--print]   open the sandbox in VS Code (--print: only show the URI)")
	}
	sb := findLocal(name)
	if sb == nil {
		return fmt.Errorf("sandbox %q not found", name)
	}
	port := ""
	if len(sb.Ports) > 0 {
		port = strings.TrimPrefix(sb.Ports[0], "127.0.0.1:")
	}
	var env map[string]string
	if sb.Kind == "container" {
		env = attach.ContainerShellEnv(*sb)
	}
	r, err := ide.Open(*sb, func() (agents.Box, error) { return attach.BoxFor(*sb, port) }, env, !hasFlag(args, "--print"))
	if err != nil {
		return err
	}
	if r.Host != "" {
		ok("ssh host %s ready (hotbed.conf, included from your ssh config)\n", r.Host)
	}
	fmt.Println(r.URI)
	switch {
	case r.Missing != "":
		fmt.Printf("\033[33;1mhotbedd:\033[0m VS Code lacks the %s extension - install it once:\n  %s\n", r.Missing, r.Hint)
	case r.Launched:
		ok("VS Code opening %s\n", ide.Folder)
	default:
		fmt.Printf("open with: code --folder-uri %s\n", r.URI)
	}
	return nil
}

func idePipeCmd(args []string) error {
	name := firstArg(args)
	if name == "" {
		return fmt.Errorf("usage: hotbedd ide-pipe NAME   (ssh ProxyCommand)")
	}
	target, err := ide.PipeTarget(name)
	if err != nil {
		return err
	}
	c, err := net.DialTimeout("tcp", target, 15*time.Second)
	if err != nil {
		return err
	}
	go func() {
		_, _ = io.Copy(c, os.Stdin)
		if tc, ok := c.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
	}()
	_, _ = io.Copy(os.Stdout, c) // guest closed: done (stdin may stay open)
	return nil
}
