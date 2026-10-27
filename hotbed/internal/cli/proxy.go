package cli

// `hotbedd proxy` runs ONLY the token-accounting LLM proxy: no dashboard,
// no collector, no sweeper. Behavior is identical to the proxy inside
// `serve` — config and keys are re-read per request, receipts append to
// the shared usage journal — so it can run standalone in the same state
// dirs, or inside the engine-mounted relay container (see engine/relay.go)
// on Docker Desktop, where engine containers cannot reach the distro's
// own proxy listener.

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"hotbed/internal/config"
	"hotbed/internal/dnsfence"
	"hotbed/internal/env"
	"hotbed/internal/keys"
	"hotbed/internal/ledger"
	"hotbed/internal/proxy"
)

func proxyCmd(_ []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w — run `hotbedd config init`", err)
	}
	if len(cfg.Opencode.Providers) == 0 {
		return fmt.Errorf("no providers configured — run `hotbedd config init` first")
	}
	ks, err := keys.Open()
	if err != nil {
		return fmt.Errorf("keys: %w", err)
	}
	px := proxy.New(cfg, ks, ledger.Open(cfg.Prices))
	httpSrv := &http.Server{Addr: px.Addr(), Handler: px, IdleTimeout: 90 * time.Second, ReadHeaderTimeout: 15 * time.Second}

	// dns fence (egress allowlist): runs in the engine netns here — relay
	// containers CAN bind the bridge-facing port that fenced sandboxes DNAT
	// into. A busy port means another state set's fence already listens;
	// skip with a log line, the feature is single-winner by design.
	go func() {
		if env.DNSFenceDisabled() {
			return
		}
		if err := dnsfence.Run("0.0.0.0", cfg.Proxy.DNSPort); err != nil {
			fmt.Fprintf(os.Stderr, "\033[33;1mhotbedd proxy:\033[0m dns fence not started: %v\n", err)
		}
	}()

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(ctx)
	}()

	fmt.Printf("\033[36;1mhotbedd proxy:\033[0m LLM proxy %s (standalone/relay mode)\n", px.Addr())
	fmt.Printf("\033[36;1mhotbedd proxy:\033[0m config %s · keys %s · receipts %s\n",
		env.ConfigDir(), env.DataDir(), env.StateDir())
	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}
