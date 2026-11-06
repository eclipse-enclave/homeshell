package provider

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/store"
)

// cancellableStub is a session provider whose CreateSession honours the context
// it is given, which is what a real one does: `enclave run` is an exec.Cmd, so
// cancelling the context kills the build. It records whether it was cancelled
// and whether the session it produced was later torn down.
type cancellableStub struct {
	stubSessionProvider
	entered chan struct{}
	// release lets a test finish the create *despite* the cancellation, which is
	// the race that leaks a container: the provider had already brought the
	// session up by the time the cancel arrived. It only has that effect with
	// ignoreCancel set — otherwise the cancel wins, as it does for a provider
	// whose work is a killable subprocess.
	release      chan struct{}
	ignoreCancel bool
	session      domain.Session

	mu        sync.Mutex
	cancelled bool
	removed   []string
	// discoveredCancelled counts discoveries asked for on a cancelled context,
	// which a real provider answers with nothing.
	discoveredCancelled int
}

func newCancellableStub() *cancellableStub {
	return &cancellableStub{
		stubSessionProvider: stubSessionProvider{
			id: "slow", label: "Slow", available: true,
			sessionSchemas: []SessionSchemaOption{{ID: "claude", Label: "Claude"}},
		},
		entered: make(chan struct{}),
		release: make(chan struct{}),
		session: domain.Session{ID: "hs-real", Title: "Real", State: domain.SessionRunning},
	}
}

func (s *cancellableStub) CreateSession(ctx context.Context, _ CreateSessionRequest) (domain.Session, error) {
	close(s.entered)
	if s.ignoreCancel {
		// Models the losing side of the race: the container was already up when the
		// cancel arrived, so the create succeeds regardless of it.
		<-s.release
		return s.session, nil
	}
	select {
	case <-ctx.Done():
		s.mu.Lock()
		s.cancelled = true
		s.mu.Unlock()
		return domain.Session{}, ctx.Err()
	case <-s.release:
		return s.session, nil
	}
}

func (s *cancellableStub) RemoveSession(_ context.Context, ref SessionRef) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removed = append(s.removed, ref.SessionID)
	return nil
}

func (s *cancellableStub) DiscoverSessions(ctx context.Context, project ProjectRef) ([]DiscoveredSession, error) {
	if ctx.Err() != nil {
		s.mu.Lock()
		s.discoveredCancelled++
		s.mu.Unlock()
	}
	return s.stubSessionProvider.DiscoverSessions(ctx, project)
}

func (s *cancellableStub) wasCancelled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cancelled
}

func (s *cancellableStub) removedSessions() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.removed...)
}

// serviceWithCancellableStub wires a service over one project/workstream owned by
// a workstream provider that can be removed.
func serviceWithCancellableStub(t *testing.T, stub *cancellableStub) (*Service, *store.Store) {
	t.Helper()
	workingDir := t.TempDir()
	st := store.NewTestStore(t, []domain.Project{{
		ID: "p1", Name: "p1", Path: t.TempDir(),
		Workstreams: []domain.Workstream{{
			ID: "ws-1", Name: "ws", Provider: "core",
			StrategyID: "in-place", WorkingDir: workingDir,
		}},
	}})
	reg := NewRegistry()
	if err := reg.RegisterSessionProvider(stub); err != nil {
		t.Fatalf("registering the session stub: %v", err)
	}
	if err := reg.RegisterWorkstreamProvider(&stubWorkstreamProvider{
		id: "core", label: "Core", available: true,
	}); err != nil {
		t.Fatalf("registering the workstream stub: %v", err)
	}
	return NewService(st, reg, 0), st
}

// startCreate kicks off a create and waits until the provider is inside it, so
// the test acts during the in-flight window rather than racing it.
func startCreate(t *testing.T, svc *Service, stub *cancellableStub) {
	t.Helper()
	if _, err := svc.CreateSession(context.Background(), CreateSessionInput{
		ProjectID: "p1", WorkstreamID: "ws-1", ProviderID: "slow", OptionID: "claude",
	}); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	<-stub.entered
}

