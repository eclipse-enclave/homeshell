// Package enclave implements the Eclipse Enclave session provider,
// wrapping the enclave CLI binary for all container operations.
//
// The provider never talks to Docker directly — that is Enclave's job.
// Where the CLI surface is insufficient (see gaps.go), we document the
// gap and surface a structured error rather than working around it.
package enclave

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"

	"github.com/eclipsesource/homeshell/daemon/internal/provider/enclave/sandboxschema"
)

// CLI is the seam through which the provider calls the enclave binary.
// The real implementation shells out; tests inject a fake.
type CLI interface {
	// Available reports whether the enclave binary can be found.
	Available(ctx context.Context) (bool, error)

	// ListSessions returns the running containers via `enclave ps --json`. A
	// failed call is an error, so a caller can tell "not listed" from "could not
	// list".
	ListSessions(ctx context.Context, projectDir string) ([]Container, error)

	// ListAllSessions returns containers in every state via `enclave ps --all
	// --json`.
	ListAllSessions(ctx context.Context, projectDir string) ([]Container, error)

	// ExecOutput runs a command inside an already-running container and returns what
	// it printed. Used to ask the tool a question — today only "which models do you
	// have?" — so no session state is involved. It goes through a pseudo-terminal
	// because `enclave exec` refuses a non-TTY stdin, which is why the output
	// carries `\r` line endings for the caller to trim.
	ExecOutput(ctx context.Context, projectDir, sessionName, tool string, argv []string) ([]byte, error)

	// Run starts a container in detached mode. It returns the container
	// name printed to stdout by `enclave run --background`, and reports the
	// build/startup output line by line through opts.Progress as it arrives.
	Run(ctx context.Context, projectDir string, opts RunOpts) (containerName string, err error)

	// AttachCommand addresses the resolved CLI executable, not a shell string.
	AttachCommand(container string) []string

	// Stop stops and removes a container by name.
	Stop(ctx context.Context, containerName string) error

	// Tools returns the enabled tool profiles, via `enclave tools list --json`.
	Tools(ctx context.Context) ([]Tool, error)

	// Config returns what Enclave resolves for tool in projectDir, the way a run
	// with flags would resolve it, via `enclave config --json`. Without flags it
	// is what a session starts with unless told otherwise.
	Config(ctx context.Context, projectDir, tool string, flags []string) (ResolvedConfig, error)

	// Features returns the features Enclave can install, via `enclave features
	// list --json`. Which of them a session gets is its `features` option, from
	// Config.
	Features(ctx context.Context, projectDir string) ([]sandboxschema.Feature, error)
}

// RunOpts are the options passed to `enclave run`.
type RunOpts struct {
	Tool        string
	SessionName string
	Background  bool
	// Continue starts the container with `enclave continue` instead of `enclave
	// run`: the same flags, but the tool picks its last conversation back up — its
	// profile's continue arguments (`claude --continue`) — rather than starting a
	// new one. A restart uses it.
	Continue bool
	// ToolArgs are appended to the **tool's** own command line rather than to
	// `enclave`'s: Run passes them after a `--`, which `enclave run` forwards onto
	// the entrypoint it starts (`claude … --model <id>`). This is how a session
	// takes a setting the agent itself documents as a flag.
	//
	// Only for a profile whose entrypoint really is an agent CLI — `theia` runs
	// `sleep infinity`, and arguments meant for an agent would go there instead.
	ToolArgs []string
	// Env are environment variables the container's tool must start with, for a
	// tool that reads a setting from the environment instead of taking a flag.
	// Passing one takes both halves of `--pass-env +NAME`: the flag adds the
	// variable to the configured pass_env and Enclave reads its *value* from the
	// environment `enclave` itself runs in, so Run sets it on the child process too.
	Env map[string]string
	// SensitiveDirs are the directories Enclave should mount even though its
	// sensitive-mount guard would refuse them, passed as `--add-sensitive-dir`.
	// The project directory among them is only exempted, not mounted a second
	// time.
	SensitiveDirs []string
	// Additional flags to pass through verbatim.
	ExtraFlags []string
	// Progress, when set, receives each line `enclave run` writes while it works
	// — the image build and container startup output. Called as the lines arrive,
	// so the caller sees progress instead of one batch at the end. Optional; nil
	// discards the output as before.
	//
	// Called from **two** goroutines (one per stream), so it must be safe for
	// concurrent use.
	Progress func(line string)
}

