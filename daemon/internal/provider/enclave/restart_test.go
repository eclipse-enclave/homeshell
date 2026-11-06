//go:build dev

package enclave

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/provider"
)

const restartContainer = "enclave-claude-abc123abc123-hs-1a2b"

func restartRequest(config string, fresh bool) provider.RestartSessionRequest {
	return provider.RestartSessionRequest{
		Session:    provider.SessionRef{ProjectID: "proj-1", WorkstreamID: "ws1", SessionID: restartContainer},
		WorkingDir: "/tmp/project/wt",
		OptionID:   "claude",
		Config:     json.RawMessage(config),
		Fresh:      fresh,
	}
}

// restartNow prepares a restart and runs it to the end, as the service does.
func restartNow(enclaveProvider *Provider, req provider.RestartSessionRequest) error {
	run, err := enclaveProvider.PrepareRestart(context.Background(), req)
	if err != nil {
		return err
	}
	run(context.Background())
	return nil
}

// A restart replaces the container under the same session name, continuing the
// agent's conversation, with the new settings as flags.
func TestRestartSessionStopsAndContinuesUnderTheSameName(t *testing.T) {
	cli := &fakeCLI{available: true, runResult: restartContainer}
	p := restartProvider(t, cli)

	if err := restartNow(p, restartRequest(`{"enclave":{"hostConfig":true}}`, false)); err != nil {
		t.Fatalf("RestartSession: %v", err)
	}
	if !slices.Equal(cli.stopped, []string{restartContainer}) {
		t.Errorf("stopped = %q; want the session's container", cli.stopped)
	}
	if len(cli.runCalls) != 1 {
		t.Fatalf("run calls = %d; want 1", len(cli.runCalls))
	}
	opts := cli.runCalls[0].opts
	if opts.SessionName != "hs-1a2b" || !opts.Continue || opts.Tool != "claude" {
		t.Errorf("run = %+v; want `continue` for claude under the session's own name", opts)
	}
	if want := []string{"--host-config", "passthrough"}; !slices.Equal(opts.ExtraFlags, want) {
		t.Errorf("ExtraFlags = %q; want %q", opts.ExtraFlags, want)
	}
	if cli.runCalls[0].dir != "/tmp/project/wt" {
		t.Errorf("ran in %q; want the workstream's directory", cli.runCalls[0].dir)
	}
	// Back up, and checked for dying at startup like a new one.
	sess, err := p.SessionState(context.Background(), provider.SessionRef{ProjectID: "proj-1", SessionID: restartContainer})
	if err != nil || sess.State != domain.SessionRunning {
		t.Errorf("after the restart: %+v, %v; want it running", sess, err)
	}
}

func TestRestartSessionFreshStartsANewConversation(t *testing.T) {
	cli := &fakeCLI{available: true, runResult: restartContainer}
	p := restartProvider(t, cli)

	if err := restartNow(p, restartRequest(`{}`, true)); err != nil {
		t.Fatalf("RestartSession: %v", err)
	}
	if cli.runCalls[0].opts.Continue {
		t.Error("a fresh restart continued the last conversation")
	}
}

// While the container is down the session stays on the card, as starting —
// dropping it, even for one poll, would also drop its custom title.
func TestRestartingSessionIsDiscoveredAsStarting(t *testing.T) {
	cli := &fakeCLI{available: true, runResult: restartContainer}
	p := restartProvider(t, cli)

	p.bridges.restarting[restartContainer] = restartProjection{
		workingDir: "/tmp/project/wt",
		session:    domain.Session{ID: restartContainer, State: domain.SessionStarting},
	}
	// The old container is still listed on its way down.
	cli.containers = map[string][]Container{"/tmp/project": {{Name: restartContainer, Tool: "claude", Directory: "/tmp/project/wt", Status: "running"}}}

	found, err := p.DiscoverSessions(context.Background(), provider.ProjectRef{ID: "proj-1", Path: "/tmp/project"})
	if err != nil {
		t.Fatalf("DiscoverSessions: %v", err)
	}
	if len(found) != 1 || found[0].Session.State != domain.SessionStarting {
		t.Errorf("discovered = %+v; want the one session, starting", found)
	}
}

