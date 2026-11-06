package filedialog

import (
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// exitError builds an *exec.ExitError with the given status, the way a real
// helper's failure reaches interpret. os/exec has no constructor for one, so
// this runs a shell that exits with the code we want.
func exitError(t *testing.T, code int, stderr string) error {
	t.Helper()
	command := exec.Command("sh", "-c", "exit "+strconv.Itoa(code))
	command.Stderr = nil // captured into ExitError.Stderr by Output()
	err := command.Run()
	if err == nil {
		t.Fatalf("expected a non-zero exit for code %d", code)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("got %T, want *exec.ExitError", err)
	}
	if exitErr.ExitCode() != code {
		t.Fatalf("exit code = %d, want %d", exitErr.ExitCode(), code)
	}
	// What the helper wrote is what tells a cancel from a script error, and
	// Run() does not populate it (only Output() does), so it is set here.
	exitErr.Stderr = []byte(stderr)
	return err
}

func TestInterpret(t *testing.T) {
	tests := []struct {
		name     string
		kind     helperKind
		output   string
		exitCode int // 0 means "no error"
		stderr   string
		wantPath string
		wantErr  error
	}{
		{name: "chosen path", output: "/home/user/projects\n", wantPath: "/home/user/projects"},
		{name: "path with spaces", output: "/home/user/My Projects\n", wantPath: "/home/user/My Projects"},
		{
			// zenity builds that support multi-select join with "|" even when we
			// asked for one directory; the first entry is the answer.
			name:     "multiple paths keeps the first",
			output:   "/home/user/a|/home/user/b\n",
			wantPath: "/home/user/a",
		},
		{
			// zenity and kdialog exit 1 silently when the user dismisses.
			name:     "exit status 1 with nothing on stderr is a cancel",
			kind:     kindToolkit,
			exitCode: 1,
			wantErr:  ErrCancelled,
		},
		{
			// zenity 4.x prints this on every run, cancel or not. Requiring an
			// empty stderr made each dismissal a 500, and the UI answered that by
			// opening the in-app picker over the dialog the user just dismissed.
			name:     "zenity's transient-parent chatter is still a cancel",
			kind:     kindToolkit,
			exitCode: 1,
			stderr: "Gtk-Message: 09:12:31.447: GtkDialog mapped without a transient parent. " +
				"This is discouraged.",
			wantErr: ErrCancelled,
		},
		{
			// The rest of what a desktop session logs on a healthy run: theme
			// parsing, the accessibility bus, a missing GTK module.
			name:     "other toolkit noise is still a cancel",
			kind:     kindToolkit,
			exitCode: 1,
			stderr: "Gtk-Message: Failed to load module \"canberra-gtk-module\"\n" +
				"(zenity:4711): Gtk-WARNING **: Theme parsing error: gtk.css:12:1: Expected a valid selector\n" +
				"** (zenity:4711): WARNING **: Couldn't connect to accessibility bus",
			wantErr: ErrCancelled,
		},
		{
			// A chooser that never got a window on screen exits 1 too, and that
			// one has to stay a failure so the caller falls back to the in-app
			// picker instead of reporting a dismissal nobody made.
			name:     "a display that never opened is a failure",
			kind:     kindToolkit,
			exitCode: 1,
			stderr: "Unable to init server: Could not connect: Connection refused\n" +
				"(zenity:4711): Gtk-WARNING **: cannot open display: :0",
			wantErr: nil, // checked below: not ErrCancelled, not nil
		},
		{
			// osascript names it, and that is the only exit 1 of its that is one.
			name:     "osascript's cancel is a cancel",
			kind:     kindAppleScript,
			exitCode: 1,
			stderr:   "execution error: User canceled. (-128)",
			wantErr:  ErrCancelled,
		},
		{
			// osascript exits 1 for any script error too: an unresolvable start
			// directory, a refused Automation prompt. Reading those as a cancel
			// would tell the UI the user declined and skip the in-app fallback.
			name:     "osascript's script errors are failures",
			kind:     kindAppleScript,
			exitCode: 1,
			stderr:   "execution error: Finder got an error: File some object wasn't found. (-1728)",
			wantErr:  nil, // checked below: not ErrCancelled, not nil
		},
		{
			// Silence means nothing to AppleScript: only -128 is a dismissal, so
			// a bare exit 1 stays a failure even with nothing on stderr.
			name:     "osascript's silent exit 1 is a failure",
			kind:     kindAppleScript,
			exitCode: 1,
			wantErr:  nil, // checked below: not ErrCancelled, not nil
		},
		{
			name:    "empty output is a cancel",
			kind:    kindToolkit,
			output:  "   \n",
			wantErr: ErrCancelled,
		},
		{
			// Anything else is a real failure and must not look like a cancel,
			// or a broken display would silently do nothing.
			name:     "other exit statuses are failures",
			kind:     kindToolkit,
			exitCode: 2,
			wantErr:  nil, // checked below: not ErrCancelled, not nil
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var runErr error
			if tt.exitCode != 0 {
				runErr = exitError(t, tt.exitCode, tt.stderr)
			}

			path, err := interpret(tt.kind, []byte(tt.output), runErr)

			switch {
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
			case tt.exitCode != 0:
				if err == nil || errors.Is(err, ErrCancelled) {
					t.Fatalf("err = %v, want a real failure", err)
				}
			default:
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if path != tt.wantPath {
					t.Errorf("path = %q, want %q", path, tt.wantPath)
				}
			}
		})
	}
}

