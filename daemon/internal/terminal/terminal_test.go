package terminal

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// The payload must stay a well-formed `do script "..."` string literal.
func TestDarwinScriptEscaping(t *testing.T) {
	tests := []struct {
		name string
		cmd  []string
		want string // the escaped payload between do script "..."
	}{
		{
			name: "plain",
			cmd:  []string{"tmux", "attach", "-t", "hs-1"},
			want: `'tmux' 'attach' '-t' 'hs-1'`,
		},
		{
			name: "double quote is escaped",
			cmd:  []string{"echo", `a"b`},
			want: `'echo' 'a\"b'`,
		},
		{
			// shellQuote turns a single quote into '\'', and AppleScript would
			// read that backslash as an escape.
			name: "single quote produces a doubled backslash",
			cmd:  []string{"cd", "/Users/me/My Project's"},
			want: `'cd' '/Users/me/My Project'\\''s'`,
		},
		{
			name: "literal backslash is doubled",
			cmd:  []string{"echo", `a\b`},
			want: `'echo' 'a\\b'`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			script := darwinScript(tt.cmd, "")
			wantLine := `do script "` + tt.want + `"`
			if !strings.Contains(script, wantLine) {
				t.Errorf("script does not contain %q\nfull script:\n%s", wantLine, script)
			}
		})
	}
}

func TestDarwinScriptTitle(t *testing.T) {
	tests := []struct {
		name      string
		cmd       []string
		title     string
		wantTitle string // expected in set custom title line; empty means no title line
	}{
		{
			name:  "no title omits custom title line",
			cmd:   []string{"tmux", "attach", "-t", "hs-1"},
			title: "",
		},
		{
			name:      "plain title is set",
			cmd:       []string{"tmux", "attach", "-t", "hs-1"},
			title:     "feature/self-update",
			wantTitle: "feature/self-update",
		},
		{
			name:      "title with double quote is escaped",
			cmd:       []string{"tmux", "attach", "-t", "hs-1"},
			title:     `my "cool" stream`,
			wantTitle: `my \"cool\" stream`,
		},
		{
			name:      "title with backslash is escaped",
			cmd:       []string{"tmux", "attach", "-t", "hs-1"},
			title:     `path\to\thing`,
			wantTitle: `path\\to\\thing`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			script := darwinScript(tt.cmd, tt.title)
			titleLine := `set custom title of newTab to "`
			if tt.title == "" {
				if strings.Contains(script, titleLine) {
					t.Errorf("expected no title line but found one\nfull script:\n%s", script)
				}
				return
			}
			if !strings.Contains(script, `set newTab to do script "`) {
				t.Errorf("script should use 'set newTab to do script' when title is set\nfull script:\n%s", script)
			}
			wantLine := titleLine + tt.wantTitle + `"`
			if !strings.Contains(script, wantLine) {
				t.Errorf("script does not contain %q\nfull script:\n%s", wantLine, script)
			}
		})
	}
}

func TestShellIn(t *testing.T) {
	// $SHELL is resolved before it is exec'd (see InteractiveShell), so these
	// cases need a shell path that exists on the machine running the test.
	shell := filepath.Join(t.TempDir(), "myshell")
	if err := os.WriteFile(shell, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name  string
		shell string // $SHELL for the test; empty means unset
		dir   string
		want  string // the expected -c argument
	}{
		{
			name:  "uses $SHELL as the interactive shell",
			shell: shell,
			dir:   "/home/me/project",
			want:  `cd '/home/me/project' && exec '` + shell + `' '-i'`,
		},
		{
			name:  "falls back to /bin/sh when $SHELL is unset",
			shell: "",
			dir:   "/home/me/project",
			want:  `cd '/home/me/project' && exec '/bin/sh' '-i'`,
		},
		{
			name:  "quotes a directory containing spaces",
			shell: shell,
			dir:   "/home/me/My Project",
			want:  `cd '/home/me/My Project' && exec '` + shell + `' '-i'`,
		},
		{
			name:  "escapes a single quote in the directory",
			shell: shell,
			dir:   "/home/me/My Project's",
			want:  `cd '/home/me/My Project'\''s' && exec '` + shell + `' '-i'`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("SHELL", tt.shell)
			if tt.shell == "" {
				os.Unsetenv("SHELL")
			}
			cmd := ShellIn(tt.dir)
			if len(cmd) != 3 || cmd[0] != "/bin/sh" || cmd[1] != "-c" {
				t.Fatalf("ShellIn(%q) = %q; want /bin/sh -c <script>", tt.dir, cmd)
			}
			if cmd[2] != tt.want {
				t.Errorf("script = %q; want %q", cmd[2], tt.want)
			}
		})
	}
}

