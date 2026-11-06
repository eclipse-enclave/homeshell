package bridgecli

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/eclipsesource/homeshell/daemon/internal/sbridge"
)

// cmdStop asks a bridge to shut down and waits for it to exit. A
// dead or absent socket is not an error: the bridge is already stopped.
func cmdStop(args []string) int {
	fs := flag.NewFlagSet("stop", flag.ExitOnError)
	sock := fs.String("sock", "", "unix socket of the running bridge (required)")
	timeout := fs.Duration("timeout", sbridge.StopTimeout, "overall stop budget (grace = budget - 2s)")
	_ = fs.Parse(args)
	if *sock == "" {
		fmt.Fprintln(os.Stderr, "usage: homeshell bridge stop --sock PATH [--timeout DUR]")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	if err := sbridge.Stop(ctx, *sock); err != nil {
		fmt.Fprintf(os.Stderr, "sbridge: stop: %v\n", err)
		return 1
	}
	return 0
}
