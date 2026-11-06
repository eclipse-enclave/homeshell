package fake

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/provider"
	"github.com/eclipsesource/homeshell/daemon/internal/store"
)

func newProvider() *Provider { return New(Config{}) }

func TestIDIsTheDemoSeedsProvider(t *testing.T) {
	if got := newProvider().ID(); got != store.DemoProviderID {
		t.Errorf("ID() = %q, want store.DemoProviderID %q", got, store.DemoProviderID)
	}
}

func TestCanCreateAndSchema(t *testing.T) {
	p := newProvider()
	if !p.CanCreate(context.Background(), provider.ProjectRef{ID: "any"}) {
		t.Errorf("CanCreate = false; want true")
	}
	if len(p.Schema(context.Background(), provider.ProjectRef{})) == 0 {
		t.Errorf("Schema is empty")
	}
}

func TestCreateValidatesConfig(t *testing.T) {
	p := newProvider()
	ref := provider.ProjectRef{ID: "p1", Path: "/p1"}

	if _, err := p.Create(context.Background(), provider.CreateRequest{
		Project: ref, Config: json.RawMessage(`{}`),
	}); err == nil {
		t.Errorf("create without required branch = nil; want validation error")
	} else if provider.FieldErrors(err) == nil {
		t.Errorf("error %v is not a field-level ValidationError", err)
	}

	created, err := p.Create(context.Background(), provider.CreateRequest{
		Project: ref, Name: "feature", Config: json.RawMessage(`{"branch":"main"}`),
	})
	if err != nil {
		t.Fatalf("valid create: %v", err)
	}
	if created.Branch != "main" || created.Name != "feature" {
		t.Errorf("created = %+v; want branch=main name=feature", created)
	}
	if created.WorkingDir == "" {
		t.Errorf("created workstream has no WorkingDir")
	}
}

