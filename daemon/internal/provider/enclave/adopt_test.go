//go:build dev

package enclave

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/provider"
	"github.com/eclipsesource/homeshell/daemon/internal/store"
	"github.com/eclipsesource/homeshell/daemon/internal/terminal"
)

const cliSessionName = "enclave-codex-0123456789ab-feature"

// cliSession is a container started with `enclave codex --name feature` in dir.
func cliSession(dir string, background bool) Container {
	return Container{Name: cliSessionName, Tool: "codex", Directory: dir, Status: "running", SessionName: "feature", Background: background}
}

// listed makes the fake CLI list containers in the project folder.
func listed(cli *fakeCLI, project provider.ProjectRef, containers ...Container) {
	cli.mu.Lock()
	defer cli.mu.Unlock()
	cli.containers = map[string][]Container{project.Path: containers}
}

// stoppedSession creates a session in HomeShell, with its launch recorded, and stops it.
func stoppedSession(t *testing.T, p *Provider, project provider.ProjectRef) provider.SessionRef {
	t.Helper()
	created := createBridge(t, p, project)
	ref := provider.SessionRef{ProjectID: project.ID, SessionID: created.ID}
	if err := p.store.SetLaunchConfig(project.ID, created.ID, domain.LaunchConfig{ProviderID: "enclave", OptionID: "codex"}); err != nil {
		t.Fatal(err)
	}
	if err := p.StopSession(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	return ref
}

// rows is what the project shows, by session ID.
func rows(t *testing.T, p *Provider, project provider.ProjectRef) map[string]domain.Session {
	t.Helper()
	out, err := p.DiscoverWatchedSessions(context.Background(), project)
	if err != nil {
		t.Fatal(err)
	}
	sessions := map[string]domain.Session{}
	for _, discovered := range out {
		sessions[discovered.Session.ID] = discovered.Session
	}
	return sessions
}

func TestInventoryAdopts(t *testing.T) {
	tests := []struct {
		name       string
		container  func(dir string) Container
		toolsErr   error
		wantRow    bool
		wantAttach bool
	}{
		{"background", func(dir string) Container { return cliSession(dir, true) }, nil, true, true},
		{"foreground", func(dir string) Container { return cliSession(dir, false) }, nil, true, false},
		{"IDE profile", func(dir string) Container {
			return Container{Name: "enclave-theia-0123456789ab", Tool: "theia", Directory: dir, Status: "running", Background: true}
		}, nil, false, false},
		{"profile that is not an agent's", func(dir string) Container {
			return Container{Name: "enclave-devbox-0123456789ab", Tool: "devbox", Directory: dir, Status: "running", Background: true}
		}, nil, false, false},
		{"tool profiles cannot be listed", func(dir string) Container { return cliSession(dir, true) },
			errors.New("enclave tools: exit status 1"), false, false},
		{"another daemon's", func(dir string) Container {
			return Container{Name: "enclave-codex-0123456789ab-hs-77", Tool: "codex", Directory: dir, Status: "running", Background: true}
		}, nil, false, false},
		{"exited", func(dir string) Container {
			container := cliSession(dir, true)
			container.Status = "exited"
			return container
		}, nil, false, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			p, cli, bridge, project := bridgeProvider(t)
			container := test.container(project.Path)
			cli.toolsErr = test.toolsErr
			listed(cli, project, container)
			if changed := p.checkInventory(context.Background()); changed != test.wantRow {
				t.Errorf("check reported a change: %v, want %v", changed, test.wantRow)
			}
			row, shown := rows(t, p, project)[container.Name]
			if shown != test.wantRow {
				t.Fatalf("row shown: %v, want %v", shown, test.wantRow)
			}
			var wantStarts [][]string
			if test.wantAttach {
				wantStarts = [][]string{{"enclave", "attach", container.Name}}
			}
			if !reflect.DeepEqual(bridge.starts, wantStarts) {
				t.Errorf("bridge starts = %q, want %q", bridge.starts, wantStarts)
			}
			if shown && row.State != domain.SessionRunning {
				t.Errorf("row state = %s, want running", row.State)
			}
			if record, _ := p.bridgeRecordFor(container.Name); shown && record.Foreground == test.wantAttach {
				t.Errorf("foreground = %v for a container started in the background: %v", record.Foreground, container.Background)
			}
		})
	}
}