// SessionFlags are the flags of a run that shape the session it starts, which
// `enclave config` takes to resolve the options that run gets. The tool is not
// among them: Config takes it on its own.
func (ro RunOpts) SessionFlags() []string {
	flags := []string{}
	// Sorted so the argv is the same for the same options — a command a log line
	// shows and a test can assert on. "+NAME" adds to the configured pass_env: a plain
	// list would replace it and lift Enclave's no_api_key and ephemeral gates.
	for _, name := range slices.Sorted(maps.Keys(ro.Env)) {
		flags = append(flags, "--pass-env", "+"+name)
	}
	// A session flag too: Enclave records the exemptions as add_sensitive_dirs,
	// so a drift check that left them out would report that option as changed.
	for _, dir := range ro.SensitiveDirs {
		flags = append(flags, "--add-sensitive-dir", dir)
	}
	return append(flags, ro.ExtraFlags...)
}

// Container is an enclave container as `enclave ps --json` reports it.
type Container struct {
	Name string
	Tool string
	// Directory is the directory the session runs in: its worktree as given to
	// Enclave, else the project directory Enclave resolved.
	Directory string
	// ProjectDir is the project directory Enclave resolved, which differs from
	// Directory for a session started in a git worktree of the project.
	ProjectDir  string
	Status      string
	SessionName string
	// Background is whether the session runs detached, as `enclave run --background` starts one. On a record,
	// Foreground says it: records saved before this was read have it unset.
	Background bool
	// Options are the typed values of the options the session was started with,
	// keyed like ResolvedConfig.Options; nil when Enclave did not record them.
	Options map[string]json.RawMessage
}

// ResolvedConfig is the `enclave config --json` envelope: the resolved options,
// and whether a run would accept them.
type ResolvedConfig struct {
	Options sandboxschema.EffectiveConfig
	// Valid is false for options a run would refuse, which Error says why.
	Valid bool
	Error string
}

// realCLI shells out to the enclave binary.
type realCLI struct {
	// pathMu guards binaryPath, the enclave binary Available() last resolved.
	pathMu     sync.Mutex
	binaryPath string
	// checkMu guards checked, checkContract's verdict on the binary it last asked.
	checkMu sync.Mutex
	checked contractCheck
	// runTimeoutOverride and runIdleTimeoutOverride override the two caps on
	// `enclave run`; zero means the constant. Only tests set them — waiting out the
	// real caps is not a test.
	runTimeoutOverride     time.Duration
	runIdleTimeoutOverride time.Duration
}

// NewCLI returns a CLI that shells out to the enclave binary found on PATH.
func NewCLI() CLI {
	return &realCLI{}
}

// cmdTimeout caps how long any single CLI invocation may run so a hung
// enclave process never blocks the daemon indefinitely.
const cmdTimeout = 30 * time.Second

// runIdleTimeout caps how long `enclave run` may go **silent** — which is the
// question a wall-clock cap got wrong. A build that is still printing is still
// working, however long it has been at it; one that has said nothing for this long
// is stuck. So the daemon measures the silence and not the duration.
//
// It has to clear the quietest legitimate stretch of a real build by a wide margin,
// and those stretches are long: the claude tool profile downloads a ~300 MB agent
// binary inside a single `RUN`, with no output at all while it does — measured at
// 61s on a fast link and 133s on a slow one, and it is the build working, not
// failing. Five minutes leaves room for a connection several times slower again.
//
// This replaced a flat 5-minute cap on the whole call, which killed those builds
// mid-download: a claude session took ~130s to come up when it went well and past
// 300s when the network was slow, so the cap fired on the slow half of an ordinary
// distribution and the session simply never appeared.
const runIdleTimeout = 5 * time.Minute

