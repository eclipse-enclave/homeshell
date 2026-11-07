// Package cli is the homeshell command-line surface. Each verb lives in its own
// file and self-registers into the command registry from an init(); Dispatch
// routes an invocation to the matching verb. Dispatch is standard-library only
// (no CLI framework), consistent with the daemon's zero-dependency stance
// (docs/ARCHITECTURE.md, "CLI and the client boundary").
package cli

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// Env carries process I/O and the build stamps to a verb. It is the seam
// through which future lifecycle verbs (status, stop) will also receive daemon
// discovery, without changing the Command signature.
type Env struct {
	Version string
	// Commit and Channel identify the build to the self-updater; Channel is
	// empty for builds that do not update themselves.
	Commit         string
	Channel        string
	Stdin          io.Reader
	Stdout, Stderr io.Writer
}

// stdin is how a verb reads a pipe. It tolerates a zero Env — most verbs never
// read stdin, so most Env literals (including every test that predates piped
// input) leave the field nil, and a nil io.Reader would panic on first read
// rather than behave like the empty pipe the caller means.
func (e Env) stdin() io.Reader {
	if e.Stdin == nil {
		return strings.NewReader("")
	}
	return e.Stdin
}

// Command is one CLI verb.
type Command struct {
	Name    string
	Summary string
	Run     func(env Env, args []string) int
	// Hidden leaves the verb out of the usage text: the daemon runs it, not users.
	Hidden bool
}

var registry = map[string]Command{}

// register adds a verb to the registry. Verb files call it from their init(),
// so adding a verb means adding one file.
func register(c Command) { registry[c.Name] = c }

// Dispatch routes a homeshell invocation to a verb and returns the process exit
// code. Running the daemon is the explicit `serve` verb; bare homeshell prints
// usage (docs/ARCHITECTURE.md, "CLI and the client boundary").
func Dispatch(env Env, args []string) int {
	if len(args) == 0 {
		writeUsage(env.Stdout)
		return 0
	}
	switch args[0] {
	case "-h", "--help", "help":
		writeUsage(env.Stdout)
		return 0
	case "-version", "--version":
		fmt.Fprintln(env.Stdout, env.Version)
		return 0
	}
	cmd, ok := registry[args[0]]
	if !ok {
		fmt.Fprintf(env.Stderr, "homeshell: unknown command %q\n\n", args[0])
		writeUsage(env.Stderr)
		return 2
	}
	return cmd.Run(env, args[1:])
}

// writeUsage prints the top-level help text, listing the registered verbs in a
// stable order so a new verb file appears automatically.
func writeUsage(w io.Writer) {
	fmt.Fprint(w, `HomeShell — a lightweight control plane for AI-assisted development.

Usage:
  homeshell <command> [flags]

Commands:
`)
	names := make([]string, 0, len(registry))
	width := 0
	for name, cmd := range registry {
		if cmd.Hidden {
			continue
		}
		names = append(names, name)
		if len(name) > width {
			width = len(name)
		}
	}
	sort.Strings(names)
	// The column is sized from the verbs themselves, the way a group's usage is
	// (writeGroupUsage): a fixed width silently breaks alignment the moment a verb
	// longer than it is registered, which is exactly what adding a file does.
	for _, name := range names {
		fmt.Fprintf(w, "  %-*s  %s\n", width, name, registry[name].Summary)
	}
	fmt.Fprint(w, `
Flags:
  --version   Print the version and exit
  -h, --help  Show this help

Run "homeshell <command> -h" for command-specific flags.
`)
}
