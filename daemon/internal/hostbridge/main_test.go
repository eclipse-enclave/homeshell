package hostbridge

import (
	"os"
	"testing"

	"github.com/eclipsesource/homeshell/daemon/internal/sbridge/bridgecli"
)

// TestMain lets a Runtime with no exe set run this test binary as the bridge,
// the way the daemon runs itself.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "bridge" {
		os.Exit(bridgecli.Main(os.Args[2:]))
	}
	os.Exit(m.Run())
}
