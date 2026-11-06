// Package terminal launches commands in the user's preferred terminal
// emulator: the caller supplies the command, this package opens the window.
package terminal

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/eclipsesource/homeshell/daemon/internal/procutil"
)

// Launcher opens a command in a terminal emulator window. Tests inject a fake
// to avoid spawning real processes.
type Launcher interface {
	Launch(ctx context.Context, cmd []string, title string) error
}

// Preference is the user's choice of terminal emulator.
type Preference struct {
	// Emulator is Auto, Custom, or the id of one of this platform's Emulators.
	Emulator string `json:"emulator"`
	// Command is the Custom template: a shell line in which CommandPlaceholder
	// stands for the shell-quoted command to run.
	Command string `json:"command"`
}

const (
	// Auto detects the emulator: $TERMINAL, then the first one installed.
	Auto = "auto"
	// Custom runs the Preference's Command template instead of a known
	// emulator. Its windows are untitled.
	Custom = "custom"
	// CommandPlaceholder marks where a Custom template takes the command.
	CommandPlaceholder = "{command}"
)

// Emulator is one terminal emulator this platform can open windows in, as
// clients offer it for choosing.
type Emulator struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Installed bool   `json:"installed"`
}

// EmulatorList is what clients offer for choosing an emulator: this platform's
// emulators, and what Auto opens windows in right now.
type EmulatorList struct {
	// Automatic is the label of the emulator Auto resolves to — a listed one, or
	// whatever else $TERMINAL names — or "" when it resolves to none.
	Automatic string     `json:"automatic"`
	Emulators []Emulator `json:"emulators"`
}

// emulatorSpec is one terminal emulator: how it is found on the host and how it
// is asked to open a window running a command, which each spells differently.
type emulatorSpec struct {
	id     string
	label  string
	locate func() (string, bool)
	// argv is the full command that opens a window running cmd, given what
	// locate found.
	argv func(found string, cmd []string, title string) []string
}

// onPath is an emulator installed as the executable id on PATH, which opens a
// window when run with args(cmd, title).
func onPath(id, label string, args func(cmd []string, title string) []string) emulatorSpec {
	return emulatorSpec{
		id:    id,
		label: label,
		locate: func() (string, bool) {
			found, err := exec.LookPath(id)
			return found, err == nil
		},
		argv: func(found string, cmd []string, title string) []string {
			return append([]string{found}, args(cmd, title)...)
		},
	}
}

// linuxEmulators is walked in order when detecting an emulator; the first one
// installed wins.
var linuxEmulators = []emulatorSpec{
	onPath("gnome-terminal", "GNOME Terminal", func(cmd []string, title string) []string {
		var args []string
		if title != "" {
			args = append(args, "--title="+title)
		}
		args = append(args, "--")
		return append(args, cmd...)
	}),
	onPath("konsole", "Konsole", func(cmd []string, title string) []string {
		var args []string
		if title != "" {
			args = append(args, "-p", "tabtitle="+title)
		}
		args = append(args, "-e")
		return append(args, cmd...)
	}),
	onPath("kitty", "kitty", func(cmd []string, title string) []string {
		if title != "" {
			return append([]string{"--title", title}, cmd...)
		}
		return cmd
	}),
	onPath("alacritty", "Alacritty", func(cmd []string, title string) []string {
		var args []string
		if title != "" {
			args = append(args, "--title", title)
		}
		args = append(args, "-e")
		return append(args, cmd...)
	}),
	onPath("ghostty", "Ghostty", func(cmd []string, title string) []string {
		args := []string{"--gtk-single-instance=false", "--confirm-close-surface=false"}
		if title != "" {
			args = append(args, "--title="+title)
		}
		args = append(args, "-e")
		return append(args, cmd...)
	}),
	// xfce4-terminal's -e takes the command as one string; -x takes the rest.
	onPath("xfce4-terminal", "Xfce Terminal", func(cmd []string, title string) []string {
		var args []string
		if title != "" {
			args = append(args, "--title="+title)
		}
		args = append(args, "-x")
		return append(args, cmd...)
	}),
	// wezterm start takes no title.
	onPath("wezterm", "WezTerm", func(cmd []string, _ string) []string {
		return append([]string{"start", "--"}, cmd...)
	}),
	onPath("foot", "foot", func(cmd []string, title string) []string {
		if title != "" {
			return append([]string{"--title", title}, cmd...)
		}
		return cmd
	}),
	onPath("xterm", "xterm", func(cmd []string, title string) []string {
		var args []string
		if title != "" {
			args = append(args, "-title", title)
		}
		args = append(args, "-e")
		return append(args, cmd...)
	}),
	onPath("x-terminal-emulator", "System default, x-terminal-emulator", func(cmd []string, _ string) []string {
		return append([]string{"-e"}, cmd...)
	}),
}

