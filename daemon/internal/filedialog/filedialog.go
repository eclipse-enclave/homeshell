// Package filedialog opens the host desktop's own folder chooser — the fourth
// hand-off beside internal/terminal, internal/ide and internal/opener (hand
// interaction off, never embed it).
//
// It exists because the in-app folder picker (GET /api/browse) is a list of
// directory names and nothing else: no places sidebar, no typing a path with
// completion, no bookmarks, none of the keyboard handling people already know.
// Asking the desktop for its chooser gets all of that for free, and it is the
// same widget every other application on the machine shows.
//
// The chooser runs on the *daemon's* host, which is the same host as the UI in
// the loopback deployment HomeShell is built for — and the same host
// /api/browse already lists. The API layer refuses the endpoint on a public
// bind for exactly that reason; this package does not know about binds.
//
// Availability is per-host and best-effort: a machine with no dialog tool
// installed reports ErrUnavailable and the caller falls back to the in-app
// picker rather than failing the interaction.
package filedialog

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

var (
	// ErrUnavailable reports that this host has no native folder chooser we know
	// how to drive. Callers fall back to the in-app picker.
	ErrUnavailable = errors.New("no native folder chooser on this host")
	// ErrCancelled reports that the dialog opened and the user dismissed it
	// without choosing. It is a normal outcome, not a failure.
	ErrCancelled = errors.New("folder selection cancelled")
)

// Chooser opens a native folder chooser and returns the chosen directory.
// It is an interface so the API layer can be tested without a desktop.
type Chooser interface {
	// ChooseDirectory blocks until the user chooses a directory or dismisses the
	// dialog, and returns the absolute path chosen. startDir, when non-empty, is
	// where the dialog opens; a host that cannot honor it simply opens elsewhere.
	// Cancelling returns ErrCancelled; a host with no chooser returns
	// ErrUnavailable.
	ChooseDirectory(ctx context.Context, startDir string) (string, error)
}

// OSChooser is the production Chooser: it drives the platform's dialog helper.
type OSChooser struct{}

// NewOSChooser returns the production chooser.
func NewOSChooser() Chooser { return OSChooser{} }

// ChooseDirectory runs the first dialog helper this host has and interprets its
// result. It blocks for as long as the dialog is up, so the caller's context is
// what bounds it: cancelling the context (a disconnected HTTP client, a
// deadline) kills the helper and closes the dialog.
func (OSChooser) ChooseDirectory(ctx context.Context, startDir string) (string, error) {
	for _, candidate := range dialogCommands(runtime.GOOS, startDir) {
		binary, err := exec.LookPath(candidate.argv[0])
		if err != nil {
			continue
		}
		output, runErr := exec.CommandContext(ctx, binary, candidate.argv[1:]...).Output()
		return interpret(candidate.kind, output, runErr)
	}
	return "", ErrUnavailable
}

// interpret turns a helper's output and exit status into a chosen path,
// ErrCancelled, or an error. It is separate from the exec above so every
// platform's cancel convention is unit-testable on any platform.
//
// Cancelling is exit status 1 on both Unix helpers and on osascript, but exit 1
// alone does not mean the same thing for each, so the convention travels with
// the command (dialogCommand.kind) rather than being guessed from what the
// helper happened to print.
func interpret(kind helperKind, output []byte, runErr error) (string, error) {
	chosen := strings.TrimSpace(string(output))
	if runErr != nil {
		stderr := stderrOf(runErr)
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) && exitErr.ExitCode() == 1 && kind.isCancel(stderr) {
			return "", ErrCancelled
		}
		if stderr != "" {
			return "", fmt.Errorf("native folder chooser failed: %w: %s", runErr, stderr)
		}
		return "", fmt.Errorf("native folder chooser failed: %w", runErr)
	}
	if chosen == "" {
		return "", ErrCancelled
	}
	// zenity in multi-select-capable builds separates paths with "|"; we ask for
	// one directory, so keep the first and ignore any separator that appears.
	if index := strings.IndexByte(chosen, '|'); index >= 0 {
		chosen = chosen[:index]
	}
	return chosen, nil
}

// helperKind names a helper's cancel convention.
type helperKind int

const (
	// kindToolkit is zenity and kdialog: a dismissed dialog is exit 1 with
	// nothing on stdout, whatever the toolkit wrote to stderr beside it.
	kindToolkit helperKind = iota
	// kindAppleScript is osascript, which exits 1 for any script error at all
	// and names the cancel when it is one.
	kindAppleScript
)

