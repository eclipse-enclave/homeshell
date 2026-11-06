package main

import (
	"os"

	"github.com/eclipsesource/homeshell/daemon/internal/cli"
)

// Injected at build time via -ldflags (see the root Makefile).
var (
	version = "dev"
	commit  string
	// channel is the release channel of a standalone release build; empty
	// builds never update themselves.
	channel string
)

func main() {
	os.Exit(cli.Dispatch(cli.Env{
		Version: version,
		Commit:  commit,
		Channel: channel,
		Stdin:   os.Stdin,
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
	}, os.Args[1:]))
}
