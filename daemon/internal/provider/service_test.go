package provider_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/provider"
	"github.com/eclipsesource/homeshell/daemon/internal/provider/fake"
	"github.com/eclipsesource/homeshell/daemon/internal/store"
)

// registerFake registers the fake in both roles (workstream + session), as the
// demo does. The core workstream provider is registered by NewService.
func registerFake(t *testing.T, reg *provider.Registry, cfg fake.Config) {
	t.Helper()
	fp := fake.New(cfg)
	if err := reg.RegisterWorkstreamProvider(fp); err != nil {
		t.Fatalf("register fake workstream provider: %v", err)
	}
	if err := reg.RegisterSessionProvider(fp); err != nil {
		t.Fatalf("register fake session provider: %v", err)
	}
}

// newService wires a service over one empty project. The project path is a real
// temp dir so the core workstream provider's in-place strategy succeeds.
func newService(t *testing.T) (*provider.Service, *store.Store) {
	return newServiceCap(t, 0)
}

func newServiceCap(t *testing.T, cap int) (*provider.Service, *store.Store) {
	t.Helper()
	st := store.NewTestStore(t, []domain.Project{{ID: "p1", Name: "p1", Path: t.TempDir(), Workstreams: []domain.Workstream{}}})
	reg := provider.NewRegistry()
	registerFake(t, reg, fake.Config{MaxConcurrentSessions: cap})
	svc := provider.NewService(st, reg, 0)
	svc.Reconcile(context.Background())
	return svc, st
}

// createWS creates a workstream via the fake and waits for the real (non
// placeholder) workstream to land in the store, returning a ref to it.
func createWS(t *testing.T, svc *provider.Service, st *store.Store) provider.WorkstreamRef {
	t.Helper()
	if _, err := svc.Create(context.Background(), provider.CreateInput{
		ProjectID: "p1", ProviderID: "fake", Config: []byte(`{"branch":"main"}`),
	}); err != nil {
		t.Fatalf("Create workstream: %v", err)
	}
	ws := awaitWorkstream(t, st, "p1")
	return provider.WorkstreamRef{ProjectID: "p1", WorkstreamID: ws.ID}
}