// A restart Enclave refuses after the old container is stopped — a mount it will
// not make, say — shows on the session, as a failed start does, and can be tried
// again: the container is already gone, so the second stop failing must not stop
// it.
func TestRestartSessionFailureIsReportedOnTheSession(t *testing.T) {
	cli := &fakeCLI{available: true, runErr: errors.New("enclave continue: refusing to mount sensitive path ~/.ssh")}
	p := restartProvider(t, cli)

	if err := restartNow(p, restartRequest(`{}`, false)); err != nil {
		t.Fatalf("RestartSession: %v; want the failure on the session instead", err)
	}
	sess, err := p.SessionState(context.Background(), provider.SessionRef{ProjectID: "proj-1", SessionID: restartContainer})
	if err != nil || sess.State != domain.SessionError || sess.Error == "" {
		t.Fatalf("after a failed restart: %+v, %v; want it in error, saying why", sess, err)
	}

	cli.runErr = nil
	cli.runResult = restartContainer
	cli.stopErr = errors.New("no such container")
	if err := restartNow(p, restartRequest(`{}`, false)); err != nil {
		t.Fatalf("RestartSession (retry): %v", err)
	}
	sess, _ = p.SessionState(context.Background(), provider.SessionRef{ProjectID: "proj-1", SessionID: restartContainer})
	if sess.State != domain.SessionRunning {
		t.Errorf("after the retry: %+v; want it running", sess)
	}
}

// A restart whose old container cannot be stopped leaves the session as it was —
// the container most likely still runs it — and says why to the service, rather
// than showing a failed start beside a container that is still up.
func TestRestartSessionStopFailureLeavesTheSessionAsItWas(t *testing.T) {
	cli := &fakeCLI{available: true, stopErr: errors.New("docker is not running")}
	p := restartProvider(t, cli)

	run, err := p.PrepareRestart(context.Background(), restartRequest(`{}`, false))
	if err != nil {
		t.Fatalf("PrepareRestart: %v", err)
	}
	if err := run(context.Background()); err == nil || !strings.Contains(err.Error(), "docker is not running") {
		t.Errorf("restart = %v; want the stop failure returned", err)
	}
	if len(cli.runCalls) != 0 {
		t.Error("started a container although the old one could not be stopped")
	}
	sess, _ := p.SessionState(context.Background(), provider.SessionRef{ProjectID: "proj-1", SessionID: restartContainer})
	if sess.State != domain.SessionRunning {
		t.Errorf("session = %+v; want it as it was, running", sess)
	}
	if r, _ := p.bridgeRecordFor(restartContainer); r.State == domain.SessionError {
		t.Error("the session was recorded as failed; removing it would then ignore a stop failure and orphan the container")
	}
	if _, restarting := p.bridges.restarting[restartContainer]; restarting {
		t.Error("the session is still reported as restarting")
	}
}

// A session whose start failed is restarted although stopping its container
// fails: the container is usually gone already.
func TestRestartOfAFailedSessionIgnoresTheStopFailure(t *testing.T) {
	cli := &fakeCLI{available: true, runResult: restartContainer, stopErr: errors.New("no such container")}
	p := restartProvider(t, cli)

	r := p.bridges.records[restartContainer]
	r.State = domain.SessionError
	r.Error = "failed start"
	p.bridges.records[restartContainer] = r

	if err := restartNow(p, restartRequest(`{}`, false)); err != nil {
		t.Fatalf("RestartSession: %v", err)
	}
	if len(cli.runCalls) != 1 {
		t.Fatalf("run calls = %d; want the session started again", len(cli.runCalls))
	}
	sess, _ := p.SessionState(context.Background(), provider.SessionRef{ProjectID: "proj-1", SessionID: restartContainer})
	if sess.State != domain.SessionRunning {
		t.Errorf("session = %+v; want it running", sess)
	}
}

// A container that comes back under another name is not the session's: the
// restart fails, on the session, and the stray container is stopped.
func TestRestartUnderAnotherNameFails(t *testing.T) {
	cli := &fakeCLI{available: true, runResult: "enclave-claude-abc123abc123-hs-other"}
	p := restartProvider(t, cli)

	if err := restartNow(p, restartRequest(`{}`, false)); err != nil {
		t.Fatalf("RestartSession: %v", err)
	}
	sess, _ := p.SessionState(context.Background(), provider.SessionRef{ProjectID: "proj-1", SessionID: restartContainer})
	if sess.State != domain.SessionError || !strings.Contains(sess.Error, "hs-other") {
		t.Errorf("session = %+v; want it in error, naming the container it got", sess)
	}
	if !slices.Contains(cli.stopped, "enclave-claude-abc123abc123-hs-other") {
		t.Errorf("stopped = %q; want the stray container stopped", cli.stopped)
	}
}

