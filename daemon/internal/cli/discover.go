package cli

import (
	"context"
	"errors"
	"io/fs"

	"github.com/eclipsesource/homeshell/daemon/internal/apiclient"
	"github.com/eclipsesource/homeshell/daemon/internal/daemonstate"
)

// daemonPhase classifies the daemon a client discovered through the state file.
type daemonPhase int

const (
	daemonNone    daemonPhase = iota // no state file: no daemon has run, or it cleaned up
	daemonStale                      // a state file, but the daemon does not answer
	daemonRunning                    // a state file and a reachable daemon
)

// discover reads the discovery state file and classifies the daemon. Lifecycle
// verbs start here because "is a daemon running, and where?" cannot be answered
// over that daemon's own API (docs/ARCHITECTURE.md, "CLI and the client
// boundary"). A missing file is daemonNone (not an error); a present file is
// daemonRunning when a GET /api/hello probe succeeds and daemonStale otherwise.
// The State is zero for daemonNone and carries the recorded record for the other
// phases.
func discover(ctx context.Context) (daemonstate.State, daemonPhase, error) {
	st, err := daemonstate.Read()
	if errors.Is(err, fs.ErrNotExist) {
		return daemonstate.State{}, daemonNone, nil
	}
	if err != nil {
		return daemonstate.State{}, daemonNone, err
	}
	client := apiclient.New(st.Address, st.Token)
	if _, err := client.Hello(ctx); err != nil {
		return st, daemonStale, nil
	}
	return st, daemonRunning, nil
}