func TestInventorySkipsContainersHomeShellIsStarting(t *testing.T) {
	t.Run("a container being created", func(t *testing.T) {
		p, cli, bridge, project := bridgeProvider(t)
		cli.onRun = func() {
			listed(cli, project, Container{Name: cli.runResult, Tool: "codex", Directory: project.Path, Status: "running", Background: true})
			p.checkInventory(context.Background())
		}
		createBridge(t, p, project)
		if len(bridge.starts) != 1 {
			t.Errorf("bridge started %d times; want once, by the create", len(bridge.starts))
		}
	})
	t.Run("a recorded container", func(t *testing.T) {
		p, cli, bridge, project := bridgeProvider(t)
		cli.runResult = cliSessionName
		createBridge(t, p, project)
		listed(cli, project, cliSession(project.Path, true))
		p.checkInventory(context.Background())
		if len(bridge.starts) != 1 {
			t.Errorf("bridge started %d times; want once, by the create", len(bridge.starts))
		}
	})
	t.Run("a container restarting", func(t *testing.T) {
		p, cli, bridge, project := bridgeProvider(t)
		cli.runResult = cliSessionName
		ref := stoppedSession(t, p, project)
		listed(cli, project, cliSession(project.Path, true))
		// The check lists the stopped session while a restart starts it again.
		running, release := make(chan struct{}), make(chan struct{})
		cli.onRun = func() {
			close(running)
			<-release
		}
		var once sync.Once
		restarted := make(chan error, 1)
		cli.onList = func() {
			once.Do(func() {
				go func() { restarted <- startBridge(t, p, ref, project.Path) }()
				<-running
			})
		}
		p.checkInventory(context.Background())
		close(release)
		if err := <-restarted; err != nil {
			t.Fatal(err)
		}
		if len(bridge.starts) != 2 {
			t.Errorf("bridge started %d times; want twice, by the create and the restart", len(bridge.starts))
		}
	})
}

func TestAdoptionThatCannotAttachLeavesTheContainerRunning(t *testing.T) {
	p, cli, bridge, project := bridgeProvider(t)
	listed(cli, project, cliSession(project.Path, true))
	bridge.startErr = errors.New("attach refused")
	p.checkInventory(context.Background())
	if _, recorded := p.bridgeRecordFor(cliSessionName); recorded {
		t.Error("a container that could not be attached is recorded")
	}
	if _, recorded := reloaded(t, p, cli).bridgeRecordFor(cliSessionName); recorded {
		t.Error("a container that could not be attached is recorded after a daemon restart")
	}
	if len(cli.stopped) != 0 {
		t.Errorf("stopped %q; the user's container must keep running", cli.stopped)
	}
	bridge.startErr = nil
	p.checkInventory(context.Background())
	if _, recorded := p.bridgeRecordFor(cliSessionName); !recorded || len(bridge.starts) != 2 {
		t.Errorf("next check: recorded %v after %d attaches; want it adopted on the second", recorded, len(bridge.starts))
	}
}

// reloaded is a daemon started again over p's records, with a bridge that still serves.
func reloaded(t *testing.T, p *Provider, cli *fakeCLI) *Provider {
	t.Helper()
	again, err := NewWithBridge(cli, &testBridge{live: true}, p.bridges.path)
	if err != nil {
		t.Fatal(err)
	}
	again.SetStore(p.store)
	return again
}

func TestAdoptedSessionSurvivesADaemonRestart(t *testing.T) {
	for _, background := range []bool{true, false} {
		p, cli, _, project := bridgeProvider(t)
		listed(cli, project, cliSession(project.Path, background))
		p.checkInventory(context.Background())
		again := reloaded(t, p, cli)
		if _, shown := rows(t, again, project)[cliSessionName]; !shown {
			t.Fatalf("background %v: the adopted session is gone after a daemon restart", background)
		}
		again.checkInventory(context.Background())
		if starts := again.bridges.runtime.(*testBridge).starts; len(starts) != 0 {
			t.Errorf("background %v: the restarted daemon attached the adopted session again: %q", background, starts)
		}
	}
}