func TestServiceCreateReturnsPlaceholderThenReal(t *testing.T) {
	svc, st := newService(t)
	ws, err := svc.Create(context.Background(), provider.CreateInput{
		ProjectID: "p1", ProviderID: "fake", Name: "feat", Config: []byte(`{"branch":"main"}`),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Creation is async: the immediate return is a creating-state placeholder.
	if ws.Provider != "fake" || ws.RuntimeState != domain.RuntimeCreating || ws.Name != "feat" {
		t.Errorf("placeholder = %+v; want provider=fake creating name=feat", ws)
	}
	// Named from the start, not only once a projection has run.
	if ws.ProviderLabel != "Fake Provider" {
		t.Errorf("placeholder provider label = %q; want %q", ws.ProviderLabel, "Fake Provider")
	}
	// The real workstream lands shortly, carrying provider-derived fields.
	real := awaitWorkstream(t, st, "p1")
	if real.Provider != "fake" || real.Branch != "main" || real.Name != "feat" {
		t.Errorf("real workstream = %+v; want provider=fake branch=main name=feat", real)
	}
	if real.WorkingDir == "" {
		t.Error("real workstream has no WorkingDir set")
	}
}

func TestServiceCreateSession(t *testing.T) {
	svc, st := newService(t)
	wref := createWS(t, svc, st)

	if _, err := svc.CreateSession(context.Background(), provider.CreateSessionInput{
		ProjectID: "p1", WorkstreamID: wref.WorkstreamID, ProviderID: "fake", OptionID: "claude",
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	awaitSession(t, st, "p1", wref.WorkstreamID, "Claude Code")
}

func TestServiceCreateSessionUnknownWorkstream(t *testing.T) {
	svc, _ := newService(t)
	_, err := svc.CreateSession(context.Background(), provider.CreateSessionInput{
		ProjectID: "p1", WorkstreamID: "ghost", ProviderID: "fake",
	})
	if !errors.Is(err, provider.ErrWorkstreamNotFound) {
		t.Errorf("CreateSession(unknown) err = %v, want ErrWorkstreamNotFound", err)
	}
}

func TestServiceCreateFailureSurfacesAsErrorPlaceholder(t *testing.T) {
	// Creation is async, so a provider failure surfaces as the placeholder
	// transitioning to error state, not as a synchronous error.
	st := store.NewTestStore(t, []domain.Project{{ID: "p1", Name: "p1", Path: t.TempDir(), Workstreams: []domain.Workstream{}}})
	reg := provider.NewRegistry()
	registerFake(t, reg, fake.Config{CreateErr: errors.New("container build failed")})
	svc := provider.NewService(st, reg, 0)
	svc.Reconcile(context.Background())

	if _, err := svc.Create(context.Background(), provider.CreateInput{
		ProjectID: "p1", ProviderID: "fake", Name: "doomed", Config: []byte(`{"branch":"main"}`),
	}); err != nil {
		t.Fatalf("Create returned a synchronous error: %v", err)
	}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for error placeholder")
		default:
		}
		time.Sleep(10 * time.Millisecond)
		for _, w := range st.Projects()[0].Workstreams {
			if w.RuntimeState == domain.RuntimeError && w.StatusMessage == "container build failed" {
				// Marked as a failed create, so a client offers nothing on it but
				// its removal.
				if !w.CreateFailed {
					t.Error("CreateFailed = false on a failed create's placeholder")
				}
				// Still named: a projection carries the placeholder over as it is.
				if w.ProviderLabel != "Fake Provider" {
					t.Errorf("provider label = %q; want %q", w.ProviderLabel, "Fake Provider")
				}
				return
			}
		}
	}
}

func TestServiceCreateErrors(t *testing.T) {
	svc, _ := newService(t)
	tests := []struct {
		name string
		in   provider.CreateInput
		want error
	}{
		{"unknown project", provider.CreateInput{ProjectID: "nope", ProviderID: "fake", Config: []byte(`{"branch":"m"}`)}, provider.ErrProjectNotFound},
		{"unknown provider", provider.CreateInput{ProjectID: "p1", ProviderID: "ghost", Config: []byte(`{"branch":"m"}`)}, provider.ErrProviderNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := svc.Create(context.Background(), tc.in); !errors.Is(err, tc.want) {
				t.Errorf("Create err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestServiceCreateValidationError(t *testing.T) {
	svc, _ := newService(t)
	_, err := svc.Create(context.Background(), provider.CreateInput{
		ProjectID: "p1", ProviderID: "fake", Config: []byte(`{}`),
	})
	if provider.FieldErrors(err) == nil {
		t.Errorf("Create err = %v; want a field-level ValidationError", err)
	}
}

func TestServiceRenameRemove(t *testing.T) {
	svc, st := newService(t)
	ref := createWS(t, svc, st)

	if err := svc.Rename(context.Background(), ref, "renamed"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	ws, err := st.Workstream("p1", ref.WorkstreamID)
	if err != nil {
		t.Fatalf("Workstream after rename: %v", err)
	}
	if ws.Name != "renamed" {
		t.Errorf("after rename: name=%q; want renamed", ws.Name)
	}

	if err := svc.Remove(context.Background(), ref, provider.RemoveOptions{}); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if n := len(st.Projects()[0].Workstreams); n != 0 {
		t.Errorf("workstreams after remove = %d, want 0", n)
	}
}

func TestServiceRenameUnknownWorkstream(t *testing.T) {
	svc, _ := newService(t)
	err := svc.Rename(context.Background(), provider.WorkstreamRef{ProjectID: "p1", WorkstreamID: "ghost"}, "x")
	if !errors.Is(err, provider.ErrWorkstreamNotFound) {
		t.Errorf("Rename(missing) err = %v, want ErrWorkstreamNotFound", err)
	}
}

func TestServiceCreateSessionReflectsInStore(t *testing.T) {
	svc, st := newService(t)
	wref := createWS(t, svc, st)
	svc.CreateSession(context.Background(), provider.CreateSessionInput{
		ProjectID: wref.ProjectID, WorkstreamID: wref.WorkstreamID, ProviderID: "fake", OptionID: "claude", Config: []byte(`{"model":"opus"}`),
	})
	sess := awaitSession(t, st, "p1", wref.WorkstreamID, "Claude Code")
	if sess.ID == "" {
		t.Error("session not found in store")
	}
	if sess.Provider != "fake" {
		t.Errorf("session provider = %q, want fake", sess.Provider)
	}
}

func TestServiceCreateSessionValidationError(t *testing.T) {
	svc, st := newService(t)
	wref := createWS(t, svc, st)
	_, err := svc.CreateSession(context.Background(), provider.CreateSessionInput{
		ProjectID: wref.ProjectID, WorkstreamID: wref.WorkstreamID, ProviderID: "fake", OptionID: "claude", Config: []byte(`{"model":"nope"}`),
	})
	if provider.FieldErrors(err) == nil {
		t.Errorf("CreateSession err = %v; want a field-level ValidationError", err)
	}
}

func TestServiceCreateSessionConcurrencyLimit(t *testing.T) {
	svc, st := newServiceCap(t, 1)
	wref := createWS(t, svc, st)
	if _, err := svc.CreateSession(context.Background(), provider.CreateSessionInput{
		ProjectID: wref.ProjectID, WorkstreamID: wref.WorkstreamID, ProviderID: "fake", OptionID: "claude",
	}); err != nil {
		t.Fatalf("first CreateSession: %v", err)
	}
	awaitSession(t, st, "p1", wref.WorkstreamID, "Claude Code")
	_, err := svc.CreateSession(context.Background(), provider.CreateSessionInput{
		ProjectID: wref.ProjectID, WorkstreamID: wref.WorkstreamID, ProviderID: "fake", OptionID: "claude",
	})
	if !errors.Is(err, provider.ErrConcurrencyLimit) {
		t.Fatalf("second CreateSession err = %v, want ErrConcurrencyLimit", err)
	}
}

func TestServiceStopStartAndConcurrencyOnRestart(t *testing.T) {
	svc, st := newServiceCap(t, 1)
	wref := createWS(t, svc, st)
	svc.CreateSession(context.Background(), provider.CreateSessionInput{
		ProjectID: wref.ProjectID, WorkstreamID: wref.WorkstreamID, ProviderID: "fake", OptionID: "claude",
	})
	realSess := awaitSession(t, st, "p1", wref.WorkstreamID, "Claude Code")
	sref := provider.SessionRef{ProjectID: wref.ProjectID, WorkstreamID: wref.WorkstreamID, SessionID: realSess.ID}

	if err := svc.StopSession(context.Background(), sref); err != nil {
		t.Fatalf("StopSession: %v", err)
	}
	if got := st.Projects()[0].Workstreams[0].Sessions[0].State; got != domain.SessionStopped {
		t.Errorf("after stop = %q, want stopped", got)
	}
	if _, err := svc.CreateSession(context.Background(), provider.CreateSessionInput{
		ProjectID: wref.ProjectID, WorkstreamID: wref.WorkstreamID, ProviderID: "fake", OptionID: "claude",
	}); err != nil {
		t.Fatalf("create after stop should fit under the cap: %v", err)
	}
	awaitSessionCount(t, st, "p1", wref.WorkstreamID, "Claude Code", 2)
	if err := svc.StartSession(context.Background(), sref); !errors.Is(err, provider.ErrConcurrencyLimit) {
		t.Fatalf("restart over cap err = %v, want ErrConcurrencyLimit", err)
	}
}

func TestServiceRemoveSession(t *testing.T) {
	svc, st := newService(t)
	wref := createWS(t, svc, st)
	svc.CreateSession(context.Background(), provider.CreateSessionInput{
		ProjectID: wref.ProjectID, WorkstreamID: wref.WorkstreamID, ProviderID: "fake", OptionID: "claude",
	})
	realSess := awaitSession(t, st, "p1", wref.WorkstreamID, "Claude Code")
	sref := provider.SessionRef{ProjectID: wref.ProjectID, WorkstreamID: wref.WorkstreamID, SessionID: realSess.ID}

	// A running (busy) session can still be removed — the provider tears it
	// down regardless of state; the daemon does not gate on busyness.
	if err := svc.StartSession(context.Background(), sref); err != nil {
		t.Fatal(err)
	}
	if err := svc.RemoveSession(context.Background(), sref); err != nil {
		t.Fatalf("RemoveSession of a running session: %v", err)
	}
	if n := len(st.Projects()[0].Workstreams[0].Sessions); n != 0 {
		t.Errorf("sessions after remove = %d, want 0", n)
	}
}

func TestServiceSessionSchemasFor(t *testing.T) {
	svc, st := newService(t)
	wref := createWS(t, svc, st)
	schemas, err := svc.SessionSchemasFor(context.Background(), wref)
	if err != nil {
		t.Fatalf("SessionSchemasFor: %v", err)
	}
	if len(schemas) != 1 || schemas[0].ProviderID != "fake" {
		t.Errorf("schemas = %+v; want one entry for fake", schemas)
	}
	if len(schemas[0].Options) != len(fake.SessionOptionIDs()) {
		t.Errorf("options = %+v; want one per agent the fake provider offers", schemas[0].Options)
	}
	if _, err := svc.SessionSchemasFor(context.Background(), provider.WorkstreamRef{ProjectID: "p1", WorkstreamID: "ghost"}); !errors.Is(err, provider.ErrWorkstreamNotFound) {
		t.Errorf("SessionSchemasFor(ghost) err = %v, want ErrWorkstreamNotFound", err)
	}
}

func TestServiceWorkstreamProvidersFor(t *testing.T) {
	svc, _ := newService(t)
	providers, err := svc.WorkstreamProvidersFor(context.Background(), "p1")
	if err != nil {
		t.Fatalf("WorkstreamProvidersFor: %v", err)
	}
	byID := map[string]provider.ProviderDescriptor{}
	for _, p := range providers {
		byID[p.ID] = p
	}
	core, ok := byID[provider.CoreProviderID]
	if !ok || core.Label != "Default" || len(core.WorkstreamSchema) == 0 {
		t.Errorf("core default provider missing or malformed: %+v", core)
	}
	fk, ok := byID["fake"]
	if !ok || len(fk.WorkstreamSchema) == 0 {
		t.Errorf("fake workstream provider missing or malformed: %+v", fk)
	}
	if _, err := svc.WorkstreamProvidersFor(context.Background(), "nope"); !errors.Is(err, provider.ErrProjectNotFound) {
		t.Errorf("WorkstreamProvidersFor(nope) err = %v, want ErrProjectNotFound", err)
	}
}

func TestServiceCoreCreateReturnsPlaceholder(t *testing.T) {
	// The core workstream provider handles creation when chosen explicitly.
	projectPath := t.TempDir()
	st := store.NewTestStore(t, []domain.Project{{ID: "p1", Name: "p1", Path: projectPath, Workstreams: []domain.Workstream{}}})
	reg := provider.NewRegistry()
	svc := provider.NewService(st, reg, 0)
	svc.Reconcile(context.Background())

	ws, err := svc.Create(context.Background(), provider.CreateInput{
		ProjectID: "p1", ProviderID: provider.CoreProviderID, Name: "default-ws",
		Config: []byte(`{"strategy":"in-place"}`),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if ws.RuntimeState != domain.RuntimeCreating {
		t.Errorf("placeholder runtime state = %q, want creating", ws.RuntimeState)
	}
	if ws.Name != "default-ws" {
		t.Errorf("name = %q, want default-ws", ws.Name)
	}
	// The real in-place workstream lands with WorkingDir == the project path.
	real := awaitWorkstream(t, st, "p1")
	if real.Provider != provider.CoreProviderID || real.WorkingDir != projectPath {
		t.Errorf("real workstream = %+v; want core provider and WorkingDir=%q", real, projectPath)
	}
}

func TestServiceCoreCreateValidationError(t *testing.T) {
	projectPath := t.TempDir()
	st := store.NewTestStore(t, []domain.Project{{ID: "p1", Name: "p1", Path: projectPath, Workstreams: []domain.Workstream{}}})
	reg := provider.NewRegistry()
	svc := provider.NewService(st, reg, 0)
	svc.Reconcile(context.Background())

	_, err := svc.Create(context.Background(), provider.CreateInput{
		ProjectID: "p1", ProviderID: provider.CoreProviderID,
		Config: []byte(`{"strategy":"teleport"}`),
	})
	if err == nil {
		t.Fatal("unknown strategy should fail validation")
	}
}

// Two in-place workstreams on one project would share WorkingDir (the project
// path). The second is refused on the request, on the strategy, naming the
// workstream already there, and nothing is reserved for it.
func TestServiceCreateRefusesASecondInPlaceWorkstream(t *testing.T) {
	projectPath := t.TempDir()
	st := store.NewTestStore(t, []domain.Project{{ID: "p1", Name: "p1", Path: projectPath, Workstreams: []domain.Workstream{}}})
	svc := provider.NewService(st, provider.NewRegistry(), 0)
	svc.Reconcile(context.Background())

	createInPlace := func(name string) error {
		_, err := svc.Create(context.Background(), provider.CreateInput{
			ProjectID: "p1", ProviderID: provider.CoreProviderID, Name: name,
			Config: []byte(`{"strategy":"in-place"}`),
		})
		return err
	}
	if err := createInPlace("first"); err != nil {
		t.Fatalf("Create first: %v", err)
	}
	if real := awaitWorkstream(t, st, "p1"); real.WorkingDir != projectPath {
		t.Fatalf("first WorkingDir = %q, want %q", real.WorkingDir, projectPath)
	}

	err := createInPlace("second")
	want := `"first" already works in this folder; choose copy`
	if got := provider.FieldErrors(err)["/strategy"]; got != want {
		t.Errorf("/strategy refusal = %q (err %v), want %q", got, err, want)
	}
	if got := len(st.Projects()[0].Workstreams); got != 1 {
		t.Errorf("workstreams = %d, want only the first", got)
	}
}

// coreBlankService wires a service over one project in a folder called "proj",
// and returns a create of a blank-named copy into a fresh folder each time.
func coreBlankService(t *testing.T, existing ...domain.Workstream) (*store.Store, func() (domain.Workstream, error)) {
	t.Helper()
	projectPath := filepath.Join(t.TempDir(), "proj")
	if err := os.Mkdir(projectPath, 0o755); err != nil {
		t.Fatal(err)
	}
	st := store.NewTestStore(t, []domain.Project{{ID: "p1", Name: "proj", Path: projectPath, Workstreams: existing}})
	svc := provider.NewService(st, provider.NewRegistry(), 0)
	destinations := t.TempDir()
	var mu sync.Mutex
	count := 0
	createBlank := func() (domain.Workstream, error) {
		mu.Lock()
		count++
		destination := filepath.Join(destinations, fmt.Sprint(count))
		mu.Unlock()
		return svc.Create(context.Background(), provider.CreateInput{
			ProjectID: "p1", ProviderID: provider.CoreProviderID,
			Config: []byte(`{"strategy":"copy","path":"` + destination + `"}`),
		})
	}
	return st, createBlank
}

// A blank name is resolved before the placeholder goes in, so a second blank
// create while the first is still in flight is numbered past it rather than
// refused as a duplicate of a shared placeholder name.
func TestServiceCreateNumbersBackToBackBlankNames(t *testing.T) {
	st, createBlank := coreBlankService(t)
	for _, want := range []string{"proj", "proj-2"} {
		placeholder, err := createBlank()
		if err != nil {
			t.Fatalf("blank create: %v", err)
		}
		if placeholder.Name != want {
			t.Errorf("placeholder name = %q, want %q", placeholder.Name, want)
		}
	}
	awaitNamedWorkstream(t, st, "p1", "proj")
	awaitNamedWorkstream(t, st, "p1", "proj-2")
}

// No name is set aside for a blank create's placeholder: a workstream a user
// named "New workstream" does not stand in the way of one.
func TestServiceCreateBlankIgnoresAWorkstreamCalledNewWorkstream(t *testing.T) {
	st, createBlank := coreBlankService(t, domain.Workstream{ID: "ws-1", Name: "New workstream", WorkingDir: "/elsewhere"})
	if _, err := createBlank(); err != nil {
		t.Fatalf("blank create: %v", err)
	}
	awaitNamedWorkstream(t, st, "p1", "proj")
}

// Naming and reserving are atomic against other creates: blank creates racing
// each other all succeed, each under its own name.
func TestServiceCreateConcurrentBlankNamesDoNotCollide(t *testing.T) {
	const creates = 5
	st, createBlank := coreBlankService(t)
	names := make([]string, creates)
	errs := make([]error, creates)
	var group sync.WaitGroup
	for i := range creates {
		group.Add(1)
		go func() {
			defer group.Done()
			placeholder, err := createBlank()
			names[i], errs[i] = placeholder.Name, err
		}()
	}
	group.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}
	slices.Sort(names)
	want := []string{"proj", "proj-2", "proj-3", "proj-4", "proj-5"}
	if !slices.Equal(names, want) {
		t.Fatalf("placeholder names = %v, want %v", names, want)
	}
	for _, name := range want {
		awaitNamedWorkstream(t, st, "p1", name)
	}
}

func TestServiceWorkstreamProvidersDefaultsToCore(t *testing.T) {
	st := store.NewTestStore(t, []domain.Project{{ID: "p1", Name: "p1", Path: t.TempDir(), Workstreams: []domain.Workstream{}}})
	reg := provider.NewRegistry()
	svc := provider.NewService(st, reg, 0)
	svc.Reconcile(context.Background())

	providers, err := svc.WorkstreamProvidersFor(context.Background(), "p1")
	if err != nil {
		t.Fatalf("WorkstreamProvidersFor: %v", err)
	}
	if len(providers) != 1 || providers[0].ID != provider.CoreProviderID {
		t.Fatalf("want only the core provider, got %+v", providers)
	}
}

// awaitWorkstream polls until a real (non-placeholder) workstream appears.
func awaitWorkstream(t *testing.T, st *store.Store, projectID string) domain.Workstream {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for real workstream")
		default:
		}
		time.Sleep(10 * time.Millisecond)
		for _, p := range st.Projects() {
			if p.ID != projectID {
				continue
			}
			for _, w := range p.Workstreams {
				if !strings.HasPrefix(w.ID, "creating-") {
					return w
				}
			}
		}
	}
}

// awaitSession polls the store until a session with the given agent name
// appears in the workstream.
func awaitSession(t *testing.T, st *store.Store, projectID, workstreamID, wantAgent string) domain.Session {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for session with agent %q", wantAgent)
		default:
		}
		time.Sleep(10 * time.Millisecond)
		ws, err := st.Workstream(projectID, workstreamID)
		if err != nil {
			continue
		}
		for _, s := range ws.Sessions {
			if s.Agent == wantAgent {
				return s
			}
		}
	}
}

// awaitSessionCount polls until at least n sessions with the given agent name
// exist in the workstream.
func awaitSessionCount(t *testing.T, st *store.Store, projectID, workstreamID, wantAgent string, n int) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %d sessions with agent %q", n, wantAgent)
		default:
		}
		time.Sleep(10 * time.Millisecond)
		ws, err := st.Workstream(projectID, workstreamID)
		if err != nil {
			continue
		}
		count := 0
		for _, s := range ws.Sessions {
			if s.Agent == wantAgent {
				count++
			}
		}
		if count >= n {
			return
		}
	}
}
func TestCoreSchemaExcludesWorktreeForNonGitProject(t *testing.T) {
	projectPath := t.TempDir()
	st := store.NewTestStore(t, []domain.Project{
		{ID: "nogit", Name: "nogit", Path: projectPath, GitBacked: false, Workstreams: []domain.Workstream{}},
	})
	reg := provider.NewRegistry()
	svc := provider.NewService(st, reg, 0)

	providers, err := svc.WorkstreamProvidersFor(context.Background(), "nogit")
	if err != nil {
		t.Fatalf("WorkstreamProvidersFor: %v", err)
	}
	if len(providers) == 0 {
		t.Fatal("want at least the core provider")
	}

	schema := string(providers[0].WorkstreamSchema)
	if strings.Contains(schema, "worktree") {
		t.Error("non-git project schema should not contain worktree strategy")
	}
	if strings.Contains(schema, `"branch"`) {
		t.Error("non-git project schema should not contain branch field")
	}
}

func TestCoreSchemaIncludesWorktreeForGitProject(t *testing.T) {
	projectPath := t.TempDir()
	st := store.NewTestStore(t, []domain.Project{
		{ID: "gitproj", Name: "gitproj", Path: projectPath, GitBacked: true, Workstreams: []domain.Workstream{}},
	})
	reg := provider.NewRegistry()
	svc := provider.NewService(st, reg, 0)

	providers, err := svc.WorkstreamProvidersFor(context.Background(), "gitproj")
	if err != nil {
		t.Fatalf("WorkstreamProvidersFor: %v", err)
	}
	if len(providers) == 0 {
		t.Fatal("want at least the core provider")
	}

	var parsed struct {
		Properties struct {
			Strategy struct {
				Enum []string `json:"enum"`
			} `json:"strategy"`
			Branch json.RawMessage `json:"branch"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(providers[0].WorkstreamSchema, &parsed); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}

	foundWorktree := false
	for _, strategy := range parsed.Properties.Strategy.Enum {
		if strategy == "worktree" {
			foundWorktree = true
		}
	}
	if !foundWorktree {
		t.Error("git-backed project schema should include worktree strategy")
	}
	if parsed.Properties.Branch == nil {
		t.Error("git-backed project schema should include branch field")
	}
}

func TestCoreCreateRejectsWorktreeForNonGitProject(t *testing.T) {
	projectPath := t.TempDir()
	st := store.NewTestStore(t, []domain.Project{
		{ID: "nogit", Name: "nogit", Path: projectPath, GitBacked: false, Workstreams: []domain.Workstream{}},
	})
	reg := provider.NewRegistry()
	svc := provider.NewService(st, reg, 0)

	_, err := svc.Create(context.Background(), provider.CreateInput{
		ProjectID: "nogit",
		Config:    []byte(`{"strategy":"worktree","branch":"feature"}`),
	})
	if err == nil {
		t.Fatal("worktree on non-git project should fail validation")
	}
}
