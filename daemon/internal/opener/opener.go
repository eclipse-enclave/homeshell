// Package opener hands a file to the desktop's own default application — the
// third hand-off beside internal/terminal and the "open with" command runner in
// internal/provider (hand interaction off, never embed it).
//
// It differs from those two in what decides the program: both of them run a
// command someone named — the user's "open with" row, or the shell the terminal
// launcher starts — while this one asks the desktop to open the file with
// whatever the user has associated with its type. That is what "the system text
// editor" means: a machine where .md opens in a plain editor and one where it
// opens in a markdown app are both correct, and neither is ours to pick.
package opener

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"

	"github.com/eclipsesource/homeshell/daemon/internal/procutil"
)

// Opener hands a path to the desktop. Tests inject a fake so a test run never
// pops an application open.
type Opener interface {
	Open(ctx context.Context, path string) error
}

// OSOpener is the production Opener: it shells out to the platform's
// open-with-default-application command.
type OSOpener struct{}

var _ Opener = OSOpener{}

// NewOSOpener returns the production opener.
func NewOSOpener() Opener { return OSOpener{} }

// Open asks the desktop to open path with its default application. It returns
// as soon as the helper starts: these commands hand off to an already-running
// application and exit, so waiting would report nothing useful.
func (OSOpener) Open(_ context.Context, path string) error {
	if path == "" {
		return fmt.Errorf("no path to open")
	}
	name, args := openCommand(runtime.GOOS, path)
	binary, err := exec.LookPath(name)
	if err != nil {
		return fmt.Errorf("no desktop opener (%s) on this host: %w", name, err)
	}
	return procutil.StartAndReap(exec.Command(binary, args...))
}

// openCommand is the platform's "open this with the default application"
// invocation.
func openCommand(goos, path string) (name string, args []string) {
	switch goos {
	case "darwin":
		return "open", []string{path}
	default:
		return "xdg-open", []string{path}
	}
}