// A restart shows its output on the session as it comes, including the last
// lines before a pause: the build does not have to print again for them to show.
func TestRestartShowsItsOutputWhileItRuns(t *testing.T) {
	cli := &fakeCLI{available: true, runResult: restartContainer}
	p := restartProvider(t, cli)

	ref := provider.SessionRef{ProjectID: "proj-1", SessionID: restartContainer}
	var during domain.Session
	cli.onProgress = func(progress func(string)) {
		progress("Step 1/2: building")
		progress("Step 2/2: starting")
		// Nothing more is printed while the container comes up.
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if during, _ = p.SessionState(context.Background(), ref); strings.Contains(during.Preview, "Step 2/2") {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	if err := restartNow(p, restartRequest(`{}`, false)); err != nil {
		t.Fatalf("RestartSession: %v", err)
	}
	if during.Preview != "Step 1/2: building\nStep 2/2: starting" {
		t.Errorf("preview while the container came up = %q; want both lines", during.Preview)
	}
}

// `enclave continue` picks up the tool's latest conversation in the directory, so
// a session cannot continue its own while another of the same tool works there.
func TestContinueRefusal(t *testing.T) {
	tests := []struct {
		name               string
		optionID           string
		neighbourOptionIDs []string
		refused            bool
	}{
		{name: "alone", optionID: "claude"},
		{name: "beside another tool", optionID: "claude", neighbourOptionIDs: []string{"codex"}},
		{name: "beside the same tool", optionID: "claude", neighbourOptionIDs: []string{"codex", "claude"}, refused: true},
		{name: "as the default tool beside it", neighbourOptionIDs: []string{defaultTool}, refused: true},
		{name: "beside the default tool", optionID: defaultTool, neighbourOptionIDs: []string{""}, refused: true},
	}
	p := bareProvider(t, &fakeCLI{available: true})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := p.ContinueRefusal(tt.optionID, "/work/x", tt.neighbourOptionIDs)
			if got := errors.Is(err, provider.ErrCannotContinue); got != tt.refused {
				t.Fatalf("ContinueRefusal = %v; want ErrCannotContinue: %v", err, tt.refused)
			}
			if tt.refused && !strings.Contains(err.Error(), "another claude session works in /work/x") {
				t.Errorf("ContinueRefusal = %q; want it to name the tool and the directory", err)
			}
		})
	}
}

// A container started without a session name cannot be restarted as itself:
// `enclave run` without one would start the tool's default container instead.
func TestRestartSessionNeedsASessionName(t *testing.T) {
	cli := &fakeCLI{available: true}
	p := restartProvider(t, cli)
	req := restartRequest(`{}`, false)
	req.Session.SessionID = "enclave-claude-abc123abc123"
	if _, err := p.PrepareRestart(context.Background(), req); !errors.Is(err, provider.ErrSessionNotRestartable) {
		t.Errorf("PrepareRestart = %v; want ErrSessionNotRestartable", err)
	}
	// Refused before any settings are chosen, so the restart form is never offered.
	if err := p.CheckRestart(req.Session, "claude"); !errors.Is(err, provider.ErrSessionNotRestartable) {
		t.Errorf("CheckRestart = %v; want ErrSessionNotRestartable", err)
	}
	if err := p.CheckRestart(restartRequest(`{}`, false).Session, "claude"); err != nil {
		t.Errorf("CheckRestart of a named container = %v; want nil", err)
	}
	if len(cli.stopped) != 0 {
		t.Error("stopped a container it could not bring back")
	}
}