// isCancel reports whether exit status 1, with this on stderr, is the user
// dismissing the dialog rather than the helper failing to run.
func (kind helperKind) isCancel(stderr string) bool {
	lowered := strings.ToLower(stderr)
	switch kind {
	case kindToolkit:
		// GTK and Qt write diagnostics to stderr on a perfectly ordinary run —
		// zenity 4.x prints "Gtk-Message: ... GtkDialog mapped without a
		// transient parent. This is discouraged." every single time, and hosts
		// add theme-parser and accessibility-bus complaints of their own. Taking
		// an empty stderr as the mark of a cancel therefore turned every real
		// dismissal into a 500, and the UI answered that by opening the in-app
		// picker — exactly the "ignores the dismissal" behavior Browse avoids.
		//
		// So exit 1 *is* the cancel here, and stderr only speaks up to veto it:
		// a chooser that never managed to put a window on screen also exits 1,
		// and that one has to stay a failure so the caller can fall back.
		return !containsAny(lowered, toolkitStartupFailures)
	case kindAppleScript:
		// AppleScript reports a dismissal as error -128; every other exit 1 is a
		// real script error — an unresolvable `default location`, a refused
		// Automation permission prompt — and reading those as "not now" would
		// tell the UI the user declined and skip the in-app fallback.
		return containsAny(lowered, []string{"user canceled", "user cancelled", "-128"})
	default:
		return false
	}
}

// toolkitStartupFailures are the things zenity and kdialog say when they exit 1
// without ever showing a dialog: no display to open, or a display that refused
// them. Lowercase, matched as substrings.
var toolkitStartupFailures = []string{
	"cannot open display",
	"can't open display",
	"failed to open display",
	"unable to init server",
	"no protocol specified",
	"cannot open shared object",
	"fatal io error",
}

func containsAny(haystack string, needles []string) bool {
	for _, needle := range needles {
		if strings.Contains(haystack, needle) {
			return true
		}
	}
	return false
}

func stderrOf(err error) string {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return strings.TrimSpace(string(exitErr.Stderr))
	}
	return ""
}

// --- pure command builders (GOOS-independent, unit-tested on any platform) ---

// dialogCommand is one helper invocation: what to run, and how that helper says
// the user dismissed the dialog.
type dialogCommand struct {
	argv []string
	kind helperKind
}

// dialogCommands lists the helpers to try for the given OS, best first. The
// caller runs the first one present on the host.
func dialogCommands(goos, startDir string) []dialogCommand {
	switch goos {
	case "darwin":
		return []dialogCommand{{argv: []string{"osascript", "-e", darwinScript(startDir)}, kind: kindAppleScript}}
	default:
		return []dialogCommand{
			{argv: zenityArgs(startDir), kind: kindToolkit},
			{argv: kdialogArgs(startDir), kind: kindToolkit},
		}
	}
}

// zenityArgs builds the GTK chooser invocation. --filename is where the dialog
// opens; the trailing separator is what makes zenity treat it as the directory
// to enter rather than a sibling to preselect.
func zenityArgs(startDir string) []string {
	args := []string{"zenity", "--file-selection", "--directory", "--title=Select a project folder"}
	if startDir != "" {
		args = append(args, "--filename="+strings.TrimSuffix(startDir, "/")+"/")
	}
	return args
}

// kdialogArgs builds the Qt chooser invocation, for a KDE host with no zenity.
// The start directory is positional and must be present, so it falls back to
// the user's home when unset.
//
// The home directory is resolved here rather than written as "~": these args go to
// exec, which runs no shell, so a tilde would reach kdialog as the name of a
// directory called "~" — which does not exist, and which kdialog opens nothing for.
// Only a shell expands it, and there is deliberately no shell in this path.
func kdialogArgs(startDir string) []string {
	if startDir == "" {
		startDir = homeDir()
	}
	return []string{"kdialog", "--getexistingdirectory", startDir, "--title", "Select a project folder"}
}

// homeDir is the user's home directory, or the filesystem root for a host that
// cannot say. kdialog needs *a* directory to open at, and "/" is one that always
// exists — a worse starting point than home, but not a dialog that fails to open.
func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "/"
	}
	return home
}

// darwinScript builds the AppleScript that shows the macOS folder chooser and
// prints the POSIX path of the result. Cancelling raises an AppleScript error,
// which osascript reports as exit status 1 — the cancel convention interpret
// relies on.
func darwinScript(startDir string) string {
	choose := "choose folder with prompt " + appleString("Select a project folder")
	if startDir != "" {
		choose += " default location POSIX file " + appleString(startDir)
	}
	return "POSIX path of (" + choose + ")"
}

// appleString quotes a value as an AppleScript literal. Backslashes are escaped
// before quotes, which would otherwise be double-escaped — matching
// internal/terminal.darwinScript and internal/notify.appleString.
func appleString(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return `"` + value + `"`
}