func TestForegroundSessionEndsWhenItLeavesTheListing(t *testing.T) {
	tests := []struct {
		name      string
		unlist    func(cli *fakeCLI, project provider.ProjectRef)
		wantState domain.SessionState
	}{
		{"no longer listed", func(cli *fakeCLI, project provider.ProjectRef) { listed(cli, project) }, domain.SessionDone},
		{"listing fails", func(cli *fakeCLI, _ provider.ProjectRef) { cli.listErr = errors.New("Docker unavailable") },
			domain.SessionRunning},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			p, cli, _, project := bridgeProvider(t)
			listed(cli, project, cliSession(project.Path, false))
			p.checkInventory(context.Background())
			test.unlist(cli, project)
			p.checkInventory(context.Background())
			if got := rows(t, p, project)[cliSessionName].State; got != test.wantState {
				t.Errorf("state = %s, want %s", got, test.wantState)
			}
		})
	}
}

// A directory whose listing fails says nothing about its sessions; the other projects' listings still count.
func TestCLISessionsOfAProjectThatCannotListStayLive(t *testing.T) {
	p, cli, _, project := bridgeProvider(t)
	other := provider.ProjectRef{ID: "other", Path: t.TempDir()}
	p.SetStore(store.NewTestStore(t, []domain.Project{{ID: project.ID, Path: project.Path}, {ID: other.ID, Path: other.Path}}))
	otherSession := Container{Name: "enclave-codex-fedcba987654-feature", Tool: "codex", Directory: other.Path, Status: "running", SessionName: "feature"}
	cli.containers = map[string][]Container{project.Path: {cliSession(project.Path, false)}, other.Path: {otherSession}}
	p.checkInventory(context.Background())
	newSession := Container{Name: "enclave-codex-fedcba987654-next", Tool: "codex", Directory: other.Path, Status: "running", SessionName: "next"}
	cli.containers = map[string][]Container{other.Path: {newSession}}
	cli.listErrIn = map[string]error{project.Path: errors.New("Docker unavailable")}
	p.checkInventory(context.Background())
	if got := rows(t, p, project)[cliSessionName].State; got != domain.SessionRunning {
		t.Errorf("session of the project that could not list: %s, want running", got)
	}
	otherRows := rows(t, p, other)
	if got := otherRows[otherSession.Name].State; got != domain.SessionDone {
		t.Errorf("session gone from the other project's listing: %s, want done", got)
	}
	if got := otherRows[newSession.Name].State; got != domain.SessionRunning {
		t.Errorf("session new in the other project's listing: %q, want running", got)
	}
}

// A session in a folder two projects own stays live while either cannot list it.
func TestCLISessionInAFolderAProjectCannotListStaysLive(t *testing.T) {
	p, cli, _, project := bridgeProvider(t)
	other := provider.ProjectRef{ID: "other", Path: t.TempDir()}
	p.SetStore(store.NewTestStore(t, []domain.Project{
		{ID: project.ID, Path: project.Path},
		{ID: other.ID, Path: other.Path, Workstreams: []domain.Workstream{{ID: "ws", WorkingDir: project.Path}}},
	}))
	listed(cli, project, cliSession(project.Path, false))
	p.checkInventory(context.Background())
	cli.containers = nil
	cli.listErrIn = map[string]error{project.Path: errors.New("Docker unavailable")}
	p.checkInventory(context.Background())
	if got := rows(t, p, other)[cliSessionName].State; got != domain.SessionRunning {
		t.Errorf("state = %s, want running", got)
	}
}

