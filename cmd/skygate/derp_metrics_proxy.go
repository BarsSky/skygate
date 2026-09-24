// cmd/skygate/derp_metrics_proxy.go — the `skygate derp-metrics-proxy`
// subcommand (B315).
//
// It is a thin wrapper: parse the flags, install signal handling, and hand the
// real work to internal/derpmetricsproxy. The subcommand exists so the operator
// needs NO new dependency, image or compose edit to close the "derper's metrics
// are 403 from the container" hole — the bridge is inside the artifact they
// already deploy. See internal/derpmetricsproxy for the why.

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"skygate/internal/derpmetricsproxy"
)

// runDerpMetricsProxy runs the loopback metrics bridge until SIGINT/SIGTERM.
//
// A `-h`/`--help` (or a half-finished flag set) prints the usage and exits 0,
// because on the host this is the first thing an operator types; a MISSING
// --listen prints the usage and exits 2 (a real configuration error, so a unit
// restart loop is visible rather than silent).
func runDerpMetricsProxy(args []string) error {
	cfg, err := derpmetricsproxy.ParseArgs(args)
	if err != nil {
		if err == flag.ErrHelp {
			fmt.Print(derpmetricsproxy.Usage)
			return nil
		}
		fmt.Fprint(os.Stderr, derpmetricsproxy.Usage)
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return derpmetricsproxy.Run(ctx, cfg)
}
