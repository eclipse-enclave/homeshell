package main

import (
	"os"

	"github.com/eclipsesource/homeshell/daemon/internal/cli"
)

// version is injected at build time via -ldflags (see the root Makefile).
var version = "dev"

func main() {
	os.Exit(cli.Dispatch(cli.Env{
		Version: version,
		Stdin:   os.Stdin,
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
	}, os.Args[1:]))
}