// runTimeout is the absolute ceiling on `enclave run`, well past any build that is
// genuinely progressing: it is there so a session cannot hold its workstream busy
// forever, not as the normal way a slow build ends — that is runIdleTimeout's job.
// Reaching this one means something is wrong in a way silence could not reveal (a
// process that keeps printing but never finishes).
//
// Both are named constants because the error messages quote them: when either
// fires, os/exec kills the process and reports a bare "signal: killed", which says
// nothing about why. Run turns that into a message naming the cap that fired, so
// the two cannot drift apart.
const runTimeout = 45 * time.Minute

// binaryName is the Eclipse Enclave CLI executable (formerly "yoloarena").
// Upstream derives the Docker container-name prefix from the same AppName
// ("enclave"), so this single constant doubles for both — keeping them from
// drifting apart on rename.
const binaryName = "enclave"

func (c *realCLI) Available(ctx context.Context) (bool, error) {
	path, err := exec.LookPath(binaryName)
	if err != nil {
		return false, fmt.Errorf("%s binary not found on PATH", binaryName)
	}
	c.pathMu.Lock()
	c.binaryPath = path
	c.pathMu.Unlock()
	if err := c.checkContract(ctx, path); err != nil {
		return false, err
	}
	return true, nil
}

// binaryIdentity tells one build of the enclave binary from another.
type binaryIdentity struct {
	path    string
	modTime int64
	size    int64
}

type contractCheck struct {
	binary binaryIdentity
	err    error
	// failed marks a call that failed, which says nothing about the contract, and
	// at when it was made.
	failed bool
	at     time.Time
}

// checkContract refuses an Enclave from before its versioned JSON contracts
// (eclipse-enclave/enclave#164). That Enclave's `ps --json` records no options,
// so no setting would ever show as changed; unavailable with a reason says what
// to do instead. It also refuses one from before `--pass-env +NAME`
// (eclipse-enclave/enclave#181), which SessionFlags relies on. Asked of
// `config --json --pass-env +<probe>` in the temp directory, which needs no
// Docker and is in no repository. The verdict is kept until the binary changes,
// so updating Enclave needs no daemon restart. A call that fails is no verdict:
// it is reused only for failedAnswerTTL, as Enclave's other failed answers are.
func (c *realCLI) checkContract(ctx context.Context, path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%s binary: %w", binaryName, err)
	}
	binary := binaryIdentity{path: path, modTime: info.ModTime().UnixNano(), size: info.Size()}
	c.checkMu.Lock()
	defer c.checkMu.Unlock()
	if c.checked.binary == binary && (!c.checked.failed || time.Since(c.checked.at) < failedAnswerTTL) {
		return c.checked.err
	}
	ctx, cancel := context.WithTimeout(ctx, cmdTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "config", "--json", "--pass-env", "+"+passEnvProbe)
	cmd.Dir = os.TempDir()
	out, err := cmd.Output()
	if ctx.Err() != nil {
		// Cut short, by its caller or its timeout: not even a failure of the binary.
		return fmt.Errorf("%s config --json: %w", binaryName, ctx.Err())
	}
	if err != nil {
		err = fmt.Errorf("%s config --json: %w%s", binaryName, err, stderrOf(err))
		c.checked = contractCheck{binary: binary, err: err, failed: true, at: time.Now()}
		return err
	}
	var doc struct {
		envelope
		Options sandboxschema.EffectiveConfig `json:"options"`
	}
	if err = json.Unmarshal(out, &doc); err != nil {
		err = fmt.Errorf("%s config --json: parsing its JSON: %w; update Enclave", binaryName, err)
	} else if err = doc.check("config --json", "config"); err == nil && !slices.Contains(doc.Options.List("pass_env"), passEnvProbe) {
		err = fmt.Errorf("%s config --json: --pass-env +NAME does not add to the configured list; update Enclave", binaryName)
	}
	c.checked = contractCheck{binary: binary, err: err}
	return err
}

// passEnvProbe is the variable checkContract adds with `--pass-env +NAME`.
const passEnvProbe = "HOMESHELL_CONTRACT_PROBE"

func (c *realCLI) binary() string {
	c.pathMu.Lock()
	defer c.pathMu.Unlock()
	if c.binaryPath != "" {
		return c.binaryPath
	}
	return binaryName
}