// Every argument is quoted, so nothing can break out of the -c script.
func TestCommandIn(t *testing.T) {
	tests := []struct {
		name string
		dir  string
		cmd  []string
		want string
	}{
		{
			name: "plain command",
			dir:  "/proj",
			cmd:  []string{"enclave", "exec", "--name", "7", "--", "bash"},
			want: `cd '/proj' && exec 'enclave' 'exec' '--name' '7' '--' 'bash'`,
		},
		{
			name: "quotes an argument containing spaces",
			dir:  "/home/me/My Project",
			cmd:  []string{"echo", "two words"},
			want: `cd '/home/me/My Project' && exec 'echo' 'two words'`,
		},
		{
			name: "an argument cannot break out of the script",
			dir:  "/proj",
			cmd:  []string{"echo", "'; rm -rf /; '"},
			want: `cd '/proj' && exec 'echo' ''\''; rm -rf /; '\'''`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := CommandIn(tt.dir, tt.cmd...)
			if len(cmd) != 3 || cmd[0] != "/bin/sh" || cmd[1] != "-c" {
				t.Fatalf("CommandIn = %q; want /bin/sh -c <script>", cmd)
			}
			if cmd[2] != tt.want {
				t.Errorf("script = %q; want %q", cmd[2], tt.want)
			}
		})
	}
}

// Each emulator spells "run this command" its own way; `kitty -e` and
// `gnome-terminal --` are not interchangeable.
func TestEmulatorArgv(t *testing.T) {
	cmd := []string{"/bin/sh", "-c", "cd '/proj' && exec '/bin/bash' '-i'"}
	tests := []struct {
		id    string
		title string
		want  []string // the arguments before cmd
	}{
		{"gnome-terminal", "feature/x", []string{"--title=feature/x", "--"}},
		{"konsole", "feature/x", []string{"-p", "tabtitle=feature/x", "-e"}},
		{"kitty", "feature/x", []string{"--title", "feature/x"}},
		{"kitty", "", nil},
		{"alacritty", "feature/x", []string{"--title", "feature/x", "-e"}},
		{"ghostty", "feature/x", []string{"--gtk-single-instance=false", "--confirm-close-surface=false", "--title=feature/x", "-e"}},
		{"xfce4-terminal", "feature/x", []string{"--title=feature/x", "-x"}},
		{"wezterm", "feature/x", []string{"start", "--"}},
		{"foot", "feature/x", []string{"--title", "feature/x"}},
		{"xterm", "feature/x", []string{"-title", "feature/x", "-e"}},
		{"x-terminal-emulator", "feature/x", []string{"-e"}},
	}
	for _, tt := range tests {
		t.Run(tt.id+"/"+tt.title, func(t *testing.T) {
			candidate, ok := findEmulator(linuxEmulators, tt.id)
			if !ok {
				t.Fatalf("%s is not in the Linux table", tt.id)
			}
			got := candidate.argv("/usr/bin/"+tt.id, cmd, tt.title)
			want := append(append([]string{"/usr/bin/" + tt.id}, tt.want...), cmd...)
			if !slices.Equal(got, want) {
				t.Errorf("argv = %q; want %q", got, want)
			}
		})
	}
}

// fakeEmulators puts an executable for each name on a PATH of its own, so which
// emulators are "installed" is decided by the test.
func fakeEmulators(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	return dir
}