// darwinEmulators holds Terminal.app, which ships with macOS.
var darwinEmulators = []emulatorSpec{
	{
		id:     "terminal",
		label:  "Terminal",
		locate: func() (string, bool) { return "", true },
		argv: func(_ string, cmd []string, title string) []string {
			return []string{"osascript", "-e", darwinScript(cmd, title)}
		},
	},
}

// emulatorsFor is the emulator table of an operating system (a runtime.GOOS).
func emulatorsFor(goos string) []emulatorSpec {
	if goos == "darwin" {
		return darwinEmulators
	}
	return linuxEmulators
}

// Emulators lists the emulators this platform can open windows in, each marked
// with whether it is installed on this host, and what Auto resolves to.
func Emulators() EmulatorList {
	table := emulatorsFor(runtime.GOOS)
	automatic, _, _ := autoEmulator(runtime.GOOS)
	listed := make([]Emulator, len(table))
	for i, candidate := range table {
		_, installed := candidate.locate()
		listed[i] = Emulator{ID: candidate.id, Label: candidate.label, Installed: installed}
	}
	return EmulatorList{Automatic: automatic.label, Emulators: listed}
}

// Supported reports whether this platform can act on a Preference.Emulator:
// Auto, Custom, or the id of one of its Emulators.
func Supported(emulatorID string) bool {
	if emulatorID == Auto || emulatorID == Custom {
		return true
	}
	_, ok := findEmulator(emulatorsFor(runtime.GOOS), emulatorID)
	return ok
}

func findEmulator(table []emulatorSpec, id string) (emulatorSpec, bool) {
	for _, candidate := range table {
		if candidate.id == id {
			return candidate, true
		}
	}
	return emulatorSpec{}, false
}

// OSLauncher is the production Launcher: it spawns the emulator the user's
// preference names, reading it afresh for every window.
type OSLauncher struct {
	preference func() Preference
}

// NewOSLauncher returns the production terminal launcher.
func NewOSLauncher(preference func() Preference) Launcher {
	return OSLauncher{preference: preference}
}

// Launch opens a new terminal window running cmd, titled where the emulator
// supports it. Started detached: it does not wait for the command to finish.
func (l OSLauncher) Launch(_ context.Context, cmd []string, title string) error {
	if len(cmd) == 0 {
		return fmt.Errorf("empty command")
	}
	argv, err := windowCommand(runtime.GOOS, l.preference(), cmd, title)
	if err != nil {
		return err
	}
	return procutil.StartAndReap(exec.Command(argv[0], argv[1:]...))
}

// windowCommand is the command that opens a window running cmd on goos, in the
// emulator preference names.
func windowCommand(goos string, preference Preference, cmd []string, title string) ([]string, error) {
	switch preference.Emulator {
	case Auto:
		return autoCommand(goos, cmd, title)
	case Custom:
		return customCommand(preference.Command, cmd), nil
	}
	candidate, ok := findEmulator(emulatorsFor(goos), preference.Emulator)
	if !ok {
		return nil, fmt.Errorf("unknown terminal emulator %q", preference.Emulator)
	}
	// Falling back would open a window the user did not choose.
	found, ok := candidate.locate()
	if !ok {
		return nil, fmt.Errorf("the chosen terminal emulator, %s, is not installed", candidate.label)
	}
	return candidate.argv(found, cmd, title), nil
}