func (c *realCLI) ListSessions(ctx context.Context, projectDir string) ([]Container, error) {
	return c.ps(ctx, projectDir)
}

func (c *realCLI) ListAllSessions(ctx context.Context, projectDir string) ([]Container, error) {
	return c.ps(ctx, projectDir, "--all")
}

// ps runs `enclave ps --json` with flags from projectDir.
func (c *realCLI) ps(ctx context.Context, projectDir string, flags ...string) ([]Container, error) {
	ctx, cancel := context.WithTimeout(ctx, cmdTimeout)
	defer cancel()

	args := append([]string{"ps", "--json"}, flags...)
	cmd := exec.CommandContext(ctx, c.binary(), args...)
	cmd.Dir = projectDir
	out, err := cmd.Output()
	if ctx.Err() != nil {
		// Cut short, by its caller or its timeout: say so, not how it died.
		return nil, fmt.Errorf("%s %s: %w", binaryName, strings.Join(args, " "), ctx.Err())
	}
	if err != nil {
		// Output keeps the CLI's stderr on the ExitError; it says why the call
		// failed (Docker down, say), which the bare exit status does not.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			if reason := strings.TrimSpace(string(exitErr.Stderr)); reason != "" {
				return nil, fmt.Errorf("%s %s: %w: %s", binaryName, strings.Join(args, " "), err, reason)
			}
		}
		return nil, fmt.Errorf("%s %s: %w", binaryName, strings.Join(args, " "), err)
	}
	return parsePSJSON(out)
}