// onFakePath is argv with its program, unless absolute, resolved in the fake
// PATH dir.
func onFakePath(dir string, argv []string) []string {
	resolved := slices.Clone(argv)
	if !filepath.IsAbs(resolved[0]) {
		resolved[0] = filepath.Join(dir, resolved[0])
	}
	return resolved
}

func assertErrorContains(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v; want it to contain %q", err, want)
	}
}

// Detection takes $TERMINAL before the first installed emulator.
func TestAutoCommand(t *testing.T) {
	cmd := []string{"/bin/sh", "-c", "exec bash"}
	tests := []struct {
		name      string
		installed []string
		terminal  string   // $TERMINAL
		want      []string // want[0] is looked up in the fake PATH
		wantErr   string
	}{
		{
			name:      "the first installed emulator in table order",
			installed: []string{"xterm", "kitty"},
			want:      append([]string{"kitty", "--title", "t"}, cmd...),
		},
		{
			name:      "$TERMINAL wins over table order",
			installed: []string{"kitty", "alacritty"},
			terminal:  "alacritty",
			want:      append([]string{"alacritty", "--title", "t", "-e"}, cmd...),
		},
		{
			name:      "an unknown $TERMINAL is given -e",
			installed: []string{"kitty", "st"},
			terminal:  "st",
			want:      append([]string{"st", "-e"}, cmd...),
		},
		{
			name:      "a $TERMINAL that is not installed is skipped",
			installed: []string{"kitty"},
			terminal:  "alacritty",
			want:      append([]string{"kitty", "--title", "t"}, cmd...),
		},
		{
			name:    "nothing installed names what to install",
			wantErr: "no terminal emulator is installed; install one of gnome-terminal, konsole,",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := fakeEmulators(t, tt.installed...)
			t.Setenv("TERMINAL", tt.terminal)
			got, err := autoCommand("linux", cmd, "t")
			if tt.wantErr != "" {
				assertErrorContains(t, err, tt.wantErr)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if want := onFakePath(dir, tt.want); !slices.Equal(got, want) {
				t.Errorf("autoCommand = %q; want %q", got, want)
			}
		})
	}
}