func TestOpenAndCreateSession(t *testing.T) {
	p := newProvider()
	wref := provider.WorkstreamRef{ProjectID: "p1", WorkstreamID: "w1"}

	session, err := p.CreateSession(context.Background(), provider.CreateSessionRequest{
		Workstream: wref, WorkingDir: "/p1/work", OptionID: "claude",
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if session.State != domain.SessionIdle {
		t.Errorf("new session state = %q, want idle", session.State)
	}

	sref := provider.SessionRef{ProjectID: "p1", WorkstreamID: "w1", SessionID: session.ID}
	if _, err := p.Open(context.Background(), sref, "terminal"); err != nil {
		t.Errorf("open terminal: %v", err)
	}
	if _, err := p.Open(context.Background(), sref, "ide"); err == nil {
		t.Errorf("open unsupported mode = nil; want error")
	}

	reported, err := p.SessionState(context.Background(), sref)
	if err != nil {
		t.Fatalf("session state: %v", err)
	}
	if reported.ID != session.ID || reported.State != domain.SessionIdle {
		t.Errorf("reported = %+v; want id=%s state=idle", reported, session.ID)
	}

	// DiscoverSessions reports the session tagged with its working directory.
	discovered, err := p.DiscoverSessions(context.Background(), provider.ProjectRef{ID: "p1"})
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(discovered) != 1 || discovered[0].WorkingDir != "/p1/work" || discovered[0].Session.ID != session.ID {
		t.Errorf("discovered = %+v; want one session in /p1/work", discovered)
	}

	if _, err := p.SessionState(context.Background(), provider.SessionRef{
		ProjectID: "p1", WorkstreamID: "w1", SessionID: "ghost",
	}); err == nil {
		t.Errorf("session state for missing session = nil; want error")
	}
}

func TestWatchCyclesSessionsAndNotifies(t *testing.T) {
	p := New(Config{
		Simulate:   true,
		SimProject: "p1", SimWorkingDir: "/p1/work", SimInterval: time.Millisecond,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	changes := make(chan struct{}, 64)
	go p.Watch(ctx, sinkFunc(func() { changes <- struct{}{} }))

	deadline := time.After(2 * time.Second)
	sawDemo := false
	for i := 0; i < 3; i++ {
		select {
		case <-changes:
		case <-deadline:
			t.Fatal("timed out waiting for simulator change notifications")
		}
		if ds, _ := p.DiscoverSessions(ctx, provider.ProjectRef{ID: "p1"}); len(ds) > 0 {
			sawDemo = true
		}
	}
	if !sawDemo {
		t.Errorf("simulator never added a session")
	}
}

func TestWatchNoopWhenSimulationDisabled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { newProvider().Watch(ctx, sinkFunc(func() {})); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Watch did not return promptly with simulation disabled")
	}
}

func TestFakeIsPrompter(t *testing.T) {
	var _ provider.Prompter = (*Provider)(nil)
}

func TestFakeSessionsHavePreview(t *testing.T) {
	p := New(Config{
		Simulate:   true,
		SimProject: "p1", SimWorkingDir: "/p1/work", SimInterval: time.Millisecond,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	changes := make(chan struct{}, 64)
	go p.Watch(ctx, sinkFunc(func() { changes <- struct{}{} }))

	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-changes:
		case <-deadline:
			t.Fatal("timed out waiting for a cycled session with a non-empty preview")
		}
		ds, _ := p.DiscoverSessions(ctx, provider.ProjectRef{ID: "p1"})
		if len(ds) > 0 && ds[0].Session.Preview != "" {
			return
		}
	}
}

func TestFakePromptEchoesIntoPreview(t *testing.T) {
	p := newProvider()
	wref := provider.WorkstreamRef{ProjectID: "p1", WorkstreamID: "w1"}
	sess, err := p.CreateSession(context.Background(), provider.CreateSessionRequest{
		Workstream: wref, WorkingDir: "/p1/work", OptionID: "claude",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	ref := provider.SessionRef{ProjectID: "p1", WorkstreamID: "w1", SessionID: sess.ID}

	if err := p.Prompt(context.Background(), ref, "hello"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	got, err := p.SessionState(context.Background(), ref)
	if err != nil {
		t.Fatalf("SessionState: %v", err)
	}
	if !strings.Contains(got.Preview, "hello") {
		t.Errorf("preview = %q; want it to contain %q", got.Preview, "hello")
	}

	// A key echoes into the preview too.
	if err := p.SendKey(context.Background(), ref, "down"); err != nil {
		t.Fatalf("SendKey: %v", err)
	}
	if got, _ = p.SessionState(context.Background(), ref); !strings.Contains(got.Preview, "[key: down]") {
		t.Errorf("preview = %q; want the key echo", got.Preview)
	}

	missing := provider.SessionRef{ProjectID: "p1", WorkstreamID: "w1", SessionID: "ghost"}
	if err := p.Prompt(context.Background(), missing, "hi"); err == nil {
		t.Errorf("Prompt for missing session = nil; want error")
	}
}

type sinkFunc func()

func (f sinkFunc) Changed() { f() }

// A project added again at the path of a removed one finds the sessions left
// running in its directory and below it, and only those. Discovery moves nothing:
// the project that put them there, and any other at the same path, still finds
// them.
func TestDiscoverSessionsOfAProjectAddedAgain(t *testing.T) {
	p := newProvider()
	for _, workingDir := range []string{"/p1/.fake-ws-1", "/p1", "/p10", "/other/.fake-ws-2"} {
		if _, err := p.CreateSession(context.Background(), provider.CreateSessionRequest{
			Workstream: provider.WorkstreamRef{ProjectID: "removed", WorkstreamID: "w1"}, WorkingDir: workingDir, OptionID: "claude",
		}); err != nil {
			t.Fatalf("create session: %v", err)
		}
	}
	discoveredDirs := func(project provider.ProjectRef) []string {
		t.Helper()
		discovered, err := p.DiscoverSessions(context.Background(), project)
		if err != nil {
			t.Fatalf("discover: %v", err)
		}
		var dirs []string
		for _, found := range discovered {
			dirs = append(dirs, found.WorkingDir)
		}
		return dirs
	}

	again := provider.ProjectRef{ID: "again", Path: "/p1/"}
	for _, project := range []provider.ProjectRef{again, {ID: "beside", Path: "/p1"}, again} {
		if got, want := discoveredDirs(project), []string{"/p1/.fake-ws-1", "/p1"}; !reflect.DeepEqual(got, want) {
			t.Errorf("%s discovered sessions in %q; want %q", project.ID, got, want)
		}
	}
	if got := discoveredDirs(provider.ProjectRef{ID: "removed"}); len(got) != 4 {
		t.Errorf("the project that put them there discovered sessions in %q; want all four", got)
	}
}

// Continuing is refused for a neighbour of the same option, no option standing
// for the first one, as Enclave takes none as its default tool.
func TestContinueRefusalTakesNoOptionAsTheDefault(t *testing.T) {
	tests := []struct {
		name       string
		optionID   string
		neighbours []string
		refused    bool
	}{
		{name: "same option", optionID: "codex", neighbours: []string{"codex"}, refused: true},
		{name: "other option", optionID: "codex", neighbours: []string{"claude"}},
		{name: "no option beside the default", optionID: "", neighbours: []string{"claude"}, refused: true},
		{name: "the default beside no option", optionID: "claude", neighbours: []string{""}, refused: true},
		{name: "no option beside another", optionID: "", neighbours: []string{"codex"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := newProvider().ContinueRefusal(tt.optionID, "/p1", tt.neighbours)
			if refused := errors.Is(err, provider.ErrCannotContinue); refused != tt.refused {
				t.Errorf("ContinueRefusal = %v; want refused: %v", err, tt.refused)
			}
		})
	}
}

// A restart nudges the sink as the session goes starting and again as it comes
// back running, so the demo shows each state without waiting for the next poll.
func TestRestartNotifiesTheSinkOfEachState(t *testing.T) {
	p := newProvider()
	wref := provider.WorkstreamRef{ProjectID: "p1", WorkstreamID: "w1"}
	sess, err := p.CreateSession(context.Background(), provider.CreateSessionRequest{
		Workstream: wref, WorkingDir: "/p1/work", OptionID: "claude",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	ref := provider.SessionRef{ProjectID: "p1", WorkstreamID: "w1", SessionID: sess.ID}
	var notified []domain.SessionState
	p.sink = sinkFunc(func() {
		got, err := p.SessionState(context.Background(), ref)
		if err != nil {
			t.Errorf("SessionState: %v", err)
			return
		}
		notified = append(notified, got.State)
	})

	restart, err := p.PrepareRestart(context.Background(), provider.RestartSessionRequest{
		Session: ref, WorkingDir: "/p1/work", OptionID: "claude",
	})
	if err != nil {
		t.Fatalf("PrepareRestart: %v", err)
	}
	if err := restart(context.Background()); err != nil {
		t.Fatalf("restart: %v", err)
	}
	want := []domain.SessionState{domain.SessionStarting, domain.SessionRunning}
	if !slices.Equal(notified, want) {
		t.Errorf("states notified = %v; want %v", notified, want)
	}
}

// The demo's out-of-date session is up to date once restarted.
func TestRestartClearsOutdatedSettings(t *testing.T) {
	fakeProvider := New(Config{Seed: map[string][]domain.Workstream{"p1": {{WorkingDir: "/p1/work", Sessions: []domain.Session{
		{ID: "s1", Title: "Tests", Agent: "Codex", State: domain.SessionIdle, OutdatedSettings: []string{"features"}},
	}}}}})
	ref := provider.SessionRef{ProjectID: "p1", WorkstreamID: "w1", SessionID: "s1"}
	restart, err := fakeProvider.PrepareRestart(context.Background(), provider.RestartSessionRequest{Session: ref, WorkingDir: "/p1/work", OptionID: "codex"})
	if err != nil {
		t.Fatalf("PrepareRestart: %v", err)
	}
	if err := restart(context.Background()); err != nil {
		t.Fatalf("restart: %v", err)
	}
	got, err := fakeProvider.SessionState(context.Background(), ref)
	if err != nil || got.OutdatedSettings != nil {
		t.Errorf("SessionState = %+v, %v; want nothing outdated", got, err)
	}
}