func (c *realCLI) Run(ctx context.Context, projectDir string, opts RunOpts) (string, error) {
	verb := "run"
	if opts.Continue {
		verb = "continue"
	}
	args := []string{verb}
	if opts.Tool != "" {
		args = append(args, "--tool", opts.Tool)
	}
	if opts.SessionName != "" {
		args = append(args, "--name", opts.SessionName)
	}
	if opts.Background {
		args = append(args, "--background")
	}
	args = append(args, opts.SessionFlags()...)
	// Last, and behind a `--`: everything after it belongs to the tool, not to
	// `enclave`. Omitted entirely when there is nothing for the tool.
	if len(opts.ToolArgs) > 0 {
		args = append(args, "--")
		args = append(args, opts.ToolArgs...)
	}

	limit := runTimeout
	if c.runTimeoutOverride > 0 {
		limit = c.runTimeoutOverride
	}
	idleLimit := runIdleTimeout
	if c.runIdleTimeoutOverride > 0 {
		idleLimit = c.runIdleTimeoutOverride
	}
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	// The idle watchdog cancels this inner context; the ceiling above and the
	// caller cancel the outer one. Keeping them separate is what lets the error
	// below name which of the three ended the call — from one context they are all
	// the same bare "signal: killed".
	runCtx, stopRun := context.WithCancel(ctx)
	defer stopRun()
	started := time.Now()

	cmd := exec.CommandContext(runCtx, c.binary(), args...)
	cmd.Dir = projectDir
	// The other half of `--pass-env +NAME` (SessionFlags): the flag names a variable,
	// and the value comes from this process's environment. Left nil, the child
	// inherits the daemon's environment, which is what every call without Env
	// wants.
	if len(opts.Env) > 0 {
		env := os.Environ()
		for name, value := range opts.Env {
			env = append(env, name+"="+value)
		}
		cmd.Env = env
	}
	// The ceiling, the idle watchdog and the caller's cancel would each kill
	// `enclave`, but only `enclave` — the `docker build` it started is a grandchild
	// and would survive, holding the pipes this function reads to the end. The
	// reads would then last until that orphaned build finishes: the deadline
	// decides when the process dies, and the *build* decides when the daemon hears
	// about it, minutes later, on a call that already gave up. So `enclave` leads a
	// process group of its own, and each of them kills all of it.
	ownProcessGroup(cmd)
	killGroupOnCancel(cmd)

	// Piped rather than cmd.Output(): a container build is the slowest thing the
	// daemon does, and buffering hides all of it until the end. Reading the pipes
	// as they fill is what lets the user watch it happen (opts.Progress). The two
	// streams carry different things — stdout ends with the container name,
	// stderr carries the build log — so they are scanned separately.
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("%s %s: stdout pipe: %w", binaryName, verb, err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("%s %s: stderr pipe: %w", binaryName, verb, err)
	}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("%s %s: %w", binaryName, verb, err)
	}

	var (
		mu      sync.Mutex
		lastOut string
		// lastLine is when either stream last produced output, and idleKilled
		// records that the watchdog below was the one that ended the call.
		lastLine   = started
		idleKilled bool
		// The tail of stderr, kept for the error message. cmd.Wait() cannot supply
		// it once StderrPipe is in use (ExitError.Stderr stays empty), so a failing
		// build would otherwise report an exit status and nothing else.
		errTail = make([]string, 0, runErrTailLines)
	)
	report := func(line string) {
		mu.Lock()
		lastLine = time.Now()
		mu.Unlock()
		if opts.Progress != nil {
			opts.Progress(line)
		}
	}

	// The idle watchdog. It polls rather than arming a timer per line, because a
	// build emits thousands of lines and this only has to notice silence, not
	// measure it precisely.
	watchdogDone := make(chan struct{})
	stopWatchdog := sync.OnceFunc(func() { close(watchdogDone) })
	defer stopWatchdog()
	go func() {
		tick := time.NewTicker(idleLimit / 4)
		defer tick.Stop()
		for {
			select {
			case <-watchdogDone:
				return
			case <-tick.C:
				mu.Lock()
				silent := time.Since(lastLine) >= idleLimit
				if silent {
					idleKilled = true
				}
				mu.Unlock()
				if silent {
					stopRun()
					return
				}
			}
		}
	}()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		scanOutput(stdout, func(line string) {
			mu.Lock()
			lastOut = line
			mu.Unlock()
			report(line)
		})
	}()
	go func() {
		defer wg.Done()
		scanOutput(stderr, func(line string) {
			mu.Lock()
			if len(errTail) == cap(errTail) {
				copy(errTail, errTail[1:])
				errTail = errTail[:len(errTail)-1]
			}
			errTail = append(errTail, line)
			mu.Unlock()
			report(line)
		})
	}()
	// Drain both pipes before Wait, which is what os/exec requires.
	wg.Wait()
	// Past this point both pipes are at EOF: the process has said everything it is
	// going to say, so silence is no longer evidence of anything and the watchdog
	// must not read the wait as a hang.
	stopWatchdog()

	if err := cmd.Wait(); err != nil {
		mu.Lock()
		tail := strings.Join(errTail, "\n")
		idle := idleKilled
		mu.Unlock()
		// A context that ran out is reported by os/exec as a bare "signal: killed":
		// it kills the process and `err` carries the signal, never the reason. Said
		// plainly that is the least useful error the daemon can produce — it looks
		// like the container died on its own, so it sends whoever reads it hunting
		// through Docker, the OOM killer and the image build instead of at a cap the
		// daemon itself imposed. Name the cap and how long it actually ran.
		// Silence first: the watchdog cancels the inner context, so ctx.Err() below
		// is nil on this path and the two would otherwise be indistinguishable.
		if idle {
			return "", fmt.Errorf(
				"%s %s: gave up after %s with no output at all, having run for %s (the daemon's limit on how long %s may go silent, not an error from it) — whatever it was doing had stopped reporting progress%s",
				binaryName, verb, idleLimit, time.Since(started).Round(time.Second), binaryName, tailSuffix(tail))
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", fmt.Errorf(
				"%s %s: gave up after %s (the daemon's limit for this call, not an error from %s) — a first container image build can take longer than that; Docker keeps the layers it finished, so retrying resumes from them%s",
				binaryName, verb, limit, binaryName, tailSuffix(tail))
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			return "", fmt.Errorf("%s %s: cancelled after %s%s",
				binaryName, verb, time.Since(started).Round(time.Second), tailSuffix(tail))
		}
		return "", fmt.Errorf("%s %s: %w%s", binaryName, verb, err, tailSuffix(tail))
	}
	// In background mode the last non-empty line of stdout is the container name.
	mu.Lock()
	name := lastOut
	mu.Unlock()
	if name == "" {
		return "", fmt.Errorf("%s %s produced no container name", binaryName, verb)
	}
	return name, nil
}