// $TERMINAL may be a path; the emulator is recognized by its base name.
func TestAutoCommandMatchesAPathInTerminalOnItsBaseName(t *testing.T) {
	dir := fakeEmulators(t, "kitty")
	t.Setenv("TERMINAL", filepath.Join(dir, "kitty"))
	got, err := autoCommand("linux", []string{"bash"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{filepath.Join(dir, "kitty"), "bash"}; !slices.Equal(got, want) {
		t.Errorf("autoCommand = %q; want %q", got, want)
	}
}

// Terminal.app ships with macOS, and macOS apps do not follow $TERMINAL.
func TestAutoCommandOnMacOSOpensTerminalApp(t *testing.T) {
	fakeEmulators(t, "kitty")
	t.Setenv("TERMINAL", "kitty")
	got, err := autoCommand("darwin", []string{"bash"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0] != "osascript" || !strings.Contains(got[2], `tell application "Terminal"`) {
		t.Errorf("autoCommand = %q; want Terminal.app scripted", got)
	}
}

// A chosen emulator wins over detection, and a custom template over both.
func TestWindowCommand(t *testing.T) {
	cmd := []string{"/bin/sh", "-c", "exec bash"}
	tests := []struct {
		name       string
		installed  []string
		terminal   string // $TERMINAL
		preference Preference
		title      string
		want       []string // want[0] is looked up in the fake PATH unless absolute
		wantErr    string
	}{
		{
			name:       "a chosen emulator wins over $TERMINAL and table order",
			installed:  []string{"gnome-terminal", "kitty", "foot"},
			terminal:   "kitty",
			preference: Preference{Emulator: "foot"},
			title:      "my ws",
			want:       append([]string{"foot", "--title", "my ws"}, cmd...),
		},
		{
			name:       "a chosen emulator that is not installed fails",
			installed:  []string{"kitty"},
			preference: Preference{Emulator: "ghostty"},
			wantErr:    "Ghostty, is not installed",
		},
		{
			name:       "an unknown emulator fails",
			preference: Preference{Emulator: "no-such-terminal"},
			wantErr:    `unknown terminal emulator "no-such-terminal"`,
		},
		{
			name:       "custom substitutes the quoted command and leaves the window untitled",
			installed:  []string{"kitty"},
			terminal:   "kitty",
			preference: Preference{Emulator: Custom, Command: "foot -e {command}"},
			title:      "my ws",
			want:       []string{"/bin/sh", "-c", `foot -e '/bin/sh' '-c' 'exec bash'`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := fakeEmulators(t, tt.installed...)
			t.Setenv("TERMINAL", tt.terminal)
			got, err := windowCommand("linux", tt.preference, cmd, tt.title)
			if tt.wantErr != "" {
				assertErrorContains(t, err, tt.wantErr)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if want := onFakePath(dir, tt.want); !slices.Equal(got, want) {
				t.Errorf("windowCommand = %q; want %q", got, want)
			}
		})
	}
}

func TestEmulatorsMarksTheInstalledOnes(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("macOS emulators are applications, not PATH binaries")
	}
	fakeEmulators(t, "foot")
	for _, emulator := range Emulators().Emulators {
		if emulator.Installed != (emulator.ID == "foot") {
			t.Errorf("%s installed = %v", emulator.ID, emulator.Installed)
		}
	}
}

// What Auto resolves to is named even when it is not one the list offers, so a
// client can say so rather than guess from the list.
func TestEmulatorsNamesTheAutomaticOne(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("macOS emulators are applications, not PATH binaries")
	}
	tests := []struct {
		name      string
		installed []string
		terminal  string // $TERMINAL
		want      string // "" means none
	}{
		{name: "the first installed emulator in table order", installed: []string{"xterm", "kitty"}, want: "kitty"},
		{name: "the one $TERMINAL names", installed: []string{"kitty", "foot"}, terminal: "foot", want: "foot"},
		{name: "a $TERMINAL the table does not know", installed: []string{"kitty", "st"}, terminal: "st", want: "st"},
		{name: "a $TERMINAL the table does not know, with nothing listed installed", installed: []string{"st"}, terminal: "st", want: "st"},
		{name: "a $TERMINAL not on PATH", installed: []string{"kitty"}, terminal: "st", want: "kitty"},
		{name: "none when nothing is installed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeEmulators(t, tt.installed...)
			t.Setenv("TERMINAL", tt.terminal)
			if got := Emulators().Automatic; got != tt.want {
				t.Errorf("Automatic = %q; want %q", got, tt.want)
			}
		})
	}
}

// A client puts "(not installed)" or "Automatic (…)" around a label, which a
// label of its own in parentheses would nest.
func TestEmulatorLabelsHoldNoParentheses(t *testing.T) {
	for _, table := range [][]emulatorSpec{linuxEmulators, darwinEmulators} {
		for _, candidate := range table {
			if strings.ContainsAny(candidate.label, "()") {
				t.Errorf("%s label = %q, want no parentheses", candidate.id, candidate.label)
			}
		}
	}
}

func TestSupported(t *testing.T) {
	platformEmulator := emulatorsFor(runtime.GOOS)[0].id
	for _, emulatorID := range []string{Auto, Custom, platformEmulator} {
		if !Supported(emulatorID) {
			t.Errorf("Supported(%q) = false; want true", emulatorID)
		}
	}
	for _, emulatorID := range []string{"", "no-such-terminal"} {
		if Supported(emulatorID) {
			t.Errorf("Supported(%q) = true; want false", emulatorID)
		}
	}
}

// The directory is quoted, but the line runs as written: it is the user's own
// shell to get right.
func TestShellLineIn(t *testing.T) {
	tests := []struct {
		name string
		dir  string
		line string
		want string
	}{
		{
			name: "plain line",
			dir:  "/proj",
			line: "vim .",
			want: `cd '/proj' && ( vim .`,
		},
		{
			name: "the line is not quoted — flags and pipelines run as written",
			dir:  "/proj",
			line: "lazygit -p . | tee /tmp/log",
			want: `cd '/proj' && ( lazygit -p . | tee /tmp/log`,
		},
		{
			name: "a directory with spaces and quotes cannot break the cd",
			dir:  "/home/me/My Project's",
			line: "vim .",
			want: `cd '/home/me/My Project'\''s' && ( vim .`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := ShellLineIn(tt.dir, tt.line)
			if len(cmd) != 3 || cmd[0] != "/bin/sh" || cmd[1] != "-c" {
				t.Fatalf("ShellLineIn = %q; want /bin/sh -c <script>", cmd)
			}
			if !strings.HasPrefix(cmd[2], tt.want) {
				t.Errorf("script = %q; want it to start with %q", cmd[2], tt.want)
			}
		})
	}
}

