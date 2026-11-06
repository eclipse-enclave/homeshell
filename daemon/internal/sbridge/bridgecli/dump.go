package bridgecli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/sbridge"
)

// dumpTimeout bounds a dump so a wedged bridge degrades to an error.
const dumpTimeout = 3 * time.Second

// cmdDump prints one snapshot from a running bridge as JSON.
func cmdDump(args []string) int {
	fs := flag.NewFlagSet("dump", flag.ExitOnError)
	sock := fs.String("sock", "", "unix socket of the running bridge (required)")
	_ = fs.Parse(args)
	if *sock == "" {
		fmt.Fprintln(os.Stderr, "usage: homeshell bridge dump --sock PATH")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), dumpTimeout)
	defer cancel()
	snap, err := sbridge.Dump(ctx, *sock)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sbridge: dump: %v\n", err)
		return 1
	}
	data, _ := json.Marshal(snap)
	fmt.Println(string(data))
	return 0
}