// customCommand runs template as a shell line with the shell-quoted cmd in place
// of CommandPlaceholder.
func customCommand(template string, cmd []string) []string {
	return []string{"/bin/sh", "-c", strings.ReplaceAll(template, CommandPlaceholder, shellJoin(cmd))}
}

// autoCommand opens cmd in the emulator Auto resolves to (autoEmulator).
func autoCommand(goos string, cmd []string, title string) ([]string, error) {
	candidate, found, ok := autoEmulator(goos)
	if !ok {
		table := emulatorsFor(goos)
		ids := make([]string, len(table))
		for i, entry := range table {
			ids[i] = entry.id
		}
		return nil, fmt.Errorf("no terminal emulator is installed; install one of %s, set $TERMINAL, or choose a custom command in Settings › Terminal", strings.Join(ids, ", "))
	}
	return candidate.argv(found, cmd, title), nil
}

// autoEmulator is the emulator Auto opens windows in on goos, and what locating
// it found: the one $TERMINAL names, else the first one installed. $TERMINAL is
// read from the daemon's own environment, not the user's shell, and is a Linux
// convention macOS apps do not follow.
func autoEmulator(goos string) (emulatorSpec, string, bool) {
	if goos != "darwin" {
		if candidate, found, ok := terminalEnvEmulator(); ok {
			return candidate, found, true
		}
	}
	for _, candidate := range emulatorsFor(goos) {
		if found, ok := candidate.locate(); ok {
			return candidate, found, true
		}
	}
	return emulatorSpec{}, "", false
}

// terminalEnvEmulator is the emulator $TERMINAL names, when it is set and on
// PATH: a known one as the table has it, anything else by its executable's name
// and given `-e <cmd>`, the near-universal spelling.
func terminalEnvEmulator() (emulatorSpec, string, bool) {
	name := os.Getenv("TERMINAL")
	if name == "" {
		return emulatorSpec{}, "", false
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return emulatorSpec{}, "", false
	}
	if candidate, ok := findEmulator(linuxEmulators, filepath.Base(name)); ok {
		return candidate, path, true
	}
	return emulatorSpec{label: filepath.Base(name), argv: func(found string, cmd []string, _ string) []string {
		return append([]string{found, "-e"}, cmd...)
	}}, path, true
}

// CommandIn returns a command that runs cmd with dir as its working directory,
// by wrapping it in a POSIX `cd … && exec …` line. Every argument is
// shell-quoted, so spaces or quotes cannot break out of it. Unix-only.
func CommandIn(dir string, cmd ...string) []string {
	return []string{"/bin/sh", "-c", fmt.Sprintf("cd %s && exec %s", shellQuote(dir), shellJoin(cmd))}
}

// ShellIn returns the command that opens an interactive shell sitting in dir.
// The shell is $SHELL, resolved by InteractiveShell.
func ShellIn(dir string) []string {
	return CommandIn(dir, InteractiveShell(), "-i")
}

// InteractiveShell is the shell the terminal hand-off drops the user into: the
// daemon's $SHELL where that can actually be executed, else the same shell
// found on PATH, else /bin/sh. $SHELL carries a path that need not exist on
// this host, and a failed exec inside the new window reaches no caller.
func InteractiveShell() string {
	shell := strings.TrimSpace(os.Getenv("SHELL"))
	if shell == "" {
		return "/bin/sh"
	}
	if executable(shell) {
		return shell
	}
	// A stale path: keep the shell, drop the directory it claims to be in.
	if found, err := exec.LookPath(filepath.Base(shell)); err == nil {
		slog.Warn("$SHELL does not exist; using the same shell from PATH",
			"shell", shell, "using", found)
		return found
	}
	slog.Warn("$SHELL does not exist and is not on PATH; falling back",
		"shell", shell, "using", "/bin/sh")
	return "/bin/sh"
}

