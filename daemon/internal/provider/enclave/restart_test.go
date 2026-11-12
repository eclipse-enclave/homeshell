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

// A named session is restarted under its own name; an unnamed one — the project's default container, or one
// Enclave numbered because the default was taken — under a new HomeShell name, leaving its name to the terminal.
func TestRestartNames(t *testing.T) {
	tests := []struct {
		name            string
		container       string
		wantSessionName string // "" for a new hs- name
		wantErr         error
	}{
		{name: "the default container", container: "enclave-claude-abc123abc123"},
		{name: "an auto-numbered container", container: "enclave-claude-abc123abc123-1"},
		{name: "a named session", container: "enclave-claude-abc123abc123-feature", wantSessionName: "feature"},
		{name: "another tool's container", container: "enclave-codex-abc123abc123", wantErr: provider.ErrSessionNotRestartable},
		{name: "no Enclave container", container: "not-an-enclave-name", wantErr: provider.ErrSessionNotRestartable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target, sessionName, err := restartNames(tt.container, "claude")
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("restartNames = %v; want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			wantTarget := tt.container
			if tt.wantSessionName == "" {
				if !strings.HasPrefix(sessionName, sessionNamePrefix) {
					t.Fatalf("session name = %q; want a new %s name", sessionName, sessionNamePrefix)
				}
				wantTarget = "enclave-claude-abc123abc123-" + sessionName
			} else if sessionName != tt.wantSessionName {
				t.Errorf("session name = %q; want %q", sessionName, tt.wantSessionName)
			}
			if target != wantTarget {
				t.Errorf("target = %q; want %q", target, wantTarget)
			}
		})
	}
}

// The restart form is offered for an unnamed container too.
func TestCheckRestartOfTheDefaultContainer(t *testing.T) {
	p := restartProvider(t, &fakeCLI{available: true})
	container := Container{Name: "enclave-claude-abc123abc123", Tool: "claude", Directory: "/tmp/project/wt", Status: "running"}
	p.bridges.records[container.Name] = bridgeRecord{Container: container}
	if err := p.CheckRestart(provider.SessionRef{ProjectID: "proj-1", SessionID: container.Name}, "claude"); err != nil {
		t.Errorf("CheckRestart = %v; want nil", err)
	}
}

const defaultContainer = "enclave-codex-0123456789ab"

// adoptedDefault adopts the project's default codex container, started with a plain `enclave codex`.
func adoptedDefault(t *testing.T, background bool) (*Provider, *fakeCLI, *testBridge, provider.ProjectRef) {
	t.Helper()
	p, cli, bridge, project := bridgeProvider(t)
	listed(cli, project, Container{Name: defaultContainer, Tool: "codex", Directory: project.Path, Status: "running", Background: background})
	p.checkInventory(context.Background())
	if _, adopted := p.bridgeRecordFor(defaultContainer); !adopted {
		t.Fatal("the default container was not adopted")
	}
	cli.runBase = defaultContainer
	return p, cli, bridge, project
}

func restartOf(p *Provider, project provider.ProjectRef, container string) (provider.RestartFunc, error) {
	return p.PrepareRestart(context.Background(), provider.RestartSessionRequest{
		Session:    provider.SessionRef{ProjectID: project.ID, SessionID: container},
		WorkingDir: project.Path,
		OptionID:   "codex",
	})
}

