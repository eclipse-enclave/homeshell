package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/store"
)

// startingStub is a session provider whose CreateSession the test drives: it
// emits the given startup lines, then blocks until the test releases it. That is
// what makes the *in-flight* window observable — the real providers this models
// (Enclave building an image) hold it open for minutes, and every assertion here
// is about what the UI can see during it.
type startingStub struct {
	stubSessionProvider
	entered chan struct{}
	release chan error
	lines   []string
	session domain.Session
	// mu guards the fields the service's goroutines touch while the test reads
	// them: discovery runs on the reconciler's goroutine, creation on another.
	mu         sync.Mutex
	discovered map[string][]DiscoveredSession
	workingDir string
	// schemaCalls counts the option lookups the service made. For Enclave each is
	// a CLI call with a timeout in the tens of seconds, so whether one happens at
	// all on the synchronous create path is worth pinning.
	schemaCalls atomic.Int32
}

// SessionSchemas records the lookup, then answers as the embedded stub does.
func (s *startingStub) SessionSchemas(ctx context.Context, ref WorkstreamRef) ([]SessionSchemaOption, error) {
	s.schemaCalls.Add(1)
	return s.stubSessionProvider.SessionSchemas(ctx, ref)
}

func newStartingStub(lines ...string) *startingStub {
	return &startingStub{
		stubSessionProvider: stubSessionProvider{
			id:        "slow",
			label:     "Slow Provider",
			available: true,
			sessionSchemas: []SessionSchemaOption{
				{ID: "claude", Label: "Claude"},
				{ID: "codex", Label: "Codex"},
			},
		},
		entered: make(chan struct{}),
		release: make(chan error, 1),
		lines:   lines,
		session: domain.Session{ID: "sess-real", Title: "Real session", Agent: "Claude", State: domain.SessionRunning},
	}
}

func (s *startingStub) CreateSession(_ context.Context, req CreateSessionRequest) (domain.Session, error) {
	for _, line := range s.lines {
		req.Progress(line)
	}
	close(s.entered)
	if err := <-s.release; err != nil {
		return domain.Session{}, err
	}
	// Report it from discovery too, as a real provider does (Enclave's `ps` lists
	// the container it just started). finishCreateSession reconciles the project
	// on success, and the reconciler keeps only what a refreshing provider still
	// reports — a stub that stayed silent would have its own session projected
	// away, which is the provider's bug to avoid, not the service's.
	s.mu.Lock()
	s.discovered = map[string][]DiscoveredSession{
		"p1": {{WorkingDir: s.workingDir, Session: s.session}},
	}
	s.mu.Unlock()
	return s.session, nil
}

func (s *startingStub) DiscoverSessions(_ context.Context, p ProjectRef) ([]DiscoveredSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.discovered[p.ID], nil
}

// serviceWithStartingStub wires a service over one project/workstream with the
// stub registered as a session provider.
func serviceWithStartingStub(t *testing.T, stub *startingStub) (*Service, *store.Store) {
	t.Helper()
	workingDir := t.TempDir()
	stub.workingDir = workingDir
	st := store.NewTestStore(t, []domain.Project{{
		ID: "p1", Name: "p1", Path: t.TempDir(),
		Workstreams: []domain.Workstream{{
			ID: "ws-1", Name: "ws", Provider: "core",
			StrategyID: "in-place", WorkingDir: workingDir,
		}},
	}})
	reg := NewRegistry()
	if err := reg.RegisterSessionProvider(stub); err != nil {
		t.Fatalf("registering the stub: %v", err)
	}
	return NewService(st, reg, 0), st
}

// sessionsOf reads a workstream's sessions out of the store.
func sessionsOf(t *testing.T, st *store.Store) []domain.Session {
	t.Helper()
	ws, err := st.Workstream("p1", "ws-1")
	if err != nil {
		t.Fatalf("reading the workstream: %v", err)
	}
	return ws.Sessions
}