// The emulator closes as soon as its command returns, so a line that fails has
// to hold the window or the error is never readable.
func TestShellLineInHoldsTheWindowOpenOnFailure(t *testing.T) {
	script := ShellLineIn("/proj", "vim .")[2]
	for _, want := range []string{"status=$?", "read -r _"} {
		if !strings.Contains(script, want) {
			t.Errorf("script = %q; want it to contain %q", script, want)
		}
	}

	// A line that succeeds must not wait for a keypress.
	cmd := ShellLineIn(t.TempDir(), "true")
	out, err := exec.Command(cmd[0], cmd[1], cmd[2]).CombinedOutput()
	if err != nil {
		t.Fatalf("running a succeeding line: %v (%s)", err, out)
	}
	if len(out) != 0 {
		t.Errorf("output = %q; want nothing on success", out)
	}

	// A failing one reports the status and waits (stdin is closed here, so the
	// read returns at once rather than hanging the test).
	cmd = ShellLineIn(t.TempDir(), "exit 3") // a subshell, so exit is caught too
	out, _ = exec.Command(cmd[0], cmd[1], cmd[2]).CombinedOutput()
	if !strings.Contains(string(out), "exited 3") {
		t.Errorf("output = %q; want the exit status reported", out)
	}
}

func TestHoldOnFailure(t *testing.T) {
	tests := []struct {
		name string
		cmd  []string
		want string
	}{
		{
			name: "plain command",
			cmd:  []string{"enclave", "attach", "enclave-claude-abc"},
			want: `'enclave' 'attach' 'enclave-claude-abc'; `,
		},
		{
			name: "an argument cannot break out of the script",
			cmd:  []string{"echo", "'; rm -rf /; '"},
			want: `'echo' ''\''; rm -rf /; '\'''; `,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := HoldOnFailure(tt.cmd...)
			if len(cmd) != 3 || cmd[0] != "/bin/sh" || cmd[1] != "-c" {
				t.Fatalf("HoldOnFailure = %q; want /bin/sh -c <script>", cmd)
			}
			if !strings.HasPrefix(cmd[2], tt.want) {
				t.Errorf("script = %q; want it to start with %q", cmd[2], tt.want)
			}
		})
	}
}

func TestHoldOnFailureHoldsTheWindowOpenOnlyOnFailure(t *testing.T) {
	cmd := HoldOnFailure("true")
	out, err := exec.Command(cmd[0], cmd[1:]...).CombinedOutput()
	if err != nil {
		t.Fatalf("running a succeeding command: %v (%s)", err, out)
	}
	if len(out) != 0 {
		t.Errorf("output = %q; want nothing on success", out)
	}

	// stdin is closed here, so the read returns at once rather than hanging.
	cmd = HoldOnFailure("sh", "-c", "echo attach failed >&2; exit 3")
	out, _ = exec.Command(cmd[0], cmd[1:]...).CombinedOutput()
	for _, want := range []string{"attach failed", "exited 3"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("output = %q; want it to contain %q", out, want)
		}
	}
}

func TestHoldOnStartupFailureIn(t *testing.T) {
	cmd := HoldOnStartupFailureIn("/proj", "enclave", "exec", "--", "bash")
	if len(cmd) != 3 || cmd[0] != "/bin/sh" || cmd[1] != "-c" {
		t.Fatalf("HoldOnStartupFailureIn = %q; want /bin/sh -c <script>", cmd)
	}
	want := `cd '/proj' && 'enclave' 'exec' '--' 'bash'; `
	if !strings.Contains(cmd[2], want) {
		t.Errorf("script = %q; want it to contain %q", cmd[2], want)
	}
}