func TestDialogCommandsPerPlatform(t *testing.T) {
	tests := []struct {
		goos      string
		startDir  string
		wantAll   []string
		wantKind  helperKind // every candidate for the platform carries it
		wantStart string     // a substring the joined command must contain
	}{
		{
			goos:     "linux",
			startDir: "/home/user/code",
			wantAll:  []string{"zenity", "kdialog"},
			wantKind: kindToolkit,
			// The trailing separator is what makes zenity *enter* the directory.
			wantStart: "--filename=/home/user/code/",
		},
		{
			goos:      "darwin",
			startDir:  "/Users/user/code",
			wantAll:   []string{"osascript"},
			wantKind:  kindAppleScript,
			wantStart: `default location POSIX file "/Users/user/code"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.goos, func(t *testing.T) {
			commands := dialogCommands(tt.goos, tt.startDir)
			if len(commands) != len(tt.wantAll) {
				t.Fatalf("got %d candidates, want %d: %v", len(commands), len(tt.wantAll), commands)
			}
			for i, want := range tt.wantAll {
				if commands[i].argv[0] != want {
					t.Errorf("candidate %d = %q, want %q", i, commands[i].argv[0], want)
				}
				// The cancel convention has to travel with the command: it is
				// what tells a dismissal from a helper that never opened.
				if commands[i].kind != tt.wantKind {
					t.Errorf("candidate %d kind = %d, want %d", i, commands[i].kind, tt.wantKind)
				}
			}
			joined := strings.Join(commands[0].argv, " ")
			if !strings.Contains(joined, tt.wantStart) {
				t.Errorf("command %q does not carry the start directory (%q)", joined, tt.wantStart)
			}
		})
	}
}

// A dialog must still open when no start directory is known, so no builder may
// emit an empty or dangling argument for one.
func TestDialogCommandsWithoutStartDirectory(t *testing.T) {
	for _, goos := range []string{"linux", "darwin"} {
		t.Run(goos, func(t *testing.T) {
			for _, command := range dialogCommands(goos, "") {
				for i, arg := range command.argv {
					if arg == "" {
						t.Errorf("%s: argument %d is empty: %v", goos, i, command.argv)
					}
				}
				if strings.Contains(strings.Join(command.argv, " "), "--filename=") {
					t.Errorf("%s: emitted a --filename with no directory: %v", goos, command.argv)
				}
			}
		})
	}
}

// Quoting is what keeps a path with a quote in it from becoming script syntax.
func TestScriptQuotingEscapesUserPaths(t *testing.T) {
	if got, want := appleString(`a"b\c`), `"a\"b\\c"`; got != want {
		t.Errorf("appleString = %s, want %s", got, want)
	}
}

// kdialog's start directory is positional and must be a real path. There is no
// shell in this path — the args go straight to exec — so the fallback has to be an
// expanded home directory; a literal "~" would name a directory that does not
// exist, and the dialog would open at nothing.
func TestKdialogFallsBackToAnExpandedHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	args := kdialogArgs("")
	for _, arg := range args {
		if strings.Contains(arg, "~") {
			t.Errorf("args = %v, want no unexpanded tilde (exec runs no shell)", args)
		}
	}
	if len(args) < 3 || args[2] != home {
		t.Errorf("args = %v, want %q as the positional start directory", args, home)
	}
}

// An explicit start directory is passed through as given.
func TestKdialogUsesTheRequestedStartDirectory(t *testing.T) {
	args := kdialogArgs("/home/user/code")
	if len(args) < 3 || args[2] != "/home/user/code" {
		t.Errorf("args = %v, want the requested directory as the positional", args)
	}
}