// tailSuffix appends a captured stderr tail to an error message, or nothing when
// there was no output. The old form always appended a newline, so a failure with a
// silent build — which a timeout usually is — ended in a dangling blank line.
func tailSuffix(tail string) string {
	if strings.TrimSpace(tail) == "" {
		return ""
	}
	return "\n" + tail
}

// stderrOf is what a command cmd.Output ran wrote to stderr before failing, as
// tailSuffix appends it: kept on the ExitError, and left out of its message.
func stderrOf(err error) string {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return ""
	}
	return tailSuffix(strings.TrimSpace(string(exitErr.Stderr)))
}

// runErrTailLines is how many trailing stderr lines a failed `enclave run`
// reports. Enough to carry the actual build error, bounded so a runaway build
// log cannot grow the daemon's memory.
const runErrTailLines = 20

// maxOutputLine caps a single captured line. Build tools redraw progress with
// very long escape-laden lines, and a `docker build` that emits megabytes
// without a newline must not become a megabyte-sized token.
const maxOutputLine = 8 * 1024

// scanOutput reads output and calls onLine for every non-empty plain-text line.
//
// It splits on carriage returns as well as newlines, because build progress is
// drawn by redrawing a line with \r — treating those as line ends turns each
// redraw into its own update instead of one unbounded line. ANSI escapes are
// stripped so what reaches the UI matches domain.Session.Preview's contract
// (plain text), and blank lines are dropped: they carry nothing in a tail and a
// \r\n pair would otherwise produce one every time.
func scanOutput(output io.Reader, onLine func(string)) {
	scanner := bufio.NewScanner(output)
	// Headroom over splitOutputLines' own cap, so the split function always gets
	// to hand back an over-long line before the scanner would give up on one.
	scanner.Buffer(make([]byte, 0, 4096), maxOutputLine*2)
	scanner.Split(splitOutputLines)
	for scanner.Scan() {
		if line := strings.TrimRight(stripANSI(scanner.Text()), " \t"); line != "" {
			onLine(line)
		}
	}
	if err := scanner.Err(); err != nil {
		// Reading stopped early, but this pipe still has to be drained: the child
		// writes into a fixed-size OS pipe and blocks forever once it fills, and a
		// build that has stopped producing output is one the idle watchdog kills as
		// hung. Reading the rest and discarding it is the only obligation left.
		slog.Debug("reading provider output stopped early; draining the rest", "error", err)
		_, _ = io.Copy(io.Discard, output)
	}
}

// splitOutputLines is a bufio.SplitFunc breaking on either \r or \n.
//
// A run of maxOutputLine bytes with neither in it is handed back as a line of
// its own rather than waited on. Truncating loses the tail of one redraw; the
// alternative is not "keep the whole line" but a scanner that reports
// bufio.ErrTooLong and stops reading — which strands the child on a full pipe
// and silences the output the caller is watching for progress.
func splitOutputLines(data []byte, atEOF bool) (int, []byte, error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF {
		return len(data), data, nil
	}
	if len(data) >= maxOutputLine {
		return maxOutputLine, data[:maxOutputLine], nil
	}
	return 0, nil, nil // ask for more
}

// ansiEscape matches the CSI/OSC sequences build tools use for colour and
// cursor movement.
var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[()][B0]|\x1b[=><]`)

// stripANSI removes escape sequences and other control bytes, leaving the text
// a terminal would have displayed.
func stripANSI(text string) string {
	text = ansiEscape.ReplaceAllString(text, "")
	return strings.Map(func(char rune) rune {
		if char == '\t' {
			return char
		}
		if char < 0x20 || char == 0x7f {
			return -1
		}
		return char
	}, text)
}

func (c *realCLI) Config(ctx context.Context, projectDir, tool string, flags []string) (ResolvedConfig, error) {
	ctx, cancel := context.WithTimeout(ctx, cmdTimeout)
	defer cancel()

	args := []string{"config", "--json"}
	if tool != "" {
		args = append(args, "--tool", tool)
	}
	cmd := exec.CommandContext(ctx, c.binary(), append(args, flags...)...)
	cmd.Dir = projectDir
	out, err := cmd.Output()
	if err != nil {
		return ResolvedConfig{}, fmt.Errorf("%s config: %w%s", binaryName, err, stderrOf(err))
	}
	var doc struct {
		envelope
		Options sandboxschema.EffectiveConfig `json:"options"`
		Valid   bool                          `json:"valid"`
		Error   string                        `json:"error"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		return ResolvedConfig{}, fmt.Errorf("%s config: parsing its JSON: %w", binaryName, err)
	}
	if err := doc.check("config", "config"); err != nil {
		return ResolvedConfig{}, err
	}
	return ResolvedConfig{Options: doc.Options, Valid: doc.Valid, Error: doc.Error}, nil
}

