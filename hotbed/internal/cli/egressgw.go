package cli

// `hotbedd egress-gateway` runs inside the strict egress gateway container
// (engine/strict.go): the allowlist HTTP(S) proxy for sandboxes on the
// isolated network, plus plain forwarders (the LLM proxy).

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"hotbed/internal/dnsfence"
	"hotbed/internal/egressproxy"
	"hotbed/internal/engine"
)

func egressGatewayCmd(args []string) error {
	listen := ":" + egressproxy.Port
	var fwd []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--listen":
			if i+1 < len(args) {
				listen = args[i+1]
				i++
			}
		case "--forward": // LISTEN=TARGET
			if i+1 < len(args) {
				fwd = append(fwd, args[i+1])
				i++
			}
		default:
			return fmt.Errorf("usage: hotbedd egress-gateway [--listen ADDR] [--forward LISTEN=TARGET]...")
		}
	}
	for _, f := range fwd {
		l, t, ok := strings.Cut(f, "=")
		if !ok {
			return fmt.Errorf("--forward %q: want LISTEN=TARGET", f)
		}
		go func() {
			if err := egressproxy.Forward(l, t); err != nil {
				fmt.Fprintf(os.Stderr, "hotbed egress-gateway: forward %s: %v\n", f, err)
				os.Exit(1)
			}
		}()
	}
	fmt.Printf("hotbed egress-gateway: allowlist proxy on %s, forwards %v\n", listen, fwd)
	return egressproxy.Serve(listen, egressproxy.New(dnsfence.NewJudge()))
}

// strictLibvirtListener serves the egress proxy on the isolated libvirt
// network's bridge address while that network exists (it appears with
// the first strict guest; checked every 5 s, rebinds after a restart).
func strictLibvirtListener() {
	if !engine.LibvirtUp() {
		return
	}
	h := egressproxy.New(dnsfence.NewJudge())
	bound := ""
	for {
		if ip := engine.StrictLibvirtBridgeIP(); ip != "" && ip != bound {
			addr := net.JoinHostPort(ip, egressproxy.Port)
			if l, err := net.Listen("tcp", addr); err == nil {
				bound = ip
				fmt.Printf("\033[36;1mhotbedd:\033[0m strict egress proxy on %s (isolated libvirt network)\n", addr)
				go func() {
					_ = (&http.Server{Handler: h, ReadHeaderTimeout: 20 * time.Second}).Serve(l)
				}()
			}
		}
		time.Sleep(5 * time.Second)
	}
}