func TestEndedCLISessionListedAgainIsLiveAgain(t *testing.T) {
	t.Run("foreground", func(t *testing.T) {
		p, cli, _, project := bridgeProvider(t)
		listed(cli, project, cliSession(project.Path, false))
		p.checkInventory(context.Background())
		listed(cli, project)
		p.checkInventory(context.Background())
		listed(cli, project, cliSession(project.Path, false))
		p.checkInventory(context.Background())
		if got := rows(t, p, project)[cliSessionName].State; got != domain.SessionRunning {
			t.Errorf("state = %s, want running", got)
		}
	})
	t.Run("background", func(t *testing.T) {
		p, cli, bridge, project := bridgeProvider(t)
		listed(cli, project, cliSession(project.Path, true))
		p.checkInventory(context.Background())
		// The session ends: its bridge is gone and `enclave ps --all` no longer lists it.
		pastStartup(p, cliSessionName)
		bridge.live = false
		p.sampleBridgeAndWait(context.Background(), cliSessionName, p.detectionAgents(), true)
		if got := rows(t, p, project)[cliSessionName].State; got != domain.SessionDone {
			t.Fatalf("state after the session ended = %s, want done", got)
		}
		p.checkInventory(context.Background())
		if got := rows(t, p, project)[cliSessionName].State; got.Terminal() || len(bridge.starts) != 2 {
			t.Errorf("state = %s after %d attaches; want it live, attached again", got, len(bridge.starts))
		}
	})
}

func TestSamplingSkipsForegroundRecords(t *testing.T) {
	p, cli, bridge, project := bridgeProvider(t)
	listed(cli, project, cliSession(project.Path, false))
	p.checkInventory(context.Background())
	p.changeDetector()(context.Background())
	p.waitForBridgeSamples()
	if bridge.dumps != 0 || cli.allCalls != 0 {
		t.Errorf("a foreground session was sampled (%d dumps) or recovered (%d listings)", bridge.dumps, cli.allCalls)
	}
}

// Ownership comes from what Enclave reported; the listing's project is not made up as the container's.
func TestAdoptionOwnsByWhatEnclaveReported(t *testing.T) {
	tests := []struct {
		name        string
		projectDir  func(project provider.ProjectRef) string
		wantAdopted bool
	}{
		{"resolved to the project", func(project provider.ProjectRef) string { return project.Path }, true},
		{"no project reported", func(provider.ProjectRef) string { return "" }, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			p, cli, _, project := bridgeProvider(t)
			container := cliSession(filepath.Join(project.Path, "unclaimed"), false)
			container.ProjectDir = test.projectDir(project)
			listed(cli, project, container)
			p.checkInventory(context.Background())
			record, adopted := p.bridgeRecordFor(cliSessionName)
			if adopted != test.wantAdopted {
				t.Fatalf("adopted = %v, want %v", adopted, test.wantAdopted)
			}
			if adopted && record.Container.ProjectDir != container.ProjectDir {
				t.Errorf("recorded project dir = %q, want Enclave's %q", record.Container.ProjectDir, container.ProjectDir)
			}
		})
	}
}

// A listing taken before HomeShell stopped or removed a foreground session neither brings it back, while it still
// lists it, nor ends it as done, once it does not: what the lifecycle wrote after the listing wins.
func TestInventoryKeepsWhatHomeShellDidDuringTheListing(t *testing.T) {
	remove := func(p *Provider, ref provider.SessionRef) error { return p.RemoveSession(context.Background(), ref) }
	stop := func(p *Provider, ref provider.SessionRef) error { return p.StopSession(context.Background(), ref) }
	tests := []struct {
		name        string
		act         func(p *Provider, ref provider.SessionRef) error
		stillListed bool
		wantShown   bool
		wantState   domain.SessionState
	}{
		{"removed, still listed", remove, true, false, ""},
		{"removed, no longer listed", remove, false, false, ""},
		{"stopped, still listed", stop, true, true, domain.SessionStopped},
		{"stopped, no longer listed", stop, false, true, domain.SessionStopped},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			p, cli, _, project := bridgeProvider(t)
			listed(cli, project, cliSession(project.Path, false))
			p.checkInventory(context.Background())
			if !test.stillListed {
				listed(cli, project)
			}
			var once sync.Once
			cli.onList = func() {
				once.Do(func() {
					if err := test.act(p, provider.SessionRef{ProjectID: project.ID, SessionID: cliSessionName}); err != nil {
						t.Error(err)
					}
				})
			}
			p.checkInventory(context.Background())
			row, shown := rows(t, p, project)[cliSessionName]
			if shown != test.wantShown || row.State != test.wantState {
				t.Errorf("shown %v as %q; want shown %v as %q", shown, row.State, test.wantShown, test.wantState)
			}
		})
	}
}