// A restart of an unnamed session continues it under a new HomeShell name, in the background, and its row is
// replaced by the new session's, attached once. Until then the session shows alone, starting.
func TestRestartOfAnUnnamedSessionReplacesIt(t *testing.T) {
	p, cli, bridge, project := adoptedDefault(t, true)
	run, err := restartOf(p, project, defaultContainer)
	if err != nil {
		t.Fatal(err)
	}
	var attaching map[string]domain.Session
	bridge.onStart = func() { attaching = rows(t, p, project) }
	if err := run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(attaching) != 1 || attaching[defaultContainer].State != domain.SessionStarting {
		t.Errorf("rows while the new container was attached = %+v; want only the session, starting", attaching)
	}

	opts := cli.runCalls[0].opts
	if !opts.Continue || !opts.Background || !strings.HasPrefix(opts.SessionName, sessionNamePrefix) {
		t.Fatalf("run = %+v; want a background continue under a new %s name", opts, sessionNamePrefix)
	}
	replacement := defaultContainer + "-" + opts.SessionName
	shown := rows(t, p, project)
	if len(shown) != 1 || shown[replacement].Replaces != defaultContainer {
		t.Errorf("rows = %+v; want only %s, replacing %s", shown, replacement, defaultContainer)
	}
	if _, kept := reloaded(t, p, cli).bridgeRecordFor(defaultContainer); kept {
		t.Error("the records file still has the replaced session")
	}
	listed(cli, project, Container{Name: replacement, Tool: "codex", Directory: project.Path, Status: "running",
		Background: true, SessionName: opts.SessionName})
	p.checkInventory(context.Background())
	if len(bridge.starts) != 2 {
		t.Errorf("bridge started %d times; want twice, by the adoption and by the restart", len(bridge.starts))
	}
}

// A foreground session restarted in HomeShell ends in its terminal and goes on in the background, attached.
func TestRestartOfAForegroundSessionAttachesHomeShell(t *testing.T) {
	p, cli, bridge, project := bridgeProvider(t)
	foreground := cliSession(project.Path, false)
	foreground.ProjectDir = "/where/enclave/resolved/the/project"
	listed(cli, project, foreground)
	p.checkInventory(context.Background())
	cli.runResult = cliSessionName

	run, err := restartOf(p, project, cliSessionName)
	if err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cli.stopped, []string{cliSessionName}) || bridge.stops != 0 {
		t.Errorf("stopped %q and %d bridges; want the container, which has no bridge", cli.stopped, bridge.stops)
	}
	if opts := cli.runCalls[0].opts; opts.SessionName != "feature" || !opts.Continue || !opts.Background {
		t.Errorf("run = %+v; want a background continue under the same name", opts)
	}
	record, _ := p.bridgeRecordFor(cliSessionName)
	if record.Foreground || !record.Container.Background || record.Container.ProjectDir != foreground.ProjectDir {
		t.Errorf("record = %+v; want it attached in the background, in the same project", record)
	}
	if len(bridge.starts) != 1 {
		t.Errorf("bridge started %d times; want once", len(bridge.starts))
	}
}

// A restart under a new name cancelled by the daemon shutting down withdraws the new container, whichever point it
// got to: the next daemon has the session as it was, to end and continue, and no unrecorded hs- container.
func TestShutdownDuringARenamingRestart(t *testing.T) {
	tests := []struct {
		name   string
		cancel func(cli *fakeCLI, bridge *testBridge, cancel func())
	}{
		{"while the container comes up", func(cli *fakeCLI, _ *testBridge, cancel func()) { cli.onRun = cancel }},
		{"once it is attached", func(_ *fakeCLI, bridge *testBridge, cancel func()) { bridge.onStart = cancel }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, cli, bridge, project := adoptedDefault(t, true)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			tt.cancel(cli, bridge, cancel)
			run, err := restartOf(p, project, defaultContainer)
			if err != nil {
				t.Fatal(err)
			}
			if err := run(ctx); !errors.Is(err, provider.ErrRestartInterrupted) {
				t.Fatalf("restart = %v; want ErrRestartInterrupted", err)
			}

			replacement := defaultContainer + "-" + cli.runCalls[0].opts.SessionName
			if !slices.Contains(cli.stopped, replacement) {
				t.Errorf("stopped = %q; want the new container stopped", cli.stopped)
			}
			shown := rows(t, reloaded(t, p, cli), project)
			if _, kept := shown[defaultContainer]; len(shown) != 1 || !kept {
				t.Errorf("rows after a daemon restart = %+v; want the session as it was, alone", shown)
			}
		})
	}
}

