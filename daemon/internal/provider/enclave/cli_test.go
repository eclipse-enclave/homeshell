package enclave

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/provider/enclave/sandboxschema"
)

func TestParsePSJSON(t *testing.T) {
	out := []byte(`[
  {
    "name": "enclave-claude-aa163db36bfd-hs-6e299ca1fde8624d",
    "tool": "claude",
    "projectDir": "/real/demo-app",
    "projectHash": "aa163db36bfd",
    "worktree": "/home/me/demo-app",
    "status": "running",
    "sessionName": "hs-6e299ca1fde8624d",
    "ports": [],
    "network": null,
    "options": {"features": ["devtools"], "yolo": true}
  },
  {
    "name": "enclave-codex-def456def456",
    "tool": "codex",
    "projectDir": "/tmp/my project",
    "worktree": "",
    "status": "exited",
    "sessionName": ""
  }
]`)
	got, err := parsePSJSON(out)
	if err != nil {
		t.Fatalf("parsePSJSON: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d containers, want 2", len(got))
	}
	first := got[0]
	if first.Name != "enclave-claude-aa163db36bfd-hs-6e299ca1fde8624d" || first.Tool != "claude" ||
		first.Directory != "/home/me/demo-app" || first.Status != "running" || first.SessionName != "hs-6e299ca1fde8624d" {
		t.Errorf("first = %+v; want the directory the session was given, not the resolved one", first)
	}
	if string(first.Options["features"]) != `["devtools"]` || string(first.Options["yolo"]) != "true" {
		t.Errorf("options = %v; want the recorded values as they are", first.Options)
	}
	second := got[1]
	if second.Directory != "/tmp/my project" || second.SessionName != "" || second.Options != nil {
		t.Errorf("second = %+v; want the project directory without a worktree, and no options when none are recorded", second)
	}
}

func TestParseSessionName(t *testing.T) {
	tests := []struct {
		containerName string
		tool          string
		want          string
	}{
		{"enclave-claude-abc123abc123", "claude", ""},
		{"enclave-claude-abc123abc123-main", "claude", "main"},
		{"enclave-codex-def456def456-feat-1", "codex", "feat-1"},
		{"enclave-open-code-abc123abc123-test", "open-code", "test"},
		{"unrelated-container", "claude", ""},
		{"enclave-claude-short", "claude", ""},
	}
	for _, tt := range tests {
		t.Run(tt.containerName, func(t *testing.T) {
			got := parseSessionName(tt.containerName, tt.tool)
			if got != tt.want {
				t.Errorf("parseSessionName(%q, %q) = %q, want %q",
					tt.containerName, tt.tool, got, tt.want)
			}
		})
	}
}

// TestBinaryNameIsEnclave guards the rename from YoloArena to Eclipse Enclave:
// the CLI executable is "enclave", so binary() must resolve to it (this is what
// broke provider detection when the binary was still looked up as "yoloarena").
func TestBinaryNameIsEnclave(t *testing.T) {
	if got := (&realCLI{}).binary(); got != "enclave" {
		t.Errorf("binary() = %q, want enclave", got)
	}
}

// configEnvelope is what `enclave config --json --pass-env +<probe>` prints from
// #181 on, cut to what Available asks about.
const configEnvelope = `{"schemaVersion":"1","kind":"config","options":{"pass_env":{"value":["HOMESHELL_CONTRACT_PROBE"]}}}`

// TestAvailableFindsEnclaveOnPath exercises the code path that actually broke:
// Available() resolves the executable by name on PATH. A PATH holding only an
// "enclave" executable must satisfy it (the pre-rename LookPath("yoloarena")
// failed here, so the daemon reported the provider unavailable and dropped it)
// and the resolved path must be cached for binary().
func TestAvailableFindsEnclaveOnPath(t *testing.T) {
	dir := t.TempDir()
	stub := filepath.Join(dir, "enclave")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\necho '"+configEnvelope+"'\n"), 0o755); err != nil {
		t.Fatalf("writing stub binary: %v", err)
	}
	t.Setenv("PATH", dir)

	cli := &realCLI{}
	ok, err := cli.Available(context.Background())
	if !ok || err != nil {
		t.Fatalf("Available() = %v, %v; want true, <nil>", ok, err)
	}
	if got := cli.binary(); got != stub {
		t.Errorf("binary() = %q, want the resolved %q", got, stub)
	}
}