// The agent of an adopted session was running before HomeShell attached, so an exit right after is an end.
func TestAdoptedSessionExitingRightAfterAttachIsDone(t *testing.T) {
	p, cli, bridge, project := bridgeProvider(t)
	listed(cli, project, cliSession(project.Path, true))
	p.checkInventory(context.Background())
	bridge.live = false
	exitedSession := cliSession(project.Path, true)
	exitedSession.Status = "exited"
	cli.all = map[string][]Container{project.Path: {exitedSession}}
	p.sampleBridgeAndWait(context.Background(), cliSessionName, p.detectionAgents(), true)
	if got := rows(t, p, project)[cliSessionName].State; got != domain.SessionDone {
		t.Errorf("state = %s, want done", got)
	}
}

// endAdopted ends an adopted background session the way recovery finds it: bridge gone, container no longer listed.
func endAdopted(p *Provider, bridge *testBridge) {
	pastStartup(p, cliSessionName)
	bridge.mu.Lock()
	bridge.live = false
	bridge.mu.Unlock()
	p.sampleBridgeAndWait(context.Background(), cliSessionName, p.detectionAgents(), true)
}

// A session restarted while the check lists it, as it was before, is attached once, by the restart: one stopped in
// HomeShell, and one that had ended, restarted and ended again (listed running before and after).
func TestInventorySkipsASessionRestartedDuringTheListing(t *testing.T) {
	tests := []struct {
		name     string
		ended    func(t *testing.T, p *Provider, bridge *testBridge, project provider.ProjectRef) provider.SessionRef
		endAgain bool
	}{
		{"stopped in HomeShell", func(t *testing.T, p *Provider, _ *testBridge, project provider.ProjectRef) provider.SessionRef {
			return stoppedSession(t, p, project)
		}, false},
		{"ended, restarted and ended again", func(t *testing.T, p *Provider, bridge *testBridge, project provider.ProjectRef) provider.SessionRef {
			p.checkInventory(context.Background())
			endAdopted(p, bridge)
			if err := p.store.SetLaunchConfig(project.ID, cliSessionName, domain.LaunchConfig{ProviderID: "enclave", OptionID: "codex"}); err != nil {
				t.Fatal(err)
			}
			return provider.SessionRef{ProjectID: project.ID, SessionID: cliSessionName}
		}, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			p, cli, bridge, project := bridgeProvider(t)
			cli.runResult = cliSessionName
			listed(cli, project, cliSession(project.Path, true))
			ref := test.ended(t, p, bridge, project)
			var once sync.Once
			cli.onList = func() {
				once.Do(func() {
					if err := startBridge(t, p, ref, project.Path); err != nil {
						t.Error(err)
					}
					if test.endAgain {
						endAdopted(p, bridge)
					}
				})
			}
			p.checkInventory(context.Background())
			if len(bridge.starts) != 2 {
				t.Errorf("bridge started %d times; want twice, before the listing and by the restart", len(bridge.starts))
			}
		})
	}
}

// A restart of one session during the listing does not keep another ended one from being adopted again.
func TestInventoryReadoptsDespiteAnotherSessionRestartedDuringTheListing(t *testing.T) {
	p, cli, bridge, project := bridgeProvider(t)
	listed(cli, project, cliSession(project.Path, true))
	p.checkInventory(context.Background())
	endAdopted(p, bridge)
	own := stoppedSession(t, p, project)
	var once sync.Once
	cli.onList = func() {
		once.Do(func() {
			if err := startBridge(t, p, own, project.Path); err != nil {
				t.Error(err)
			}
		})
	}
	p.checkInventory(context.Background())
	if got := rows(t, p, project)[cliSessionName].State; got.Terminal() {
		t.Errorf("state = %s; want the ended session adopted again", got)
	}
}

