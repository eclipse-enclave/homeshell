package cli

// The hidden `bridge` verb is the session bridge's process side: the daemon
// runs every session's agent under `homeshell bridge serve`, and terminals
// attach with `homeshell bridge attach`. It is the daemon's own executable so
// the bridge always comes from the same build that started it.

import "github.com/eclipsesource/homeshell/daemon/internal/sbridge/bridgecli"

func init() {
	register(Command{
		Name:    "bridge",
		Summary: "Run or attach to a session bridge (used by the daemon)",
		Run:     func(_ Env, args []string) int { return bridgecli.Main(args) },
		Hidden:  true,
	})
}