// Force-removing a workstream promises that its busy sessions are "stopped and
// torn down with it". A build in flight is the case that promise is hardest to
// keep and matters most: the create runs on a detached context, so without an
// explicit cancel it runs to completion after the workstream is gone and starts
// a container nothing owns.
func TestForceRemoveCancelsAnInFlightCreate(t *testing.T) {
	stub := newCancellableStub()
	svc, _ := serviceWithCancellableStub(t, stub)
	startCreate(t, svc, stub)

	if err := svc.Remove(context.Background(), WorkstreamRef{ProjectID: "p1", WorkstreamID: "ws-1"},
		RemoveOptions{Force: true}); err != nil {
		t.Fatalf("Remove(force) error = %v", err)
	}

	waitFor(t, "the in-flight create to be cancelled", stub.wasCancelled)
}

// Removing a project cancels the creates in flight in it, which would land in a
// project that is gone.
func TestRemoveProjectCancelsAnInFlightCreate(t *testing.T) {
	stub := newCancellableStub()
	svc, _ := serviceWithCancellableStub(t, stub)
	startCreate(t, svc, stub)

	if err := svc.RemoveProject(context.Background(), "p1"); err != nil {
		t.Fatalf("RemoveProject() error = %v", err)
	}

	waitFor(t, "the in-flight create to be cancelled", stub.wasCancelled)
}

// Shutting the daemon down cancels a create in flight, and waits for it: left
// running, it would build on out of the daemon's sight.
func TestShutdownCancelsAnInFlightCreate(t *testing.T) {
	stub := newCancellableStub()
	svc, _ := serviceWithCancellableStub(t, stub)
	startCreate(t, svc, stub)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	svc.Shutdown(ctx)

	if ctx.Err() != nil || !stub.wasCancelled() {
		t.Error("Shutdown did not cancel the create in flight")
	}
}

// A create that lands as the daemon shuts down does not reconcile its project on
// the context the shutdown cancelled: the listing would come back empty and drop
// the project's other sessions, with their titles and launch configs.
func TestACreateLandingAsTheDaemonShutsDownDoesNotReconcileOnACancelledContext(t *testing.T) {
	stub := newCancellableStub()
	stub.ignoreCancel = true
	svc, _ := serviceWithCancellableStub(t, stub)
	startCreate(t, svc, stub)

	svc.cancelLifetime()
	close(stub.release)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	svc.Shutdown(ctx)

	stub.mu.Lock()
	defer stub.mu.Unlock()
	if stub.discoveredCancelled != 0 {
		t.Errorf("the landed create reconciled on a cancelled context %d time(s)", stub.discoveredCancelled)
	}
}

// A create a request starts once the daemon is shutting down runs cancelled, and
// untracked rather than joining a wait already under way.
func TestACreateStartedAfterShutdownRunsCancelled(t *testing.T) {
	stub := newCancellableStub()
	svc, _ := serviceWithCancellableStub(t, stub)
	svc.Shutdown(context.Background())

	startCreate(t, svc, stub)

	waitFor(t, "the create to be cancelled", stub.wasCancelled)
}

// Without force, a workstream whose only busy row is a starting placeholder is
// refused — and the create must be left running, since nothing was torn down.
func TestRemoveWithoutForceLeavesAnInFlightCreateAlone(t *testing.T) {
	stub := newCancellableStub()
	svc, _ := serviceWithCancellableStub(t, stub)
	startCreate(t, svc, stub)

	err := svc.Remove(context.Background(), WorkstreamRef{ProjectID: "p1", WorkstreamID: "ws-1"}, RemoveOptions{})
	var busy *BusyError
	if !errors.As(err, &busy) {
		t.Fatalf("Remove() error = %v, want a BusyError naming the starting session", err)
	}
	if stub.wasCancelled() {
		t.Error("a refused removal cancelled the build it refused to interrupt")
	}
}

// Dismissing the placeholder row is the user saying they no longer want what it
// is building, so the build stops with it — otherwise it lands a session on a row
// that is gone.
func TestRemovingAStartingPlaceholderCancelsItsCreate(t *testing.T) {
	stub := newCancellableStub()
	svc, st := serviceWithCancellableStub(t, stub)
	startCreate(t, svc, stub)

	ws, err := st.Workstream("p1", "ws-1")
	if err != nil {
		t.Fatalf("reading the workstream: %v", err)
	}
	if len(ws.Sessions) != 1 {
		t.Fatalf("sessions = %d, want the placeholder", len(ws.Sessions))
	}
	if err := svc.RemoveSession(context.Background(), SessionRef{
		ProjectID: "p1", WorkstreamID: "ws-1", SessionID: ws.Sessions[0].ID,
	}); err != nil {
		t.Fatalf("RemoveSession() error = %v", err)
	}

	waitFor(t, "the dismissed create to be cancelled", stub.wasCancelled)
}