// waitFor polls until cond holds, so the tests never depend on a fixed sleep
// matching the sink's flush cadence.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// The point of the placeholder: the moment the user asks for a session, the
// workstream shows what is coming up — named after the option they chose, in the
// starting state — instead of an empty list for the length of a build.
func TestCreateSessionShowsAStartingPlaceholder(t *testing.T) {
	stub := newStartingStub()
	svc, st := serviceWithStartingStub(t, stub)

	// With a config, which is what the web form always sends: that is what makes
	// the daemon read the provider's options, and the label comes off them.
	created, err := svc.CreateSession(context.Background(), CreateSessionInput{
		ProjectID: "p1", WorkstreamID: "ws-1", ProviderID: "slow", OptionID: "claude",
		Config: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	<-stub.entered

	sessions := sessionsOf(t, st)
	if len(sessions) != 1 {
		t.Fatalf("sessions = %d, want the placeholder", len(sessions))
	}
	placeholder := sessions[0]
	// What the caller gets back is the row it can find in the projection.
	if created.ID != placeholder.ID {
		t.Errorf("returned session ID = %q, want the placeholder's %q", created.ID, placeholder.ID)
	}
	if !IsStartingPlaceholder(placeholder) {
		t.Errorf("session ID = %q, want the %q prefix", placeholder.ID, StartingIDPrefix)
	}
	if !placeholder.Placeholder {
		t.Error("the placeholder does not say it is one")
	}
	if placeholder.State != domain.SessionStarting {
		t.Errorf("state = %q, want %q", placeholder.State, domain.SessionStarting)
	}
	// The option's label, not its ID: naming what is starting is the feature.
	if placeholder.Agent != "Claude" {
		t.Errorf("agent = %q, want the option label %q", placeholder.Agent, "Claude")
	}
	if placeholder.Provider != "slow" {
		t.Errorf("provider = %q, want the creating provider", placeholder.Provider)
	}

	// And no workstream status message: the row above already says what is
	// starting, so a "Starting Claude…" line beside it would say it twice.
	ws, _ := st.Workstream("p1", "ws-1")
	if ws.StatusMessage != "" {
		t.Errorf("status message = %q, want none while a placeholder says the same thing", ws.StatusMessage)
	}

	stub.release <- nil
}

// A create supersedes the last one's failure: the stale error must not sit on the
// workstream for the whole of the new attempt.
func TestCreateSessionClearsAnEarlierFailure(t *testing.T) {
	stub := newStartingStub()
	svc, st := serviceWithStartingStub(t, stub)
	if err := st.SetStatusMessage("p1", "ws-1", "start enclave container: exit status 1"); err != nil {
		t.Fatalf("seeding the earlier failure: %v", err)
	}

	if _, err := svc.CreateSession(context.Background(), CreateSessionInput{
		ProjectID: "p1", WorkstreamID: "ws-1", ProviderID: "slow", OptionID: "claude",
	}); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	<-stub.entered

	ws, _ := st.Workstream("p1", "ws-1")
	if ws.StatusMessage != "" {
		t.Errorf("status message = %q, want the earlier failure cleared", ws.StatusMessage)
	}

	stub.release <- nil
}

// A different option must produce a differently-named placeholder — otherwise
// "which session is starting" is decoration rather than information.
func TestStartingPlaceholderNamesTheChosenOption(t *testing.T) {
	stub := newStartingStub()
	svc, st := serviceWithStartingStub(t, stub)

	if _, err := svc.CreateSession(context.Background(), CreateSessionInput{
		ProjectID: "p1", WorkstreamID: "ws-1", ProviderID: "slow", OptionID: "codex",
		Config: json.RawMessage(`{}`),
	}); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	<-stub.entered

	if got := sessionsOf(t, st)[0].Agent; got != "Codex" {
		t.Errorf("agent = %q, want %q", got, "Codex")
	}
	stub.release <- nil
}

// A create carrying no config needs no schema, so the daemon does not ask for
// one — for Enclave that is a CLI call with a timeout in the tens of seconds, on
// a request whose whole point is to return before the work starts. The row wears
// the option's ID until the real session replaces it and names itself.
func TestStartingPlaceholderWithoutAConfigCostsNoSchemaLookup(t *testing.T) {
	stub := newStartingStub()
	svc, st := serviceWithStartingStub(t, stub)

	if _, err := svc.CreateSession(context.Background(), CreateSessionInput{
		ProjectID: "p1", WorkstreamID: "ws-1", ProviderID: "slow", OptionID: "codex",
	}); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	<-stub.entered

	if got := stub.schemaCalls.Load(); got != 0 {
		t.Errorf("SessionSchemas called %d times; want none without a config to validate", got)
	}
	if got := sessionsOf(t, st)[0].Agent; got != "codex" {
		t.Errorf("agent = %q, want the option id %q", got, "codex")
	}
	stub.release <- nil
}

// awaitStartupPreview drives a create whose provider emits the given lines and
// returns the placeholder's Preview once the sink has published it. The caller
// releases the stub.
func awaitStartupPreview(t *testing.T, stub *startingStub, st *store.Store, svc *Service) string {
	t.Helper()
	if _, err := svc.CreateSession(context.Background(), CreateSessionInput{
		ProjectID: "p1", WorkstreamID: "ws-1", ProviderID: "slow", OptionID: "claude",
	}); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	<-stub.entered

	var preview string
	waitFor(t, "the startup output to reach the preview", func() bool {
		sessions := sessionsOf(t, st)
		if len(sessions) == 0 {
			return false
		}
		preview = sessions[0].Preview
		return preview != ""
	})
	return preview
}

// The startup output has to reach the placeholder's Preview — the field the UI
// already renders as a session's output box.
//
// A build shorter than the retention is kept *whole*: the box shows ten lines and
// the user scrolls back through the rest, so discarding history under the bound
// would leave nothing above the fold.
func TestStartupOutputLandsOnThePlaceholderPreview(t *testing.T) {
	lines := make([]string, 0, 25)
	for i := 0; i < 25; i++ {
		lines = append(lines, "build step "+string(rune('a'+i)))
	}
	stub := newStartingStub(lines...)
	svc, st := serviceWithStartingStub(t, stub)

	preview := awaitStartupPreview(t, stub, st, svc)

	got := strings.Split(preview, "\n")
	if len(got) != len(lines) {
		t.Errorf("preview has %d lines, want all %d that were emitted", len(got), len(lines))
	}
	if got[len(got)-1] != lines[len(lines)-1] {
		t.Errorf("preview ends at %q, want the newest line %q", got[len(got)-1], lines[len(lines)-1])
	}
	// The oldest line is the one scrolling back is *for*.
	if !strings.Contains(preview, "build step a") {
		t.Errorf("preview %q dropped the oldest line; there would be nothing to scroll up to", preview)
	}

	stub.release <- nil
}

// The retention is still a bound — every line rides in a full-state broadcast, so
// it cannot grow without limit. Exercised with more lines than the bound, because a
// fixture that never reaches it proves nothing.
func TestStartupOutputIsBoundedToTheRetainedScrollback(t *testing.T) {
	overflow := startupTailLines + 50
	lines := make([]string, 0, overflow)
	for i := 0; i < overflow; i++ {
		lines = append(lines, fmt.Sprintf("build step %d", i))
	}
	stub := newStartingStub(lines...)
	svc, st := serviceWithStartingStub(t, stub)

	preview := awaitStartupPreview(t, stub, st, svc)

	got := strings.Split(preview, "\n")
	if len(got) != startupTailLines {
		t.Errorf("preview has %d lines, want it capped at %d", len(got), startupTailLines)
	}
	if got[len(got)-1] != lines[len(lines)-1] {
		t.Errorf("preview ends at %q, want the newest line %q", got[len(got)-1], lines[len(lines)-1])
	}
	if strings.Contains(preview, "build step 0\n") {
		t.Errorf("preview still carries the oldest line; the scrollback is not bounded")
	}

	stub.release <- nil
}

// On success the placeholder becomes the real session — one row throughout, so
// nothing flickers and the count never lies.
func TestSuccessfulCreatePromotesThePlaceholder(t *testing.T) {
	stub := newStartingStub("building")
	svc, st := serviceWithStartingStub(t, stub)

	placeholder, err := svc.CreateSession(context.Background(), CreateSessionInput{
		ProjectID: "p1", WorkstreamID: "ws-1", ProviderID: "slow", OptionID: "claude",
	})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	<-stub.entered
	stub.release <- nil

	waitFor(t, "the real session to replace the placeholder", func() bool {
		sessions := sessionsOf(t, st)
		return len(sessions) == 1 && sessions[0].ID == "sess-real"
	})

	sessions := sessionsOf(t, st)
	if len(sessions) != 1 {
		t.Fatalf("sessions = %d, want exactly the real one", len(sessions))
	}
	if IsStartingPlaceholder(sessions[0]) || sessions[0].Placeholder {
		t.Error("the placeholder outlived the create")
	}
	if sessions[0].Provider != "slow" {
		t.Errorf("provider = %q, want it stamped with the owner", sessions[0].Provider)
	}
	if sessions[0].PlaceholderID != placeholder.ID {
		t.Errorf("placeholderId = %q, want the placeholder's %q", sessions[0].PlaceholderID, placeholder.ID)
	}
}

// A failed create keeps its row and explains itself there: the session the user
// was watching becomes the record of why it never started. It must not stay at
// "starting" (non-terminal, so it would hold a concurrency slot and block the
// workstream's removal forever) — `error` is terminal, which is what makes
// keeping the row affordable.
func TestFailedCreateLeavesTheSessionInError(t *testing.T) {
	stub := newStartingStub("building")
	svc, st := serviceWithStartingStub(t, stub)

	if _, err := svc.CreateSession(context.Background(), CreateSessionInput{
		ProjectID: "p1", WorkstreamID: "ws-1", ProviderID: "slow", OptionID: "claude",
	}); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	<-stub.entered
	stub.release <- errFakeBuild

	waitFor(t, "the placeholder to go to error", func() bool {
		sessions := sessionsOf(t, st)
		return len(sessions) == 1 && sessions[0].State == domain.SessionError
	})

	sessions := sessionsOf(t, st)
	failed := sessions[0]
	if !IsStartingPlaceholder(failed) {
		t.Errorf("session ID = %q, want the placeholder kept its ID", failed.ID)
	}
	if !strings.Contains(failed.Error, "image build failed") {
		t.Errorf("session error = %q, want the provider's failure", failed.Error)
	}
	if !failed.State.Terminal() {
		t.Error("a failed session must be terminal: it holds no slot and blocks no removal")
	}
	if failed.State.Busy() {
		t.Error("a failed session must not be busy, or its workstream cannot be removed")
	}
	// The failure belongs to the session, so nothing is written to the workstream:
	// saying it in both places would say it twice, and the workstream's own copy
	// would outlive the row the user removes.
	ws, _ := st.Workstream("p1", "ws-1")
	if ws.StatusMessage != "" {
		t.Errorf("workstream status message = %q, want the error only on the session", ws.StatusMessage)
	}
}

// The × on the failed row. No provider ever heard of the placeholder, so the
// removal cannot be routed to one — dropping the row is the whole disposal.
func TestRemoveSessionDropsAFailedPlaceholder(t *testing.T) {
	stub := newStartingStub("building")
	svc, st := serviceWithStartingStub(t, stub)

	if _, err := svc.CreateSession(context.Background(), CreateSessionInput{
		ProjectID: "p1", WorkstreamID: "ws-1", ProviderID: "slow", OptionID: "claude",
	}); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	<-stub.entered
	stub.release <- errFakeBuild

	waitFor(t, "the placeholder to go to error", func() bool {
		sessions := sessionsOf(t, st)
		return len(sessions) == 1 && sessions[0].State == domain.SessionError
	})

	failed := sessionsOf(t, st)[0]
	err := svc.RemoveSession(context.Background(), SessionRef{
		ProjectID: "p1", WorkstreamID: "ws-1", SessionID: failed.ID,
	})
	if err != nil {
		t.Fatalf("RemoveSession() error = %v", err)
	}
	if got := sessionsOf(t, st); len(got) != 0 {
		t.Errorf("sessions = %d, want the failed row gone for good", len(got))
	}
}

var errFakeBuild = errStartupTest("image build failed")

type errStartupTest string

func (e errStartupTest) Error() string { return string(e) }

// The placeholder stands for a creation the in-flight counter already reserved,
// so counting it again would charge one create twice.
func TestActiveSessionsSkipsStartingPlaceholders(t *testing.T) {
	ws := domain.Workstream{Sessions: []domain.Session{
		{ID: "sess-1", State: domain.SessionRunning},
		{ID: StartingIDPrefix + "abc", State: domain.SessionStarting},
		{ID: "sess-2", State: domain.SessionStopped},
	}}
	if got := activeSessions(ws); got != 1 {
		t.Errorf("activeSessions() = %d, want 1 (the running one only)", got)
	}
}

// vanishingStub is a session provider whose availability check removes the
// workstream, so the workstream is gone by the time the placeholder is added.
type vanishingStub struct {
	*startingStub
	store *store.Store
}

func (s vanishingStub) Available(context.Context) (bool, error) {
	return true, s.store.RemoveWorkstream("p1", "ws-1")
}

// A placeholder that never made it into the store must not be handed back as
// the session that is starting.
func TestCreateSessionInAWorkstreamRemovedMeanwhile(t *testing.T) {
	stub := newStartingStub()
	service, testStore := serviceWithStartingStub(t, stub)
	vanishing := vanishingStub{startingStub: stub, store: testStore}
	service.registry = NewRegistry()
	if err := service.registry.RegisterSessionProvider(vanishing); err != nil {
		t.Fatal(err)
	}

	_, err := service.CreateSession(context.Background(), CreateSessionInput{
		ProjectID: "p1", WorkstreamID: "ws-1", ProviderID: "slow", OptionID: "claude",
	})
	if !errors.Is(err, ErrWorkstreamNotFound) {
		t.Fatalf("CreateSession() error = %v, want ErrWorkstreamNotFound", err)
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if len(service.inflight) != 0 || len(service.creating) != 0 {
		t.Errorf("inflight = %v, creating = %d; want the reservation released and nothing started", service.inflight, len(service.creating))
	}
}