// TestAvailableRefusesEnclaveBeforeVersionedJSON: an Enclave from before
// enclave#164 prints `config --json` without an envelope, so it is unavailable
// with a reason naming the fix. Updating it in place makes it available without
// a daemon restart.
func TestAvailableRefusesEnclaveBeforeVersionedJSON(t *testing.T) {
	tests := []struct {
		name   string
		output string
	}{
		{"JSON without an envelope", `{"global":{},"project":{},"rows":[]}`},
		{"no JSON at all", "Global Config: ~/.config/enclave/config.json"},
		// Before enclave#181 a --pass-env list replaces the configured one, and "+NAME" is a name.
		{"--pass-env +NAME taken literally", `{"schemaVersion":"1","kind":"config","options":{"pass_env":{"value":["+HOMESHELL_CONTRACT_PROBE"]}}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			stub := filepath.Join(dir, "enclave")
			write := func(script string) {
				t.Helper()
				if err := os.WriteFile(stub, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
					t.Fatalf("writing stub binary: %v", err)
				}
			}
			t.Setenv("PATH", dir)
			cli := &realCLI{}

			write("echo '" + tt.output + "'")
			ok, err := cli.Available(context.Background())
			if ok || err == nil || !strings.Contains(err.Error(), "update Enclave") {
				t.Fatalf("Available() = %v, %v; want false and an error saying to update Enclave", ok, err)
			}

			write(`[ "$*" = "config --json --pass-env +HOMESHELL_CONTRACT_PROBE" ] || exit 9; echo '` + configEnvelope + `'`)
			if ok, err := cli.Available(context.Background()); !ok || err != nil {
				t.Fatalf("after updating, Available() = %v, %v; want true, <nil>", ok, err)
			}
		})
	}
}

// An Enclave refused for predating the versioned JSON is not asked again on
// every call, only once it changes.
func TestAvailableAsksEachEnclaveBuildOnce(t *testing.T) {
	dir := t.TempDir()
	stub := filepath.Join(dir, "enclave")
	calls := filepath.Join(t.TempDir(), "calls")
	write := func(output string) {
		t.Helper()
		script := "#!/bin/sh\necho x >> '" + calls + "'\necho '" + output + "'\n"
		if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
			t.Fatalf("writing stub binary: %v", err)
		}
	}
	asked := func() int {
		out, _ := os.ReadFile(calls)
		return strings.Count(string(out), "x")
	}
	t.Setenv("PATH", dir)
	cli := &realCLI{}

	write(`{"global":{},"project":{},"rows":[]}`)
	for range 3 {
		if ok, _ := cli.Available(context.Background()); ok {
			t.Fatal("Available() = true for an Enclave without the versioned JSON")
		}
	}
	if n := asked(); n != 1 {
		t.Errorf("asked the same Enclave %d times; want once", n)
	}

	write(configEnvelope)
	for range 3 {
		if ok, err := cli.Available(context.Background()); !ok || err != nil {
			t.Fatalf("after updating, Available() = %v, %v; want true, <nil>", ok, err)
		}
	}
	if n := asked(); n != 2 {
		t.Errorf("asked %d times in all; want once more for the updated Enclave", n)
	}
}

// A `config --json` that fails says nothing about the contract: Available
// reports why, reuses that briefly as other failed answers are, then asks again,
// so fixing the cause needs no new binary.
func TestAvailableAsksAgainAfterAFailedCheck(t *testing.T) {
	dir := t.TempDir()
	stub := filepath.Join(dir, "enclave")
	calls := filepath.Join(t.TempDir(), "calls")
	broken := filepath.Join(t.TempDir(), "broken")
	if err := os.WriteFile(broken, nil, 0o644); err != nil {
		t.Fatalf("writing marker: %v", err)
	}
	script := "#!/bin/sh\necho x >> '" + calls + "'\nif [ -e '" + broken + "' ]; then echo 'config: cannot read it' >&2; exit 1; fi\necho '" + configEnvelope + "'\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatalf("writing stub binary: %v", err)
	}
	t.Setenv("PATH", dir)
	cli := &realCLI{}

	for range 3 {
		ok, err := cli.Available(context.Background())
		if ok || err == nil || !strings.Contains(err.Error(), "cannot read it") {
			t.Fatalf("Available() = %v, %v; want false and Enclave's reason", ok, err)
		}
	}
	if out, _ := os.ReadFile(calls); strings.Count(string(out), "x") != 1 {
		t.Errorf("asked %d times within failedAnswerTTL; want once", strings.Count(string(out), "x"))
	}
	if err := os.Remove(broken); err != nil {
		t.Fatalf("removing marker: %v", err)
	}
	cli.checked.at = cli.checked.at.Add(-failedAnswerTTL)
	if ok, err := cli.Available(context.Background()); !ok || err != nil {
		t.Fatalf("once the failure is old and the call succeeds, Available() = %v, %v; want true, <nil>", ok, err)
	}
}

// The reconciler checks availability while the drift loop lists sessions, so
// resolving the binary and using it must not race (go test -race).
func TestAvailableWhileTheBinaryIsInUse(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "enclave"), []byte("#!/bin/sh\necho '"+configEnvelope+"'\n"), 0o755); err != nil {
		t.Fatalf("writing stub binary: %v", err)
	}
	t.Setenv("PATH", dir)
	cli := &realCLI{}
	var wait sync.WaitGroup
	for range 4 {
		wait.Go(func() { _, _ = cli.Available(context.Background()) })
		wait.Go(func() { _ = cli.binary() })
	}
	wait.Wait()
}

// TestAvailableWithoutBinaryReports the negative case: no enclave on PATH means
// unavailable, with a reason that names the binary the user has to install.
func TestAvailableWithoutBinaryReports(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	ok, err := (&realCLI{}).Available(context.Background())
	if ok || err == nil {
		t.Fatalf("Available() = %v, %v; want false and an error", ok, err)
	}
	if !strings.Contains(err.Error(), "enclave") {
		t.Errorf("Available() error = %q, want it to name the enclave binary", err)
	}
}

// containsPair reports whether args contains first immediately followed by
// second.
func containsPair(args []string, first, second string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == first && args[i+1] == second {
			return true
		}
	}
	return false
}

// scanOutput is what turns a build's raw byte stream into the lines the UI
// shows, so its splitting and cleaning are tested directly rather than only
// through an exec.
func TestScanOutputSplitsAndCleans(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"plain newlines", "one\ntwo\nthree\n", []string{"one", "two", "three"}},
		{"no trailing newline", "one\ntwo", []string{"one", "two"}},
		// Build tools redraw a progress line with \r; each redraw is its own
		// update, not one giant line.
		{"carriage returns split", "10%\r50%\r100%\n", []string{"10%", "50%", "100%"}},
		{"crlf yields no blank lines", "one\r\ntwo\r\n", []string{"one", "two"}},
		{"blank lines dropped", "one\n\n\ntwo\n", []string{"one", "two"}},
		{"ansi colour stripped", "\x1b[32m=> building\x1b[0m\n", []string{"=> building"}},
		{"cursor moves stripped", "\x1b[2K\x1b[1A=> [2/7] RUN apt-get\n", []string{"=> [2/7] RUN apt-get"}},
		{"osc title stripped", "\x1b]0;title\x07done\n", []string{"done"}},
		{"tabs kept, other controls dropped", "a\tb\x00c\n", []string{"a\tb" + "c"}},
		{"whitespace-only line dropped", "   \n\t\nreal\n", []string{"real"}},
		{"empty input", "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			scanOutput(strings.NewReader(tt.in), func(line string) { got = append(got, line) })
			if len(got) != len(tt.want) {
				t.Fatalf("scanOutput(%q) = %q, want %q", tt.in, got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("line %d = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// A single unterminated line must not grow without bound: a build that writes
// megabytes with no newline should be truncated, not buffered whole.
func TestScanOutputCapsALine(t *testing.T) {
	var got []string
	scanOutput(strings.NewReader(strings.Repeat("x", 3*maxOutputLine)), func(l string) {
		got = append(got, l)
	})
	// Asserted before the per-line check below, which passes vacuously on an empty
	// result — and empty is exactly what the bug this pins produced: the scanner
	// gave up on the over-long token instead of cutting it, reporting no lines at
	// all rather than lines that were too long.
	if len(got) == 0 {
		t.Fatal("no lines reported for an over-long run of output, want it split into capped lines")
	}
	total := 0
	for i, line := range got {
		if len(line) > maxOutputLine {
			t.Errorf("line %d is %d bytes, want <= %d", i, len(line), maxOutputLine)
		}
		total += len(line)
	}
	if total != 3*maxOutputLine {
		t.Errorf("reported %d bytes in total, want the whole %d — a capped line is split, not dropped",
			total, 3*maxOutputLine)
	}
}

// The real cost of giving up on an over-long line is not the line: it is
// everything after it. The scanner stops, the child fills its pipe and blocks,
// and the caller sees a build that has gone silent — which its idle watchdog
// then kills as hung.
func TestScanOutputKeepsReadingPastAnOverLongLine(t *testing.T) {
	var got []string
	input := strings.Repeat("x", 3*maxOutputLine) + "\nbuild step 2\nbuild step 3\n"
	scanOutput(strings.NewReader(input), func(l string) {
		got = append(got, l)
	})
	last := []string{"build step 2", "build step 3"}
	if len(got) < len(last) {
		t.Fatalf("got %d lines, want the %d lines that follow the long one", len(got), len(last))
	}
	if diff := got[len(got)-len(last):]; !slices.Equal(diff, last) {
		t.Errorf("lines after the over-long one = %q, want %q", diff, last)
	}
}

// writeStubEnclave installs an executable stub the realCLI will run in place of
// the enclave binary, and returns a CLI wired to it.
func writeStubEnclave(t *testing.T, script string) *realCLI {
	t.Helper()
	path := filepath.Join(t.TempDir(), "enclave")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatalf("writing stub binary: %v", err)
	}
	return &realCLI{binaryPath: path}
}

// The command inside the container is agent-controlled, so its output is capped
// rather than buffered until the deadline.
func TestExecOutputStopsAtTheCap(t *testing.T) {
	cli := writeStubEnclave(t, "exec yes model/endless\n")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	out, err := cli.ExecOutput(ctx, t.TempDir(), "hs-1", "opencode", []string{"models"})
	if err == nil || !strings.Contains(err.Error(), "output over") {
		t.Fatalf("ExecOutput on endless output = %d bytes, %v; want an output-over-the-cap error", len(out), err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("ExecOutput took %s, want it to stop at the cap rather than at the deadline", elapsed)
	}
}

func TestListSessionsAsksForJSON(t *testing.T) {
	cli := writeStubEnclave(t, `[ "$*" = "ps --json" ] || exit 9
echo '[]'
`)
	if got, err := cli.ListSessions(context.Background(), t.TempDir()); err != nil || len(got) != 0 {
		t.Errorf("ListSessions = %v, %v; want no containers", got, err)
	}
}

// A failed `ps` is an error, not an empty listing: read as one, it would end
// every running session. A cancelled one says it was cancelled.
func TestListSessionsReportsAFailedListing(t *testing.T) {
	cli := writeStubEnclave(t, "echo 'Cannot connect to the Docker daemon' >&2; exit 1\n")
	_, err := cli.ListSessions(context.Background(), t.TempDir())
	if err == nil {
		t.Fatal("ListSessions on a failing CLI: want an error")
	}
	// The error carries the CLI's own reason, so the reconcile warning says why.
	if !strings.Contains(err.Error(), "Cannot connect to the Docker daemon") {
		t.Errorf("ListSessions error = %q, want it to carry the CLI's stderr", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := cli.ListSessions(ctx, t.TempDir()); !errors.Is(err, context.Canceled) {
		t.Errorf("ListSessions cancelled: error = %v, want context.Canceled", err)
	}
}

// A failed `ps --all` is an error: the create-time check reads it as "could not
// tell", not as "no such container".
func TestListAllSessions(t *testing.T) {
	cli := writeStubEnclave(t, `[ "$*" = "ps --json --all" ] || exit 9
echo '[{"name":"enclave-claude-x","tool":"claude","worktree":"/p","status":"exited"}]'
`)
	got, err := cli.ListAllSessions(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("ListAllSessions: %v", err)
	}
	if len(got) != 1 || got[0].Name != "enclave-claude-x" || got[0].Status != "exited" {
		t.Errorf("ListAllSessions = %+v, want the exited container", got)
	}

	cli = writeStubEnclave(t, "echo 'Cannot connect to the Docker daemon' >&2; exit 1\n")
	if _, err := cli.ListAllSessions(context.Background(), t.TempDir()); err == nil {
		t.Error("ListAllSessions on a failing CLI: want an error")
	}
}

// Run must report the build output as it arrives and still pick the container
// name off the last non-empty stdout line — the two jobs its two streams do.
func TestRunStreamsProgressAndReturnsContainerName(t *testing.T) {
	cli := writeStubEnclave(t, `
echo "=> [internal] load metadata" >&2
printf '\x1b[32m=> [2/7] RUN apt-get install\x1b[0m\n' >&2
printf 'building 10%%\rbuilding 90%%\n' >&2
echo "enclave-proj-claude-1"
`)

	// Guarded because Run scans stdout and stderr on two goroutines and calls
	// Progress from both — the same reason the daemon's own sink takes a lock.
	var (
		mu    sync.Mutex
		lines []string
	)
	name, err := cli.Run(context.Background(), t.TempDir(), RunOpts{
		Tool:       "claude",
		Background: true,
		Progress: func(line string) {
			mu.Lock()
			lines = append(lines, line)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if name != "enclave-proj-claude-1" {
		t.Errorf("container name = %q, want the last stdout line", name)
	}
	mu.Lock()
	joined := strings.Join(lines, "\n")
	mu.Unlock()
	for _, want := range []string{
		"=> [internal] load metadata",
		"=> [2/7] RUN apt-get install", // ANSI stripped
		"building 10%",                 // \r split into its own update
		"building 90%",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("progress %q is missing %q", joined, want)
		}
	}
}

// A nil Progress is the ordinary case for every caller that does not care, and
// must not panic the streaming goroutines.
func TestRunWithoutProgressSinkStillWorks(t *testing.T) {
	cli := writeStubEnclave(t, "echo noise >&2\necho container-1\n")

	name, err := cli.Run(context.Background(), t.TempDir(), RunOpts{Background: true})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if name != "container-1" {
		t.Errorf("container name = %q, want container-1", name)
	}
}

// StderrPipe leaves ExitError.Stderr empty, so a failing build has to carry its
// reason from the tail we kept — otherwise the user gets a bare exit status.
func TestRunFailureReportsStderrTail(t *testing.T) {
	cli := writeStubEnclave(t, `
echo "Step 3/7: RUN bad-command" >&2
echo "/bin/sh: bad-command: not found" >&2
exit 2
`)

	_, err := cli.Run(context.Background(), t.TempDir(), RunOpts{Background: true})
	if err == nil {
		t.Fatal("Run() error = nil, want a failure")
	}
	if !strings.Contains(err.Error(), "bad-command: not found") {
		t.Errorf("Run() error = %q, want it to carry the stderr tail", err)
	}
}

// Succeeding with no stdout is a protocol violation by the CLI, not a silent
// success: without a name the daemon has no container to track.
func TestRunWithoutContainerNameIsAnError(t *testing.T) {
	cli := writeStubEnclave(t, "echo only-stderr >&2\n")

	if _, err := cli.Run(context.Background(), t.TempDir(), RunOpts{Background: true}); err == nil {
		t.Fatal("Run() error = nil, want an error naming the missing container name")
	}
}

// A `run` that outlives the daemon's cap has to say so. os/exec reports a
// context-deadline kill as a bare "signal: killed", which reads as "the container
// died" and sends the reader hunting through Docker, the OOM killer and the image
// build for a limit the daemon imposed itself.
func TestRunTimeoutSaysItTimedOutRatherThanKilled(t *testing.T) {
	cli := writeStubEnclave(t, "sleep 1\n")
	cli.runTimeoutOverride = 150 * time.Millisecond

	_, err := cli.Run(context.Background(), t.TempDir(), RunOpts{Background: true})
	if err == nil {
		t.Fatal("Run() error = nil, want a timeout")
	}
	if strings.Contains(err.Error(), "signal: killed") {
		t.Errorf("Run() error = %q; want the cap named, not the raw signal", err)
	}
	for _, want := range []string{"gave up after", "150ms", "retrying"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Run() error = %q; want it to mention %q", err, want)
		}
	}
}

// Tool args go **after a `--`**, and last: that separator is what makes
// `enclave run` forward them onto the tool it starts instead of reading them as
// its own flags. Order matters as much as presence, so the argv is asserted whole.
func TestRunForwardsToolArgsAfterASeparator(t *testing.T) {
	cli := writeStubEnclave(t, "echo \"args: $*\" >&2\necho container-1\n")

	var (
		mu    sync.Mutex
		lines []string
	)
	if _, err := cli.Run(context.Background(), t.TempDir(), RunOpts{
		Tool:        "claude",
		SessionName: "hs-1",
		Background:  true,
		ToolArgs:    []string{"--model", "claude-opus-5"},
		Progress: func(line string) {
			mu.Lock()
			lines = append(lines, line)
			mu.Unlock()
		},
	}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	mu.Lock()
	joined := strings.Join(lines, "\n")
	mu.Unlock()
	want := "args: run --tool claude --name hs-1 --background -- --model claude-opus-5"
	if !strings.Contains(joined, want) {
		t.Errorf("stub saw %q, want %q", joined, want)
	}
}

// Without tool args there is no `--`: a session with no knobs set must produce
// exactly the command it always did.
func TestRunWithoutToolArgsPassesNoSeparator(t *testing.T) {
	cli := writeStubEnclave(t, "echo \"args: $*\" >&2\necho container-1\n")

	var (
		mu    sync.Mutex
		lines []string
	)
	if _, err := cli.Run(context.Background(), t.TempDir(), RunOpts{
		Tool:       "claude",
		Background: true,
		Progress: func(line string) {
			mu.Lock()
			lines = append(lines, line)
			mu.Unlock()
		},
	}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	mu.Lock()
	joined := strings.Join(lines, "\n")
	mu.Unlock()
	if strings.Contains(joined, "--") && strings.Contains(joined, " -- ") {
		t.Errorf("args %q carry a `--` with no tool args", joined)
	}
}

// A session's model reaches an agent with no model flag as an environment
// variable, and `--pass-env` only names it: Enclave reads the value from the
// environment `enclave` runs with. Both halves have to be there, so the test
// asserts on both — the flag on the command line and the value in the child
// process.
func TestRunPassesEnvByNameAndValue(t *testing.T) {
	cli := writeStubEnclave(t, `
echo "args: $*" >&2
echo "ANTHROPIC_MODEL=$ANTHROPIC_MODEL" >&2
echo "PATH-was-inherited=$([ -n "$PATH" ] && echo yes)" >&2
echo enclave-proj-claude-1
`)

	var (
		mu    sync.Mutex
		lines []string
	)
	if _, err := cli.Run(context.Background(), t.TempDir(), RunOpts{
		Tool:       "claude",
		Background: true,
		Env:        map[string]string{"ANTHROPIC_MODEL": "claude-opus-5"},
		Progress: func(line string) {
			mu.Lock()
			lines = append(lines, line)
			mu.Unlock()
		},
	}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	mu.Lock()
	joined := strings.Join(lines, "\n")
	mu.Unlock()
	for _, want := range []string{
		"--pass-env +ANTHROPIC_MODEL",
		"ANTHROPIC_MODEL=claude-opus-5",
		// Naming one variable must not cut the child off from the rest of the
		// daemon's environment — enclave needs PATH, HOME and its own settings.
		"PATH-was-inherited=yes",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("stub reported %q, missing %q", joined, want)
		}
	}
}

// A session's variables are added to the configured pass_env with "+NAME", never
// passed as a list: a list replaces the configured one, and Enclave's no_api_key
// and ephemeral gates apply only to configured entries.
func TestRunAddsSessionVariablesToTheConfiguredPassEnv(t *testing.T) {
	cli := writeStubEnclave(t, `
[ "$1" = "run" ] || { echo "unexpected call: $*" >&2; exit 9; }
echo "args: $*" >&2
echo enclave-proj-claude-1
`)

	lines := runCollectingProgress(t, cli, RunOpts{
		Tool:       "claude",
		Background: true,
		Env:        map[string]string{"VIBE_ACTIVE_MODEL": "local", "ANTHROPIC_MODEL": "claude-opus-5"},
	})
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "--pass-env +ANTHROPIC_MODEL --pass-env +VIBE_ACTIVE_MODEL") {
		t.Errorf("args %q: want each session variable added with +NAME, sorted", joined)
	}
}

func runCollectingProgress(t *testing.T, cli *realCLI, opts RunOpts) []string {
	t.Helper()
	var (
		mu    sync.Mutex
		lines []string
	)
	opts.Progress = func(line string) {
		mu.Lock()
		lines = append(lines, line)
		mu.Unlock()
	}
	if _, err := cli.Run(context.Background(), t.TempDir(), opts); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	return slices.Clone(lines)
}

// Without Env there is no --pass-env: a session with no knobs set must produce
// the same command it always did.
func TestRunWithoutEnvPassesNoEnvFlag(t *testing.T) {
	cli := writeStubEnclave(t, "echo \"args: $*\" >&2\necho container-1\n")

	var (
		mu    sync.Mutex
		lines []string
	)
	if _, err := cli.Run(context.Background(), t.TempDir(), RunOpts{
		Tool:       "claude",
		Background: true,
		Progress: func(line string) {
			mu.Lock()
			lines = append(lines, line)
			mu.Unlock()
		},
	}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	mu.Lock()
	joined := strings.Join(lines, "\n")
	mu.Unlock()
	if strings.Contains(joined, "--pass-env") {
		t.Errorf("args %q carry --pass-env with no Env set", joined)
	}
}

// A sensitive directory is an option of `enclave`'s, not of the tool's, so it
// goes before the `--` that hands the rest to the tool: after the --pass-env
// flags, ahead of any verbatim extra flags.
func TestRunPassesSensitiveDirsBeforeTheSeparator(t *testing.T) {
	cli := writeStubEnclave(t, "echo \"args: $*\" >&2\necho container-1\n")
	workstreamDir := filepath.Join(t.TempDir(), "proj-x")

	joined := strings.Join(runCollectingProgress(t, cli, RunOpts{
		Tool:          "claude",
		SessionName:   "hs-1",
		Background:    true,
		Env:           map[string]string{"ANTHROPIC_MODEL": "claude-opus-5"},
		SensitiveDirs: []string{workstreamDir},
		ExtraFlags:    []string{"--extra"},
		ToolArgs:      []string{"--model", "claude-opus-5"},
	}), "\n")
	want := "args: run --tool claude --name hs-1 --background" +
		" --pass-env +ANTHROPIC_MODEL --add-sensitive-dir " + workstreamDir +
		" --extra -- --model claude-opus-5"
	if !strings.Contains(joined, want) {
		t.Errorf("stub saw %q, want %q", joined, want)
	}
}

// Without SensitiveDirs there is no --add-sensitive-dir: a session outside the
// default workstreams root must produce the same command it always did.
func TestRunWithoutSensitiveDirsPassesNoSensitiveDirFlag(t *testing.T) {
	cli := writeStubEnclave(t, "echo \"args: $*\" >&2\necho container-1\n")

	joined := strings.Join(runCollectingProgress(t, cli, RunOpts{
		Tool:       "claude",
		Background: true,
	}), "\n")
	if strings.Contains(joined, "--add-sensitive-dir") {
		t.Errorf("args %q carry --add-sensitive-dir with no SensitiveDirs set", joined)
	}
}

// The regression this exists for: a build that is slow but *working* must not be
// killed. A claude session's image build spends over a minute downloading a
// ~300 MB agent binary and takes longer than the old flat cap in total, so the
// daemon killed it mid-download and the session never appeared. What matters is
// that it keeps reporting progress, not how long it takes.
func TestRunSurvivesABuildThatIsSlowButStillReporting(t *testing.T) {
	// Ten steps at 60ms is 600ms of work — well past the idle window below, but
	// never silent for longer than a fraction of it.
	cli := writeStubEnclave(t, `
i=0
while [ $i -lt 10 ]; do
  echo "=> [$i/10] building" >&2
  sleep 0.06
  i=$((i+1))
done
echo "enclave-proj-claude-slow"
`)
	cli.runIdleTimeoutOverride = 250 * time.Millisecond

	name, err := cli.Run(context.Background(), t.TempDir(), RunOpts{Background: true})
	if err != nil {
		t.Fatalf("Run() error = %v; a build that keeps reporting must not be killed", err)
	}
	if name != "enclave-proj-claude-slow" {
		t.Errorf("Run() name = %q, want %q", name, "enclave-proj-claude-slow")
	}
}

// The other half of the same trade: silence is what the daemon gives up on, and
// it has to say that is what happened rather than name the wall-clock ceiling it
// never reached.
func TestRunGivesUpOnASilentBuild(t *testing.T) {
	cli := writeStubEnclave(t, "echo 'building' >&2\nsleep 5\n")
	cli.runIdleTimeoutOverride = 150 * time.Millisecond

	_, err := cli.Run(context.Background(), t.TempDir(), RunOpts{Background: true})
	if err == nil {
		t.Fatal("Run() error = nil, want the idle timeout")
	}
	if strings.Contains(err.Error(), "signal: killed") {
		t.Errorf("Run() error = %q; want the cap named, not the raw signal", err)
	}
	for _, want := range []string{"no output", "150ms", "building"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Run() error = %q; want it to mention %q", err, want)
		}
	}
}

// The ceiling still holds over a process that stays chatty forever, so a stuck
// build cannot hold its workstream busy indefinitely — and reports itself as the
// ceiling rather than as silence.
func TestRunCeilingAppliesToAChattyBuild(t *testing.T) {
	cli := writeStubEnclave(t, "while true; do echo 'still going' >&2; sleep 0.03; done\n")
	cli.runTimeoutOverride = 300 * time.Millisecond
	cli.runIdleTimeoutOverride = 10 * time.Second

	_, err := cli.Run(context.Background(), t.TempDir(), RunOpts{Background: true})
	if err == nil {
		t.Fatal("Run() error = nil, want the ceiling to fire")
	}
	if !strings.Contains(err.Error(), "300ms") {
		t.Errorf("Run() error = %q; want it to name the ceiling", err)
	}
	if strings.Contains(err.Error(), "no output") {
		t.Errorf("Run() error = %q; a build that never stopped printing did not go silent", err)
	}
}

// A cancelled context (a daemon shutting down mid-build) is not a timeout, and
// must not claim to be one.
func TestRunCancellationIsReportedAsCancelled(t *testing.T) {
	cli := writeStubEnclave(t, "sleep 1\n")
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	_, err := cli.Run(ctx, t.TempDir(), RunOpts{Background: true})
	if err == nil {
		t.Fatal("Run() error = nil, want a cancellation")
	}
	if !strings.Contains(err.Error(), "cancelled") {
		t.Errorf("Run() error = %q; want it to say it was cancelled", err)
	}
	if strings.Contains(err.Error(), "gave up after") {
		t.Errorf("Run() error = %q; a cancellation must not read as the timeout", err)
	}
}

// grandchildStub is a stub `enclave` that leaves a grandchild holding its output
// open for 20s, as the `docker build` a real one starts does. The grandchild
// ignores the hangup a closing terminal sends, so only killing it ends it.
const grandchildStub = "(trap '' HUP; exec sleep 20) &\necho building >&2\nwait\n"

// cancelSoon cancels the returned context after 300ms.
func cancelSoon(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	timer := time.AfterFunc(300*time.Millisecond, cancel)
	t.Cleanup(func() { timer.Stop(); cancel() })
	return ctx
}

// A cancelled run returns at once, even though a grandchild it left behind — a
// `docker build` the killed `enclave` started — would hold its output pipes open.
func TestRunCancelledReturnsWhileAGrandchildHoldsItsPipes(t *testing.T) {
	cli := writeStubEnclave(t, grandchildStub)

	started := time.Now()
	_, err := cli.Run(cancelSoon(t), t.TempDir(), RunOpts{Background: true})
	if err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Errorf("Run() error = %v; want it cancelled", err)
	}
	if took := time.Since(started); took > 5*time.Second {
		t.Errorf("Run() returned after %s; want it soon after the cancel, not when the grandchild exits", took)
	}
}

// A silent failure must not end in a dangling blank line where the stderr tail
// would have gone.
func TestRunFailureWithNoOutputHasNoTrailingNewline(t *testing.T) {
	cli := writeStubEnclave(t, "exit 3\n")

	_, err := cli.Run(context.Background(), t.TempDir(), RunOpts{Background: true})
	if err == nil {
		t.Fatal("Run() error = nil, want a failure")
	}
	if strings.HasSuffix(err.Error(), "\n") {
		t.Errorf("Run() error = %q; want no trailing newline when there is no tail", err)
	}
}

// Config reads Enclave's typed account of each option — its value and the layer
// it came from — for the tool, the directory and the run flags a session would
// start with.
func TestConfigReadsTheTypedEnvelope(t *testing.T) {
	cli := writeStubEnclave(t, `
[ "$*" = "config --json --tool codex --session-monitor --allow-domain cdn.example.com" ] || { echo "unexpected args: $*" >&2; exit 9; }
[ "$PWD" = "$EXPECT_DIR" ] || { echo "ran in $PWD" >&2; exit 8; }
echo '{"schemaVersion":"1","kind":"config","options":{"allow_domains":{"kind":"string_slice","value":["api.example.com","cdn.example.com"],"source":"cli"},"host_config":{"kind":"string","value":"passthrough","source":"tool_override"}},"valid":false,"error":"no tool"}'
`)
	dir := t.TempDir()
	t.Setenv("EXPECT_DIR", dir)
	got, err := cli.Config(context.Background(), dir, "codex", []string{"--session-monitor", "--allow-domain", "cdn.example.com"})
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	if domains := got.Options.List("allow_domains"); !slices.Equal(domains, []string{"api.example.com", "cdn.example.com"}) {
		t.Errorf("allow_domains = %q", domains)
	}
	if got.Options["allow_domains"].Source != "cli" || got.Options.String("host_config") != "passthrough" {
		t.Errorf("config = %+v", got)
	}
	if got.Valid || got.Error != "no tool" {
		t.Errorf("Valid, Error = %v, %q; want what Enclave said", got.Valid, got.Error)
	}
}

func TestConfigReportsAFailure(t *testing.T) {
	cli := writeStubEnclave(t, "echo 'unknown command \"config\"' >&2; exit 1\n")
	if _, err := cli.Config(context.Background(), t.TempDir(), "claude", nil); err == nil || !strings.Contains(err.Error(), `unknown command "config"`) {
		t.Errorf("Config on an Enclave that cannot answer = %v; want an error saying why", err)
	}
	cli = writeStubEnclave(t, "echo 'not json'\n")
	if _, err := cli.Config(context.Background(), t.TempDir(), "claude", nil); err == nil {
		t.Error("Config on output that is not JSON: want an error")
	}
	cli = writeStubEnclave(t, `echo '{"sources":{},"effective":{}}'`+"\n")
	if _, err := cli.Config(context.Background(), t.TempDir(), "claude", nil); err == nil || !strings.Contains(err.Error(), "update Enclave") {
		t.Errorf("Config on output without the versioned envelope = %v; want an error asking for a newer Enclave", err)
	}
}

func TestFeaturesReadsTheCatalogue(t *testing.T) {
	cli := writeStubEnclave(t, `
[ "$*" = "features list --json" ] || { echo "unexpected args: $*" >&2; exit 9; }
echo '{"schemaVersion":"1","kind":"feature","extensions":[{"name":"devtools","description":"Core development tools","source":"builtin","enabled":true,"managed":false},{"name":"playwright","displayName":"Playwright","description":"Playwright browsers and MCP server for UI testing","source":"builtin","enabled":false,"managed":false}]}'
`)
	got, err := cli.Features(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("Features: %v", err)
	}
	want := []sandboxschema.Feature{
		{Name: "devtools", Description: "Core development tools"},
		{Name: "playwright", DisplayName: "Playwright", Description: "Playwright browsers and MCP server for UI testing"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("Features = %+v; want %+v", got, want)
	}
}

func TestFeaturesReportsAFailure(t *testing.T) {
	cli := writeStubEnclave(t, "echo 'unknown command \"features\"' >&2; exit 1\n")
	if _, err := cli.Features(context.Background(), t.TempDir()); err == nil || !strings.Contains(err.Error(), `unknown command "features"`) {
		t.Errorf("Features on an Enclave that cannot answer = %v; want an error saying why", err)
	}
	cli = writeStubEnclave(t, `echo '{"extensions":[]}'`+"\n")
	if _, err := cli.Features(context.Background(), t.TempDir()); err == nil || !strings.Contains(err.Error(), "update Enclave") {
		t.Errorf("Features on output without the versioned envelope = %v; want an error asking for a newer Enclave", err)
	}
}

// Tools offers only the enabled tools, labelled with Enclave's display name.
func TestToolsReadsTheEnabledTools(t *testing.T) {
	cli := writeStubEnclave(t, `
[ "$*" = "tools list --json" ] || { echo "unexpected args: $*" >&2; exit 9; }
echo '{"schemaVersion":"1","kind":"tool","extensions":[{"name":"claude","displayName":"Claude Code","source":"builtin","enabled":true,"managed":false},{"name":"gemini","displayName":"Gemini CLI","source":"builtin","enabled":false,"managed":false},{"name":"mytool","source":"user","enabled":true,"managed":false}]}'
`)
	got, err := cli.Tools(context.Background())
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	want := []Tool{{Name: "claude", Label: "Claude Code"}, {Name: "mytool", Label: "mytool"}}
	if !slices.Equal(got, want) {
		t.Errorf("Tools = %+v; want %+v", got, want)
	}
}

func TestToolsReportsAFailure(t *testing.T) {
	cli := writeStubEnclave(t, "echo 'unknown flag: --json' >&2; exit 1\n")
	if _, err := cli.Tools(context.Background()); err == nil || !strings.Contains(err.Error(), "unknown flag: --json") {
		t.Errorf("Tools on an Enclave that cannot answer = %v; want an error saying why", err)
	}
	cli = writeStubEnclave(t, "echo '✓ claude (selected)'\n")
	if _, err := cli.Tools(context.Background()); err == nil {
		t.Error("Tools on output that is not JSON: want an error")
	}
	cli = writeStubEnclave(t, `echo '{"schemaVersion":"1","kind":"feature","extensions":[]}'`+"\n")
	if _, err := cli.Tools(context.Background()); err == nil || !strings.Contains(err.Error(), "update Enclave") {
		t.Errorf("Tools on another kind of listing = %v; want an error asking for a newer Enclave", err)
	}
}

// A restart continues the agent's conversation: the same flags, under
// `enclave continue` instead of `enclave run`.
func TestRunContinuesWhenAsked(t *testing.T) {
	cli := writeStubEnclave(t, "echo \"args: $*\" >&2\necho enclave-claude-x\n")
	lines := runCollectingProgress(t, cli, RunOpts{Tool: "claude", SessionName: "hs-1", Background: true, Continue: true})
	if joined := strings.Join(lines, "\n"); !strings.Contains(joined, "args: continue --tool claude --name hs-1 --background") {
		t.Errorf("args %q: want `enclave continue` with the run flags", joined)
	}
}

// A failed continue says it was a continue, not a run.
func TestRunFailureNamesTheVerb(t *testing.T) {
	cli := writeStubEnclave(t, "echo refused >&2\nexit 1\n")
	_, err := cli.Run(context.Background(), t.TempDir(), RunOpts{Background: true, Continue: true})
	if err == nil || !strings.HasPrefix(err.Error(), "enclave continue:") {
		t.Errorf("Run() error = %v; want it to name `enclave continue`", err)
	}
}