// A tool whose profile has no way to continue is restarted fresh instead of left
// down. Any other failure of `continue` — a build, Docker, a refused flag — would
// fail a fresh start the same way, so it is reported rather than retried.
func TestRestartSessionFallsBackToAFreshStart(t *testing.T) {
	refusal := errors.New("enclave run: exit status 1\ncontinue is not supported for pi: tool profile defines neither continue_args (latest session) nor resume_args (session picker)")
	tests := []struct {
		name          string
		fresh         bool
		continueErr   error
		runErr        error
		wantContinues []bool
		wantState     domain.SessionState
	}{
		{name: "the tool cannot continue", continueErr: refusal,
			wantContinues: []bool{true, false}, wantState: domain.SessionRunning},
		{name: "continue fails otherwise", continueErr: errors.New("enclave run: image build failed"),
			wantContinues: []bool{true}, wantState: domain.SessionError},
		{name: "continue works", wantContinues: []bool{true}, wantState: domain.SessionRunning},
		{name: "a fresh restart fails", fresh: true, runErr: errors.New("image build failed"),
			wantContinues: []bool{false}, wantState: domain.SessionError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cli := &fakeCLI{available: true, runResult: restartContainer, continueErr: tt.continueErr, runErr: tt.runErr}
			p := restartProvider(t, cli)

			if err := restartNow(p, restartRequest(`{}`, tt.fresh)); err != nil {
				t.Fatalf("RestartSession: %v", err)
			}
			var continues []bool
			for _, call := range cli.runCalls {
				continues = append(continues, call.opts.Continue)
			}
			if !slices.Equal(continues, tt.wantContinues) {
				t.Errorf("runs continued %v; want %v", continues, tt.wantContinues)
			}
			sess, err := p.SessionState(context.Background(), provider.SessionRef{ProjectID: "proj-1", SessionID: restartContainer})
			if err != nil || sess.State != tt.wantState {
				t.Errorf("after the restart: %+v, %v; want %s", sess, err, tt.wantState)
			}
		})
	}
}