// The tool profiles are listed only for a running container HomeShell has no record of, once a check.
func TestInventoryListsToolProfilesOnlyForUnrecordedSessions(t *testing.T) {
	tests := []struct {
		name      string
		arrange   func(t *testing.T, p *Provider, cli *fakeCLI, project provider.ProjectRef)
		wantCalls int
	}{
		{"nothing listed", func(*testing.T, *Provider, *fakeCLI, provider.ProjectRef) {}, 0},
		{"a recorded session", func(t *testing.T, p *Provider, cli *fakeCLI, project provider.ProjectRef) {
			cli.runResult = cliSessionName
			createBridge(t, p, project)
			listed(cli, project, cliSession(project.Path, true))
		}, 0},
		{"another daemon's session", func(_ *testing.T, _ *Provider, cli *fakeCLI, project provider.ProjectRef) {
			listed(cli, project, Container{Name: "enclave-codex-0123456789ab-hs-77", Tool: "codex",
				Directory: project.Path, Status: "running", SessionName: "hs-77", Background: true})
		}, 0},
		{"two unrecorded sessions", func(_ *testing.T, _ *Provider, cli *fakeCLI, project provider.ProjectRef) {
			other := Container{Name: "enclave-codex-fedcba987654-next", Tool: "codex", Directory: project.Path,
				Status: "running", SessionName: "next"}
			listed(cli, project, cliSession(project.Path, true), other)
		}, 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			p, cli, _, project := bridgeProvider(t)
			test.arrange(t, p, cli, project)
			before := cli.toolCalls
			p.checkInventory(context.Background())
			if got := cli.toolCalls - before; got != test.wantCalls {
				t.Errorf("listed the tool profiles %d times, want %d", got, test.wantCalls)
			}
		})
	}
}

// An ended session started again under its name needs no tool profiles to be adopted again.
func TestReadoptionWhenToolProfilesCannotBeListed(t *testing.T) {
	p, cli, _, project := bridgeProvider(t)
	cli.runResult = cliSessionName
	stoppedSession(t, p, project)
	cli.toolsErr = errors.New("enclave tools: exit status 1")
	listed(cli, project, cliSession(project.Path, true))
	p.checkInventory(context.Background())
	if got := rows(t, p, project)[cliSessionName].State; got.Terminal() {
		t.Errorf("state = %s; want the ended session adopted again", got)
	}
}

// A session HomeShell once started, started again in a terminal under its name, runs with the terminal's options.
func TestReadoptionDropsTheRecordedLaunchConfig(t *testing.T) {
	for _, background := range []bool{true, false} {
		t.Run(map[bool]string{true: "background", false: "foreground"}[background], func(t *testing.T) {
			p, cli, _, project := bridgeProvider(t)
			cli.runResult = cliSessionName
			stoppedSession(t, p, project)
			p.checkInventory(context.Background())
			if _, ok := p.store.LaunchConfig(cliSessionName); !ok {
				t.Fatal("the stopped session lost its launch config before it ran again")
			}

			listed(cli, project, cliSession(project.Path, background))
			p.checkInventory(context.Background())

			if got := rows(t, p, project)[cliSessionName].State; got.Terminal() {
				t.Fatalf("state = %s; want the session adopted again", got)
			}
			if launch, ok := p.store.LaunchConfig(cliSessionName); ok {
				t.Errorf("launch config = %+v; want it dropped", launch)
			}
		})
	}
}

// An ended session whose old bridge still answers at a socket this daemon no longer uses is not attached again.
func TestReadoptionRefusesWhileTheRecordedBridgeAnswers(t *testing.T) {
	p, cli, bridge, project := bridgeProvider(t)
	locator := &locatingBridge{testBridge: bridge, location: "/run/old"}
	p.bridges.runtime = locator
	listed(cli, project, cliSession(project.Path, true))
	p.checkInventory(context.Background())
	first := locator.Location(bridgeTarget(cliSessionName))
	endAdopted(p, bridge)
	locator.location, locator.elsewhere = "/run/new", []string{first}
	p.checkInventory(context.Background())
	if got := rows(t, p, project)[cliSessionName].State; got != domain.SessionDone || len(bridge.starts) != 1 {
		t.Errorf("state = %s after %d attaches; want it left done, attached once", got, len(bridge.starts))
	}
	locator.elsewhere = nil
	p.checkInventory(context.Background())
	if got := rows(t, p, project)[cliSessionName].State; got.Terminal() || len(bridge.starts) != 2 {
		t.Errorf("state = %s after %d attaches; want it attached again once the old bridge is gone", got, len(bridge.starts))
	}
}