// A daemon that crashed after a restart under a new name recorded the new container, but before it replaced the
// old record, completes the replacement on load.
func TestCrashDuringARenamingRestartIsCompletedOnLoad(t *testing.T) {
	p, cli, _, project := adoptedDefault(t, true)
	const replacement = defaultContainer + "-hs-1a2b"
	p.bridges.mu.Lock()
	p.bridges.setRecord(replacement, bridgeRecord{
		Container:     Container{Name: replacement, Tool: "codex", Directory: project.Path, Status: "running", Background: true},
		Replaces:      defaultContainer,
		ReplacingFrom: defaultContainer,
	})
	if err := p.bridges.save(); err != nil {
		t.Fatal(err)
	}
	p.bridges.mu.Unlock()

	again := reloaded(t, p, cli)
	if shown := rows(t, again, project); len(shown) != 1 || shown[replacement].Replaces != defaultContainer {
		t.Errorf("rows = %+v; want only %s, replacing %s", shown, replacement, defaultContainer)
	}
	if record, _ := reloaded(t, again, cli).bridgeRecordFor(replacement); record.ReplacingFrom != "" {
		t.Errorf("record = %+v; want the completed replacement saved", record)
	}
}

// A session its project owns through the folder Enclave resolved it to stays on that project's card while it
// restarts, so its title and settings are not pruned meanwhile.
func TestRestartingSessionOwnedThroughItsProjectDir(t *testing.T) {
	p, cli, _, project := bridgeProvider(t)
	session := cliSession(t.TempDir(), true)
	session.ProjectDir = project.Path
	listed(cli, project, session)
	p.checkInventory(context.Background())
	cli.runResult = cliSessionName
	var during domain.Session
	cli.onRun = func() { during = rows(t, p, project)[cliSessionName] }

	run, err := p.PrepareRestart(context.Background(), provider.RestartSessionRequest{
		Session:    provider.SessionRef{ProjectID: project.ID, SessionID: cliSessionName},
		WorkingDir: session.Directory,
		OptionID:   "codex",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if during.State != domain.SessionStarting {
		t.Errorf("while restarting: %+v; want the session shown, starting", during)
	}
}

// Continue of a foreground session that ended in its terminal goes on under a new HomeShell name.
func TestContinueOfAnEndedCLISession(t *testing.T) {
	p, cli, _, project := adoptedDefault(t, false)
	listed(cli, project)
	p.checkInventory(context.Background())
	if state := rows(t, p, project)[defaultContainer].State; state != domain.SessionDone {
		t.Fatalf("state = %s; want the session done", state)
	}
	ref := provider.SessionRef{ProjectID: project.ID, SessionID: defaultContainer}
	if err := p.StartSession(context.Background(), ref); !errors.Is(err, provider.ErrStartIsRestart) {
		t.Fatalf("StartSession = %v; want ErrStartIsRestart", err)
	}
	run, err := restartOf(p, project, defaultContainer)
	if err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background()); err != nil {
		t.Fatal(err)
	}
	opts := cli.runCalls[0].opts
	replacement := defaultContainer + "-" + opts.SessionName
	if !opts.Continue || !strings.HasPrefix(opts.SessionName, sessionNamePrefix) {
		t.Fatalf("run = %+v; want a continue under a new %s name", opts, sessionNamePrefix)
	}
	if shown := rows(t, p, project); len(shown) != 1 || shown[replacement].Replaces != defaultContainer {
		t.Errorf("rows = %+v; want only %s, replacing %s", shown, replacement, defaultContainer)
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

// A foreground session whose recorded options name a directory that is gone is refused before the container is
// stopped: the service checks the observed config against the schema, then PrepareRestart checks the directories.
func TestRestartOfAForegroundSessionIsRefusedBeforeItStops(t *testing.T) {
	p, cli, _, project := bridgeProvider(t)
	foreground := cliSession(project.Path, false)
	foreground.Options = map[string]json.RawMessage{"add_dirs": json.RawMessage(`["/no/such/directory"]`)}
	listed(cli, project, foreground)
	p.checkInventory(context.Background())

	ref := provider.SessionRef{ProjectID: project.ID, SessionID: cliSessionName}
	observed, known := p.ObservedLaunchConfig(context.Background(), ref, "codex")
	if !known {
		t.Fatal("the session's options were not observed")
	}
	_, err := p.PrepareRestart(context.Background(), provider.RestartSessionRequest{
		Session: ref, WorkingDir: project.Path, OptionID: "codex", Config: observed,
	})
	var validation *provider.ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("PrepareRestart = %v; want a ValidationError", err)
	}
	if len(cli.stopped) != 0 {
		t.Errorf("stopped %q; want nothing", cli.stopped)
	}
}

// restartingSession is a session with its restart prepared: a named one, restarted under its own name, or, renaming,
// the project's default container, restarted under a new one.
type restartingSession struct {
	provider *Provider
	cli      *fakeCLI
	bridge   *testBridge
	project  provider.ProjectRef
	ref      provider.SessionRef
	run      provider.RestartFunc
}

func prepareRestartOf(t *testing.T, renaming bool) restartingSession {
	t.Helper()
	if renaming {
		enclaveProvider, cli, bridge, project := adoptedDefault(t, true)
		run, err := restartOf(enclaveProvider, project, defaultContainer)
		if err != nil {
			t.Fatal(err)
		}
		ref := provider.SessionRef{ProjectID: project.ID, SessionID: defaultContainer}
		return restartingSession{enclaveProvider, cli, bridge, project, ref, run}
	}
	cli := &fakeCLI{available: true, runResult: restartContainer}
	enclaveProvider := restartProvider(t, cli)
	run, err := enclaveProvider.PrepareRestart(context.Background(), restartRequest(`{}`, false))
	if err != nil {
		t.Fatal(err)
	}
	project := provider.ProjectRef{ID: "proj-1", Path: "/tmp/project"}
	ref := provider.SessionRef{ProjectID: "proj-1", WorkstreamID: "ws1", SessionID: restartContainer}
	return restartingSession{enclaveProvider, cli, enclaveProvider.bridges.runtime.(*testBridge), project, ref, run}
}

// target is the container the restart brings back, once it ran.
func (session restartingSession) target() string {
	if session.ref.SessionID != defaultContainer {
		return session.ref.SessionID
	}
	return defaultContainer + "-" + session.cli.runCalls[0].opts.SessionName
}

// Preparing a restart changes nothing; while the container comes up the session reads as starting, in SessionState
// as in discovery. Dropping it, even for one poll, would also drop its custom title.
func TestRestartingSessionIsStarting(t *testing.T) {
	tests := []struct {
		name string
		read func(*testing.T, restartingSession) domain.Session
	}{
		{"in SessionState", func(t *testing.T, session restartingSession) domain.Session {
			read, err := session.provider.SessionState(context.Background(), session.ref)
			if err != nil {
				t.Errorf("SessionState: %v", err)
			}
			return read
		}},
		{"in discovery", func(t *testing.T, session restartingSession) domain.Session {
			found, err := session.provider.DiscoverSessions(context.Background(), session.project)
			if err != nil || len(found) != 1 {
				t.Errorf("discovered %+v, %v; want the one session", found, err)
				return domain.Session{}
			}
			return found[0].Session
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			session := prepareRestartOf(t, false)
			if before := tt.read(t, session); before.State != domain.SessionRunning {
				t.Fatalf("after preparing: %+v; want it running, unchanged", before)
			}
			var during domain.Session
			session.cli.onRun = func() { during = tt.read(t, session) }
			if err := session.run(context.Background()); err != nil {
				t.Fatal(err)
			}
			if during.State != domain.SessionStarting {
				t.Errorf("while the container came up: %+v; want it starting", during)
			}
		})
	}
}

// A restart that fails once the old container is stopped shows on the session's row alone, saying why, as a failed
// start does.
func TestRestartFailureIsReportedOnTheSession(t *testing.T) {
	const stray = "enclave-claude-abc123abc123-hs-other"
	tests := []struct {
		name      string
		renaming  bool
		arrange   func(*fakeCLI, *testBridge)
		wantError string
		// wantStopped is a container that is not the session's, stopped rather than left running.
		wantStopped string
	}{
		{name: "Enclave refuses it", wantError: "refusing to mount", arrange: func(cli *fakeCLI, _ *testBridge) {
			cli.runErr = errors.New("enclave continue: refusing to mount sensitive path ~/.ssh")
		}},
		{name: "it comes back under another name", wantError: "hs-other", wantStopped: stray,
			arrange: func(cli *fakeCLI, _ *testBridge) { cli.runResult = stray }},
		{name: "under a new name, it cannot attach nor be stopped", renaming: true, wantError: "bridge refused",
			arrange: func(cli *fakeCLI, bridge *testBridge) {
				bridge.startErr = errors.New("bridge refused")
				bridge.onStart = func() { cli.stopErr = errors.New("docker is not running") }
			}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			session := prepareRestartOf(t, tt.renaming)
			tt.arrange(session.cli, session.bridge)
			if err := session.run(context.Background()); err != nil {
				t.Fatalf("restart = %v; want the failure on the session instead", err)
			}
			shown := rows(t, session.provider, session.project)
			failed := shown[session.ref.SessionID]
			if len(shown) != 1 || failed.State != domain.SessionError || !strings.Contains(failed.Error, tt.wantError) {
				t.Errorf("rows = %+v; want only the session, failed, saying %q", shown, tt.wantError)
			}
			if tt.wantStopped != "" && !slices.Contains(session.cli.stopped, tt.wantStopped) {
				t.Errorf("stopped = %q; want %s stopped", session.cli.stopped, tt.wantStopped)
			}
		})
	}
}

// Removing a session while it restarts withdraws the restart, under the session's name or a new one: nothing of it
// is left, the removal is not refused for the container being down, and a container started meanwhile is stopped.
func TestRemoveSessionWithdrawsARestart(t *testing.T) {
	tests := []struct {
		name     string
		renaming bool
		// cancelDuringRun cancels the restart while the new container is coming up; otherwise it is cancelled
		// before the restart runs at all.
		cancelDuringRun bool
		// removeAfterRun removes the session only once the cancelled restart has ended; otherwise it is removed
		// right after the cancel, as the service does.
		removeAfterRun bool
		// keepContext withdraws by removal alone, without cancelling.
		keepContext bool
	}{
		{name: "before the restart runs"},
		{name: "while the container comes up", cancelDuringRun: true},
		{name: "while the container comes up, removed once the restart ended", cancelDuringRun: true, removeAfterRun: true},
		{name: "while the container comes up, uncancelled", cancelDuringRun: true, keepContext: true},
		{name: "under a new name, while the container comes up, uncancelled", renaming: true, cancelDuringRun: true,
			keepContext: true},
		{name: "under a new name, removed once the restart ended", renaming: true, cancelDuringRun: true,
			removeAfterRun: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			session := prepareRestartOf(t, tt.renaming)
			cli := session.cli
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			remove := func() {
				if tt.cancelDuringRun {
					// The restart has already stopped the old container.
					listed(cli, session.project)
					cli.stopErr = errors.New("no such container")
				}
				if err := session.provider.RemoveSession(context.Background(), session.ref); err != nil {
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
			stopsBeforeRun := 0
			if tt.cancelDuringRun {
				cli.onRun = func() {
					stopsBeforeRun = len(cli.stopped)
					withdraw()
				}
			} else {
				withdraw()
			}
			_ = session.run(ctx)
			if tt.removeAfterRun {
				remove()
			}

			if len(session.provider.bridges.records) != 0 || len(rows(t, session.provider, session.project)) != 0 {
				t.Errorf("records = %+v; want nothing of the session left", session.provider.bridges.records)
			}
			if tt.cancelDuringRun && !slices.Contains(cli.stopped[stopsBeforeRun:], session.target()) {
				t.Errorf("stopped = %q; want the container started meanwhile stopped", cli.stopped)
			}
			if !tt.cancelDuringRun && (len(cli.runCalls) != 0 || len(cli.stopped) != 1) {
				t.Errorf("ran %d and stopped %d times; want only the removal's stop", len(cli.runCalls), len(cli.stopped))
			}
		})
	}
}