// A restart starts the container again in the same directory, and Enclave's
// sensitive-mount guard checks `enclave continue` as it checks `enclave run`: a
// restart in a default-root workstream directory asks for the same exemption a new
// session does — on every start, the fresh one it falls back to included — or the
// old container is stopped and the new one refused.
func TestRestartVouchesForADefaultRootWorkstreamDirectory(t *testing.T) {
	refusal := errors.New("enclave run: exit status 1\ncontinue is not supported for pi: tool profile defines neither continue_args (latest session) nor resume_args (session picker)")
	tests := []struct {
		name        string
		workingDir  string
		continueErr error
		vouched     bool
		wantRuns    int
	}{
		{name: "continued in the root", workingDir: "root/proj-x", vouched: true, wantRuns: 1},
		{name: "fallen back to a fresh start in the root", workingDir: "root/proj-x", continueErr: refusal, vouched: true, wantRuns: 2},
		{name: "outside the root", workingDir: "outside", wantRuns: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := t.TempDir()
			for _, dir := range []string{"root/proj-x", "outside"} {
				if err := os.MkdirAll(filepath.Join(base, dir), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			cli := &fakeCLI{available: true, runResult: restartContainer, continueErr: tt.continueErr}
			p := restartProvider(t, cli)
			p.workstreamsRoot = func() (string, error) { return filepath.Join(base, "root"), nil }
			req := restartRequest(`{}`, false)
			req.WorkingDir = filepath.Join(base, tt.workingDir)
			record := p.bridges.records[restartContainer]
			record.Container.Directory = req.WorkingDir
			p.bridges.records[restartContainer] = record

			if err := restartNow(p, req); err != nil {
				t.Fatalf("RestartSession: %v", err)
			}
			if len(cli.runCalls) != tt.wantRuns {
				t.Fatalf("run calls = %d; want %d", len(cli.runCalls), tt.wantRuns)
			}
			var want []string
			if tt.vouched {
				want = []string{req.WorkingDir}
			}
			for i, call := range cli.runCalls {
				if !slices.Equal(call.opts.SensitiveDirs, want) {
					t.Errorf("run %d (continue=%v): SensitiveDirs = %q; want %q", i, call.opts.Continue, call.opts.SensitiveDirs, want)
				}
			}
		})
	}
}

// Preparing a restart changes nothing; the session reads as starting while the
// restart runs, in SessionState as in discovery.
func TestRestartingSessionStateIsStarting(t *testing.T) {
	cli := &fakeCLI{available: true, runResult: restartContainer}
	p := restartProvider(t, cli)

	ref := provider.SessionRef{ProjectID: "proj-1", SessionID: restartContainer}

	run, err := p.PrepareRestart(context.Background(), restartRequest(`{}`, false))
	if err != nil {
		t.Fatalf("PrepareRestart: %v", err)
	}
	before, err := p.SessionState(context.Background(), provider.SessionRef{ProjectID: "proj-1", SessionID: restartContainer})
	if err != nil || before.State != domain.SessionRunning {
		t.Fatalf("prepare changed session: %+v err=%v", before, err)
	}
	var during domain.Session
	cli.onRun = func() { during, _ = p.SessionState(context.Background(), ref) }
	run(context.Background())
	if during.State != domain.SessionStarting {
		t.Errorf("while the container came up: %+v; want it starting", during)
	}
}

// Removing a session while it restarts withdraws the restart: the session does not
// come back, the removal is not refused for the container being down, and a
// container started meanwhile is stopped.
func TestRemoveSessionWithdrawsARestart(t *testing.T) {
	ref := provider.SessionRef{ProjectID: "proj-1", WorkstreamID: "ws1", SessionID: restartContainer}
	tests := []struct {
		name string
		// cancelDuringRun cancels the restart while the new container is coming
		// up; otherwise it is cancelled before the restart runs at all.
		cancelDuringRun bool
		// removeAfterRun removes the session only once the cancelled restart has
		// ended; otherwise it is removed right after the cancel, as the service does.
		removeAfterRun bool
		// keepContext withdraws by removal alone, without cancelling.
		keepContext bool
	}{
		{name: "before the restart runs"},
		{name: "while the container comes up", cancelDuringRun: true},
		{name: "while the container comes up, removed once the restart ended", cancelDuringRun: true, removeAfterRun: true},
		{name: "while the container comes up, uncancelled", cancelDuringRun: true, keepContext: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cli := &fakeCLI{available: true, runResult: restartContainer}
			p := restartProvider(t, cli)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			run, err := p.PrepareRestart(context.Background(), restartRequest(`{}`, false))
			if err != nil {
				t.Fatalf("PrepareRestart: %v", err)
			}
			remove := func() {
				if tt.cancelDuringRun {
					// The restart has already stopped the old container.
					cli.stopErr = errors.New("no such container")
				}
				if err := p.RemoveSession(context.Background(), ref); err != nil {
					t.Errorf("RemoveSession: %v; want the restarting session removed", err)
				}
				cli.stopErr = nil
			}
			withdraw := func() {
				if !tt.keepContext {
					cancel()
				}
				if !tt.removeAfterRun {
					remove()
				}
			}
			if tt.cancelDuringRun {
				cli.onRun = withdraw
			} else {
				withdraw()
			}
			run(ctx)
			if tt.removeAfterRun {
				remove()
			}

			if sess, err := p.SessionState(context.Background(), ref); err == nil {
				t.Errorf("after the removal: %+v; want the session gone", sess)
			}
			found, _ := p.DiscoverSessions(context.Background(), provider.ProjectRef{ID: "proj-1", Path: "/tmp/project"})
			if len(found) != 0 {
				t.Errorf("discovered %+v; want nothing", found)
			}
			if tt.cancelDuringRun && cli.stopped[len(cli.stopped)-1] != restartContainer {
				t.Errorf("stopped = %q; want the container started meanwhile stopped", cli.stopped)
			}
			if !tt.cancelDuringRun && (len(cli.runCalls) != 0 || len(cli.stopped) != 1) {
				t.Errorf("ran %d and stopped %d times; want only the removal's stop", len(cli.runCalls), len(cli.stopped))
			}
		})
	}
}

// A directory that is not one here refuses the restart per field, before the
// running container is stopped. Paths resolve as Enclave resolves them: `~`
// against the home directory, a relative one against the working directory.
// Whether a directory may be mounted is Enclave's call, made when it starts.
func TestPrepareRestartRefusesDirectoriesThatDoNotExist(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workingDir := t.TempDir()
	for _, dir := range []string{filepath.Join(home, "shared"), filepath.Join(home, ".ssh"), filepath.Join(workingDir, "docs")} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		name      string
		path      string
		wantField bool
	}{
		{name: "an existing directory", path: workingDir},
		{name: "relative to the working directory", path: "docs"},
		{name: "under the home directory", path: "~/shared"},
		{name: "a sensitive directory, left to Enclave", path: "~/.ssh"},
		{name: "a missing relative directory", path: "shared", wantField: true},
		{name: "a missing directory", path: home + "/missing", wantField: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cli := &fakeCLI{available: true, runResult: restartContainer}
			p := restartProvider(t, cli)
			config, _ := json.Marshal(map[string]any{"enclave": map[string]any{"dirs": []map[string]any{{"path": tt.path}}}})
			req := restartRequest(string(config), false)
			req.WorkingDir = workingDir

			_, err := p.PrepareRestart(context.Background(), req)
			var validation *provider.ValidationError
			refused := errors.As(err, &validation)
			if refused != tt.wantField {
				t.Fatalf("PrepareRestart = %v; want refused = %v", err, tt.wantField)
			}
			if refused {
				if _, ok := validation.Fields["/enclave/dirs/0/path"]; !ok {
					t.Errorf("fields = %v; want the path named", validation.Fields)
				}
			} else if err != nil {
				t.Errorf("PrepareRestart: %v", err)
			}
			if len(cli.stopped) != 0 {
				t.Error("preparing a restart stopped the container")
			}
		})
	}
}