// The provider may win the race: the container is up by the time the cancel
// arrives, and its workstream is already gone. The session then belongs to
// nothing — no store record means no way to route a removal to it ever again —
// so the create disposes of it rather than leaving an orphan for the next
// discovery pass to adopt.
func TestACreateThatLandsAfterItsWorkstreamIsGoneTearsTheSessionDown(t *testing.T) {
	stub := newCancellableStub()
	stub.ignoreCancel = true
	svc, _ := serviceWithCancellableStub(t, stub)
	startCreate(t, svc, stub)

	if err := svc.Remove(context.Background(), WorkstreamRef{ProjectID: "p1", WorkstreamID: "ws-1"},
		RemoveOptions{Force: true}); err != nil {
		t.Fatalf("Remove(force) error = %v", err)
	}
	// The build finishes anyway, having got past the point of no return before the
	// cancel landed.
	close(stub.release)

	waitFor(t, "the orphaned session to be torn down", func() bool {
		for _, id := range stub.removedSessions() {
			if id == stub.session.ID {
				return true
			}
		}
		return false
	})
}

// Tearing down a workstream must not ask a provider to remove a placeholder: the
// ID is the daemon's own invention, so the call can only fail on an ID that was
// never handed out — noise in the log that hides the removals that matter.
func TestWorkstreamTeardownDoesNotAskAProviderToRemoveAPlaceholder(t *testing.T) {
	stub := newCancellableStub()
	svc, _ := serviceWithCancellableStub(t, stub)
	startCreate(t, svc, stub)

	if err := svc.Remove(context.Background(), WorkstreamRef{ProjectID: "p1", WorkstreamID: "ws-1"},
		RemoveOptions{Force: true}); err != nil {
		t.Fatalf("Remove(force) error = %v", err)
	}
	for _, id := range stub.removedSessions() {
		if IsStartingPlaceholder(domain.Session{ID: id}) {
			t.Errorf("the provider was asked to remove placeholder %q", id)
		}
	}
}

// A failed workstream's status message is its whole account of why the card is
// red, which is why the dismiss endpoint answers 409 rather than clearing it.
// Creating a session must not do quietly what that endpoint refuses to do
// openly: RuntimeError is a separate sticky field, so clearing the message
// leaves a permanently red card with nothing left saying why.
func TestCreateSessionKeepsAFailedWorkstreamsExplanation(t *testing.T) {
	stub := newCancellableStub()
	svc, st := serviceWithCancellableStub(t, stub)

	failed := domain.Workstream{
		ID: "ws-1", Name: "ws", Provider: "core", StrategyID: "in-place",
		WorkingDir:   t.TempDir(),
		RuntimeState: domain.RuntimeError, StatusMessage: "the provider exploded",
		Sessions: []domain.Session{},
	}
	if err := st.UpdateWorkstream("p1", failed); err != nil {
		t.Fatalf("staging the failed workstream: %v", err)
	}

	if _, err := svc.CreateSession(context.Background(), CreateSessionInput{
		ProjectID: "p1", WorkstreamID: "ws-1", ProviderID: "slow", OptionID: "claude",
	}); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	<-stub.entered

	ws, err := st.Workstream("p1", "ws-1")
	if err != nil {
		t.Fatalf("reading the workstream: %v", err)
	}
	if ws.StatusMessage != "the provider exploded" {
		t.Errorf("status message = %q, want the failure explanation left intact", ws.StatusMessage)
	}
}

// The other half of the same rule: an ordinary workstream's stale message *is*
// cleared, so a retry after a failed session does not wear the last attempt's
// error for the whole of this one.
func TestCreateSessionClearsAnOrdinaryStaleMessage(t *testing.T) {
	stub := newCancellableStub()
	svc, st := serviceWithCancellableStub(t, stub)

	if err := st.SetStatusMessage("p1", "ws-1", "a previous session failed"); err != nil {
		t.Fatalf("staging the stale message: %v", err)
	}
	if _, err := svc.CreateSession(context.Background(), CreateSessionInput{
		ProjectID: "p1", WorkstreamID: "ws-1", ProviderID: "slow", OptionID: "claude",
	}); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	<-stub.entered

	ws, err := st.Workstream("p1", "ws-1")
	if err != nil {
		t.Fatalf("reading the workstream: %v", err)
	}
	if ws.StatusMessage != "" {
		t.Errorf("status message = %q, want a retry to start clean", ws.StatusMessage)
	}
}