// executable reports whether path is a regular file this process can run.
func executable(path string) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Mode().Perm()&0o111 != 0
}

// ShellLineIn returns the command that runs a user-authored shell line with dir
// as its working directory. The line is a shell line, not an argv, so only the
// directory is quoted.
//
// On a non-zero exit the window is held open until the user presses a key. The
// emulator closes as soon as its command returns, so without this a mistyped
// line is a window that appears and vanishes with the error inside it.
func ShellLineIn(dir, line string) []string {
	script := fmt.Sprintf("cd %s && ( %s\n ); %s", shellQuote(dir), line, holdOnFailureTail)
	return []string{"/bin/sh", "-c", script}
}

// HoldOnFailure wraps an argv so the window survives a non-zero exit, the same
// way ShellLineIn does: without it the window closes the instant the command
// returns and takes the error with it.
func HoldOnFailure(cmd ...string) []string {
	return []string{"/bin/sh", "-c", shellJoin(cmd) + "; " + holdOnFailureTail}
}

// HoldOnStartupFailureIn runs cmd with dir as its working directory, the held
// counterpart of CommandIn, for a command that turns into an interactive
// session. Only a failure within the first startupWindowSeconds holds the
// window: an interactive shell exits with the status of the last command typed
// into it, so a later non-zero exit is the user closing it, not an error. A
// directory that cannot be entered fails at once and holds too.
func HoldOnStartupFailureIn(dir string, cmd ...string) []string {
	return holdStartupFailureIn(startupWindowSeconds, dir, cmd)
}

// startupWindowSeconds is how long a command must survive before its failure
// counts as the session ending rather than failing to start. It errs long: too
// short brings back the vanishing window, too long costs at most a keypress.
const startupWindowSeconds = 10

// holdStartupFailureIn is HoldOnStartupFailureIn with the window as a
// parameter, so a test can drive the "failed late" branch without waiting it
// out.
func holdStartupFailureIn(windowSeconds int, dir string, cmd []string) []string {
	script := "started=$(date +%s); " +
		fmt.Sprintf("cd %s && %s; ", shellQuote(dir), shellJoin(cmd)) +
		`status=$?; [ "$status" -eq 0 ] || ` +
		fmt.Sprintf("[ $(($(date +%%s) - started)) -ge %d ] || ", windowSeconds) +
		holdPrompt
	return []string{"/bin/sh", "-c", script}
}

// holdOnFailureTail is the script tail that waits for a keypress after a
// non-zero exit, reporting the status.
const holdOnFailureTail = `status=$?; [ "$status" -eq 0 ] || ` + holdPrompt

// holdPrompt reports $status and waits for a keypress.
const holdPrompt = `{ printf '\n[exited %s] press enter to close' "$status"; read -r _; }`

// darwinScript builds the AppleScript that opens a Terminal.app window running
// cmd. Each arg is shell-quoted, then escaped for the `do script "..."` literal:
// backslashes before double quotes, or the escaping would double up.
func darwinScript(cmd []string, title string) string {
	escaped := strings.ReplaceAll(shellJoin(cmd), `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	if title == "" {
		return fmt.Sprintf(
			`tell application "Terminal"
	activate
	do script "%s"
end tell`, escaped)
	}
	escTitle := strings.ReplaceAll(title, `\`, `\\`)
	escTitle = strings.ReplaceAll(escTitle, `"`, `\"`)
	return fmt.Sprintf(
		`tell application "Terminal"
	activate
	set newTab to do script "%s"
	set custom title of newTab to "%s"
end tell`, escaped, escTitle)
}

// shellQuote wraps s in single quotes for shell safety.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// shellJoin shell-quotes each argument and joins them into one command line.
func shellJoin(args []string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = shellQuote(arg)
	}
	return strings.Join(quoted, " ")
}