func TestForegroundSessionCapabilities(t *testing.T) {
	want := &domain.SessionCapabilities{Live: false, TakeOver: true, StopWarning: foregroundStopWarning}
	tests := []struct {
		name   string
		record bridgeRecord
		want   *domain.SessionCapabilities
	}{
		{"live foreground", bridgeRecord{Foreground: true}, want},
		{"stopped foreground", bridgeRecord{Foreground: true, State: domain.SessionStopped}, nil},
		{"done foreground", bridgeRecord{Foreground: true, State: domain.SessionDone}, nil},
		{"own session", bridgeRecord{}, nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			p, _, _, _ := bridgeProvider(t)
			test.record.Container = cliSession("/proj", false)
			if got := p.bridgeSession(test.record).Capabilities; !reflect.DeepEqual(got, test.want) {
				t.Errorf("capabilities = %+v, want %+v", got, test.want)
			}
		})
	}
}

// adoptedForeground adopts a foreground session and returns its reference.
func adoptedForeground(t *testing.T) (*Provider, *fakeCLI, *testBridge, provider.ProjectRef, provider.SessionRef) {
	t.Helper()
	p, cli, bridge, project := bridgeProvider(t)
	listed(cli, project, cliSession(project.Path, false))
	p.checkInventory(context.Background())
	return p, cli, bridge, project, provider.SessionRef{ProjectID: project.ID, SessionID: cliSessionName}
}

func TestForegroundSessionOpensAShell(t *testing.T) {
	p, _, bridge, project, ref := adoptedForeground(t)
	launcher := &fakeLauncher{}
	p.term = launcher
	if _, err := p.Open(context.Background(), ref, "terminal"); err != nil {
		t.Fatal(err)
	}
	want := terminal.HoldOnStartupFailureIn(project.Path, "enclave", "exec", "--name", cliSessionName, "--tool", "codex", "--", "bash")
	if len(launcher.launched) != 1 || !reflect.DeepEqual(launcher.launched[0], want) {
		t.Errorf("launched %q, want [%q]", launcher.launched, want)
	}
	if bridge.dumps != 0 {
		t.Errorf("Open captured the bridge %d times", bridge.dumps)
	}
}

func TestForegroundSessionOpensNoShellWhileItRestarts(t *testing.T) {
	p, _, _, _, ref := adoptedForeground(t)
	launcher := &fakeLauncher{}
	p.term = launcher
	_, done, err := p.beginRestart(context.Background(), restartProjection{session: domain.Session{ID: cliSessionName}})
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	if _, err := p.Open(context.Background(), ref, "terminal"); !errors.Is(err, provider.ErrSessionStopped) {
		t.Errorf("error = %v, want ErrSessionStopped", err)
	}
	if len(launcher.launched) != 0 {
		t.Errorf("launched %q into a container being restarted", launcher.launched)
	}
}

func TestForegroundSessionRefusesInput(t *testing.T) {
	p, _, bridge, _, ref := adoptedForeground(t)
	for name, err := range map[string]error{
		"prompt":   p.Prompt(context.Background(), ref, "hello"),
		"send key": p.SendKey(context.Background(), ref, "Enter"),
	} {
		if !errors.Is(err, provider.ErrCapabilityUnsupported) {
			t.Errorf("%s: error = %v, want ErrCapabilityUnsupported", name, err)
		}
	}
	if len(bridge.starts) != 0 {
		t.Errorf("attached %d times", len(bridge.starts))
	}
}

func TestStopOfAForegroundSession(t *testing.T) {
	tests := []struct {
		name      string
		act       func(p *Provider, ref provider.SessionRef) error
		wantShown bool
	}{
		{"stop", func(p *Provider, ref provider.SessionRef) error { return p.StopSession(context.Background(), ref) }, true},
		{"remove", func(p *Provider, ref provider.SessionRef) error { return p.RemoveSession(context.Background(), ref) }, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			p, cli, bridge, project, ref := adoptedForeground(t)
			if err := test.act(p, ref); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(cli.stopped, []string{cliSessionName}) || bridge.stops != 0 {
				t.Errorf("stopped containers %q, bridge stops %d; want the container only", cli.stopped, bridge.stops)
			}
			row, shown := rows(t, p, project)[cliSessionName]
			if shown != test.wantShown || (shown && row.State != domain.SessionStopped) {
				t.Errorf("shown %v as %q; want shown %v", shown, row.State, test.wantShown)
			}
		})
	}
}