// A restart cancelled by its context alone brings nothing back: cancelled before
// it runs, it touches nothing; cancelled while the container comes up, it reports
// itself interrupted, leaving the session for the service to remove.
func TestCancelledRestartBringsNothingBack(t *testing.T) {
	for _, duringRun := range []bool{false, true} {
		cli := &fakeCLI{available: true, runResult: restartContainer}
		p := restartProvider(t, cli)

		run, err := p.PrepareRestart(context.Background(), restartRequest(`{}`, false))
		if err != nil {
			t.Fatalf("PrepareRestart: %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		if duringRun {
			cli.onRun = cancel
		} else {
			cancel()
		}
		want := context.Canceled
		if duringRun {
			want = provider.ErrRestartInterrupted
		}
		if err := run(ctx); !errors.Is(err, want) {
			t.Errorf("duringRun=%v: restart = %v; want %v", duringRun, err, want)
		}
		if !duringRun && (len(cli.stopped) != 0 || len(cli.runCalls) != 0) {
			t.Errorf("stopped %q and ran %d times; want a restart cancelled before it ran to touch nothing", cli.stopped, len(cli.runCalls))
		}
		if bridge := p.bridges.runtime.(*testBridge); len(bridge.starts) != 0 {
			t.Errorf("duringRun=%v: the session came back as started", duringRun)
		}
		cancel()
	}
}

// A restart's session belongs to every project that owns its directory, as its
// container does: one started from a project removed meanwhile is still shown,
// back up or failed, by another project sharing the directory.
func TestRestartFromARemovedProjectShowsInAProjectSharingItsDirectory(t *testing.T) {
	tests := []struct {
		name      string
		runErr    error
		wantState domain.SessionState
	}{
		{name: "back up", wantState: domain.SessionRunning},
		{name: "failed", runErr: errors.New("enclave continue: refusing to mount"), wantState: domain.SessionError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cli := &fakeCLI{available: true, runResult: restartContainer, runErr: tt.runErr}
			p := restartProvider(t, cli)

			if err := p.store.AddProject(&domain.Project{ID: "proj-2", Path: "/tmp/project/wt", Workstreams: []domain.Workstream{}}); err != nil {
				t.Fatalf("AddProject: %v", err)
			}
			run, err := p.PrepareRestart(context.Background(), restartRequest(`{}`, false))
			if err != nil {
				t.Fatalf("PrepareRestart: %v", err)
			}
			if _, err := p.store.RemoveProject("proj-1"); err != nil {
				t.Fatalf("RemoveProject: %v", err)
			}
			if err := run(context.Background()); err != nil {
				t.Fatalf("restart: %v", err)
			}

			found, err := p.DiscoverSessions(context.Background(), provider.ProjectRef{ID: "proj-2", Path: "/tmp/project/wt"})
			if err != nil {
				t.Fatalf("DiscoverSessions: %v", err)
			}
			if len(found) != 1 || found[0].Session.ID != restartContainer || found[0].Session.State != tt.wantState {
				t.Errorf("discovered = %+v; want the session, %s", found, tt.wantState)
			}
		})
	}
}

func restartProvider(t *testing.T, cli *fakeCLI) *Provider {
	t.Helper()
	p := sandboxProvider(t, cli)
	c := Container{Name: restartContainer, Tool: "claude", Directory: "/tmp/project/wt", Status: "running"}
	p.bridges.records[c.Name] = bridgeRecord{Container: c}
	return p
}