// envelope heads each versioned JSON document Enclave prints.
type envelope struct {
	SchemaVersion string `json:"schemaVersion"`
	Kind          string `json:"kind"`
}

// check refuses a document of another version, or of another kind than command
// prints: its fields may not mean what HomeShell reads them as. An Enclave from
// before eclipse-enclave/enclave#164 prints no version at all.
func (doc envelope) check(command, kind string) error {
	if doc.SchemaVersion == "" {
		return fmt.Errorf("%s %s: output is not versioned; update Enclave", binaryName, command)
	}
	if doc.SchemaVersion != "1" || doc.Kind != kind {
		return fmt.Errorf("%s %s: unsupported output (schemaVersion %q, kind %q); update Enclave", binaryName, command, doc.SchemaVersion, doc.Kind)
	}
	return nil
}

func (c *realCLI) Features(ctx context.Context, projectDir string) ([]sandboxschema.Feature, error) {
	ctx, cancel := context.WithTimeout(ctx, cmdTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, c.binary(), "features", "list", "--json")
	cmd.Dir = projectDir
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s features list: %w%s", binaryName, err, stderrOf(err))
	}
	var doc struct {
		envelope
		Extensions []sandboxschema.Feature `json:"extensions"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		return nil, fmt.Errorf("%s features list: parsing its JSON: %w", binaryName, err)
	}
	if err := doc.check("features list", "feature"); err != nil {
		return nil, err
	}
	return doc.Extensions, nil
}

func (c *realCLI) Stop(ctx context.Context, containerName string) error {
	ctx, cancel := context.WithTimeout(ctx, cmdTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, c.binary(), "stop", containerName)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s stop %s: %w\n%s", binaryName, containerName, err, out)
	}
	return nil
}

// maxExecOutput caps what ExecOutput reads; the command runs in an agent's container.
const maxExecOutput = 1 << 20

// ExecOutput runs argv inside a running container and returns its output.
// `enclave exec` requires a PTY; its output is read to EOF, up to maxExecOutput.
// The caller gets the bytes as the terminal produced them, `\r` and all.
func (c *realCLI) ExecOutput(ctx context.Context, projectDir, sessionName, tool string, argv []string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, cmdTimeout)
	defer cancel()

	args := append(execPrefix(sessionName, tool), argv...)
	cmd := exec.CommandContext(ctx, c.binary(), args...)
	cmd.Dir = projectDir
	// pty.Start makes the command a session (and so process group) leader; a
	// grandchild left holding the pty would keep the read below going, as in Run.
	killGroupOnCancel(cmd)
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("%s exec %v: %w", binaryName, argv, err)
	}
	defer func() { _ = ptmx.Close() }()
	// Read to EOF, then Wait: the pty closes when the command exits, and a command
	// that writes more than the pty buffer would block if this drained after Wait.
	// The read error at EOF on a pty is expected and not a failure of the command.
	out, _ := io.ReadAll(io.LimitReader(ptmx, maxExecOutput+1))
	if len(out) > maxExecOutput {
		cancel() // kills the group, so Wait returns
		_ = cmd.Wait()
		return nil, fmt.Errorf("%s exec %v: output over %d bytes", binaryName, argv, maxExecOutput)
	}
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%s exec %v: gave up: %w", binaryName, argv, ctx.Err())
		}
		return nil, fmt.Errorf("%s exec %v: %w", binaryName, argv, err)
	}
	return out, nil
}

// execPrefix builds the leading `exec [--name …] [--tool …] --` argv shared by
// shell and model-query commands.
func execPrefix(sessionName, tool string) []string {
	args := []string{"exec"}
	if sessionName != "" {
		args = append(args, "--name", sessionName)
	}
	if tool != "" {
		args = append(args, "--tool", tool)
	}
	return append(args, "--")
}

func (c *realCLI) Tools(ctx context.Context) ([]Tool, error) {
	ctx, cancel := context.WithTimeout(ctx, cmdTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, c.binary(), "tools", "list", "--json")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s tools list: %w%s", binaryName, err, stderrOf(err))
	}
	return parseToolsJSON(out)
}

// Tool is a tool profile Enclave can start a session with.
type Tool struct {
	Name  string
	Label string
}

// parseToolsJSON reads the enabled tools from `enclave tools list --json`.
func parseToolsJSON(out []byte) ([]Tool, error) {
	var doc struct {
		envelope
		Extensions []struct {
			Name        string `json:"name"`
			DisplayName string `json:"displayName"`
			Enabled     bool   `json:"enabled"`
		} `json:"extensions"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		return nil, fmt.Errorf("%s tools list: parsing its JSON: %w", binaryName, err)
	}
	if err := doc.check("tools list", "tool"); err != nil {
		return nil, err
	}
	var tools []Tool
	for _, extension := range doc.Extensions {
		if !extension.Enabled || extension.Name == "" {
			continue
		}
		label := extension.DisplayName
		if label == "" {
			label = extension.Name
		}
		tools = append(tools, Tool{Name: extension.Name, Label: label})
	}
	return tools, nil
}