// stdin is closed in these runs, so a hold's read returns at once rather than
// hanging; what the window would show is the output.
func TestHoldOnStartupFailureInHoldsOnlyAFailureToStart(t *testing.T) {
	dir := t.TempDir()
	run := func(cmd []string) string {
		t.Helper()
		out, _ := exec.Command(cmd[0], cmd[1:]...).CombinedOutput()
		return string(out)
	}

	if out := run(HoldOnStartupFailureIn(dir, "touch", "marker")); out != "" {
		t.Errorf("output on success = %q; want nothing", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "marker")); err != nil {
		t.Errorf("the command did not run in %s: %v", dir, err)
	}

	out := run(HoldOnStartupFailureIn(dir, "sh", "-c", "echo exec failed >&2; exit 3"))
	for _, want := range []string{"exec failed", "exited 3"} {
		if !strings.Contains(out, want) {
			t.Errorf("output on a failure to start = %q; want it to contain %q", out, want)
		}
	}

	// cd's own error, naming the path, is what the user needs to read.
	out = run(HoldOnStartupFailureIn(filepath.Join(dir, "missing"), "true"))
	for _, want := range []string{"missing", "exited"} {
		if !strings.Contains(out, want) {
			t.Errorf("output for a missing directory = %q; want it to contain %q", out, want)
		}
	}

	// A shell the user exits after a failed command returns that status: past
	// the window it is a normal close, not an error. A zero window makes any
	// failure late without waiting one out.
	if out := run(holdStartupFailureIn(0, dir, []string{"sh", "-c", "exit 130"})); out != "" {
		t.Errorf("output for a late non-zero exit = %q; want the window to close", out)
	}
}

// $SHELL carries the login shell from /etc/passwd, a path that need not exist
// here; exec'ing a missing one fails inside the terminal window just opened.
func TestInteractiveShell(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	if got := InteractiveShell(); got != "/bin/sh" {
		t.Errorf("InteractiveShell() = %q; want the usable $SHELL", got)
	}

	t.Setenv("SHELL", "")
	if got := InteractiveShell(); got != "/bin/sh" {
		t.Errorf("InteractiveShell() with no $SHELL = %q; want /bin/sh", got)
	}

	// A path that does not exist must never be handed to exec.
	t.Setenv("SHELL", "/usr/bin/homeshell-no-such-shell")
	got := InteractiveShell()
	if got == "/usr/bin/homeshell-no-such-shell" {
		t.Error("InteractiveShell() returned a path that does not exist")
	}
	if !filepath.IsAbs(got) {
		t.Errorf("InteractiveShell() = %q; want an absolute path", got)
	}
	if _, err := os.Stat(got); err != nil {
		t.Errorf("InteractiveShell() = %q, which cannot be run: %v", got, err)
	}

	// The nix/homebrew shape: the recorded directory is wrong, but the shell
	// itself is on PATH. The stale path is made up rather than /usr/bin/zsh,
	// which exists on many hosts and would then be returned as it stands.
	dir := t.TempDir()
	fake := filepath.Join(dir, "zsh")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("SHELL", filepath.Join(t.TempDir(), "no-such-dir", "zsh"))
	if got := InteractiveShell(); got != fake {
		t.Errorf("InteractiveShell() = %q; want the zsh on PATH (%q)", got, fake)
	}

	// A relative $SHELL is not a path to exec either.
	t.Setenv("SHELL", "zsh")
	if got := InteractiveShell(); got != fake {
		t.Errorf("InteractiveShell() = %q; want the zsh on PATH (%q)", got, fake)
	}
}

func TestShellInResolvesAMissingShell(t *testing.T) {
	t.Setenv("SHELL", "/usr/bin/homeshell-no-such-shell")
	cmd := ShellIn("/proj")
	if strings.Contains(cmd[2], "homeshell-no-such-shell") {
		t.Errorf("script = %q; want the missing shell resolved away", cmd[2])
	}
}