// parsePSJSON reads the array `enclave ps --json` prints.
func parsePSJSON(out []byte) ([]Container, error) {
	var entries []struct {
		Name        string                     `json:"name"`
		Tool        string                     `json:"tool"`
		ProjectDir  string                     `json:"projectDir"`
		Worktree    string                     `json:"worktree"`
		Status      string                     `json:"status"`
		SessionName string                     `json:"sessionName"`
		Background  bool                       `json:"background"`
		Options     map[string]json.RawMessage `json:"options"`
	}
	if err := json.Unmarshal(out, &entries); err != nil {
		return nil, fmt.Errorf("%s ps: parsing its JSON: %w", binaryName, err)
	}
	containers := make([]Container, 0, len(entries))
	for _, entry := range entries {
		dir := entry.Worktree
		if dir == "" {
			dir = entry.ProjectDir
		}
		containers = append(containers, Container{
			Name:        entry.Name,
			Tool:        entry.Tool,
			Directory:   dir,
			ProjectDir:  entry.ProjectDir,
			Background:  entry.Background,
			Status:      entry.Status,
			SessionName: entry.SessionName,
			Options:     entry.Options,
		})
	}
	return containers, nil
}

// parseSessionName extracts the session name from a container name.
// Container names follow the pattern: enclave-<tool>-<12-char-hash>[-<session>]
func parseSessionName(containerName, tool string) string {
	prefix := binaryName + "-" + tool + "-"
	if !strings.HasPrefix(containerName, prefix) {
		return ""
	}
	rest := containerName[len(prefix):]
	// rest is "<hash>[-<session>]"; hash is 12 hex chars
	if len(rest) < 12 {
		return ""
	}
	after := rest[12:]
	if after == "" {
		return ""
	}
	if after[0] == '-' {
		return after[1:]
	}
	return ""
}

// marshalJSON is a helper that marshals to json.RawMessage, panicking on
// static types that cannot fail.
func marshalJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// AttachCommand resolves the same executable used for lifecycle operations.
func (c *realCLI) AttachCommand(container string) []string {
	return []string{c.binary(), "attach", container}
}

// ownProcessGroup starts cmd as the leader of a new process group, for
// killGroupOnCancel to kill. pty.Start does the same through Setsid, which
// excludes this.
func ownProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killGroupOnCancel makes cancelling cmd's context kill the whole process group
// cmd leads, not cmd alone: a grandchild that survived would hold cmd's output
// open, and a caller reading it to the end would wait for that grandchild.
func killGroupOnCancel(cmd *exec.Cmd) {
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}
