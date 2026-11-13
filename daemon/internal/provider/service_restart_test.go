package provider_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/provider"
	"github.com/eclipsesource/homeshell/daemon/internal/provider/fake"
	"github.com/eclipsesource/homeshell/daemon/internal/store"
)

// sandboxedSession creates a session of the fake's "sandboxed" option with the
// given config, and returns a ref to it once it has landed.
func sandboxedSession(t *testing.T, svc *provider.Service, st *store.Store, config string) provider.SessionRef {
	t.Helper()
	wref := createWS(t, svc, st)
	if _, err := svc.CreateSession(context.Background(), provider.CreateSessionInput{
		ProjectID: wref.ProjectID, WorkstreamID: wref.WorkstreamID, ProviderID: "fake", OptionID: "sandboxed",
		Config: json.RawMessage(config),
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if ws, err := st.Workstream(wref.ProjectID, wref.WorkstreamID); err == nil {
			for _, sess := range ws.Sessions {
				if !provider.IsStartingPlaceholder(sess) {
					return provider.SessionRef{ProjectID: wref.ProjectID, WorkstreamID: wref.WorkstreamID, SessionID: sess.ID}
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the session to land")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// awaitLaunchConfig waits for a session's recorded config: on create it lands with
// the session, after the call returned, and until then the session reports none.
func awaitLaunchConfig(t *testing.T, svc *provider.Service, ref provider.SessionRef) provider.RestartSettings {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		launch, err := svc.SessionLaunchConfig(context.Background(), ref)
		if err == nil && launch.Config != nil {
			return launch
		}
		if time.Now().After(deadline) {
			t.Fatalf("SessionLaunchConfig: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCreateSessionRecordsWhatItStartedWith(t *testing.T) {
	svc, st := newService(t)
	config := `{"model":"opus","enclave":{"allowDomains":["api.example.com"]}}`
	ref := sandboxedSession(t, svc, st, config)

	launch := awaitLaunchConfig(t, svc, ref)
	if launch.ProviderID != "fake" || launch.OptionID != "sandboxed" || string(launch.Config) != config {
		t.Errorf("launch config = %+v; want fake/sandboxed with %s", launch, config)
	}
}

func TestRestartSessionRecordsTheNewSettings(t *testing.T) {
	svc, st := newService(t)
	ref := sandboxedSession(t, svc, st, `{"model":"opus"}`)
	awaitLaunchConfig(t, svc, ref)

	config := `{"model":"sonnet","enclave":{"bridgePorts":[5432],"hostConfig":true}}`
	sess, err := svc.RestartSession(context.Background(), provider.RestartSessionInput{
		Session: ref, Config: json.RawMessage(config),
	})
	if err != nil {
		t.Fatalf("RestartSession: %v", err)
	}
	if sess.ID != ref.SessionID {
		t.Errorf("RestartSession returned %q; want the same session", sess.ID)
	}
	// Read off the store: the session is starting, which SessionLaunchConfig refuses.
	launch, _ := st.LaunchConfig(ref.SessionID)
	// The option is the one it was started as — the fake names its sessions by
	// label, so it could not be read back off the session.
	if launch.OptionID != "sandboxed" || string(launch.Config) != config {
		t.Errorf("launch config = %+v; want sandboxed with %s", launch, config)
	}
}

// A restart that carries no config replays the one the session was last started
// with, so a plain caller does not restart it on the defaults; an explicit config,
// even an empty one, is used as given.
func TestRestartSessionWithoutAConfigReplaysTheRecordedOne(t *testing.T) {
	svc, st := newService(t)
	original := `{"model":"opus","enclave":{"hostConfig":true}}`
	ref := sandboxedSession(t, svc, st, original)
	awaitLaunchConfig(t, svc, ref)

	for _, absent := range []json.RawMessage{nil, json.RawMessage(`null`)} {
		restartOnceIdle(t, svc, provider.RestartSessionInput{Session: ref, Config: absent, Fresh: true})
		if launch, _ := st.LaunchConfig(ref.SessionID); string(launch.Config) != original {
			t.Errorf("launch config after a restart with config %s = %s; want %s", absent, launch.Config, original)
		}
	}

	restartOnceIdle(t, svc, provider.RestartSessionInput{Session: ref, Config: json.RawMessage(`{}`)})
	if launch, _ := st.LaunchConfig(ref.SessionID); string(launch.Config) != `{}` {
		t.Errorf("launch config after a restart with {} = %s; want {}", launch.Config)
	}
}

// restartOnceIdle restarts a session, retrying while an earlier restart of it is
// still in flight.
func restartOnceIdle(t *testing.T, svc *provider.Service, in provider.RestartSessionInput) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, err := svc.RestartSession(context.Background(), in)
		if err == nil {
			return
		}
		if !errors.Is(err, provider.ErrSessionStarting) || time.Now().After(deadline) {
			t.Fatalf("RestartSession(%s): %v", in.Config, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A session HomeShell did not start has no record, but reports the option a
// restart would start it as — its agent's, matched by label — and no config.
func TestSessionLaunchConfigOfAnAdoptedSession(t *testing.T) {
	dir := t.TempDir()
	projectStore := store.NewTestStore(t, []domain.Project{{ID: "p1", Name: "p1", Path: dir, Workstreams: []domain.Workstream{
		{ID: "ws1", Name: "ws", Provider: "fake", WorkingDir: dir},
	}}})
	registry := provider.NewRegistry()
	registerFake(t, registry, fake.Config{Seed: map[string][]domain.Workstream{"p1": {{WorkingDir: dir, Sessions: []domain.Session{
		{ID: "sess-adopted", Title: "Adopted", Agent: "Codex", State: domain.SessionRunning},
	}}}}})
	service := provider.NewService(projectStore, registry, 0)
	service.Reconcile(context.Background())
	ref := provider.SessionRef{ProjectID: "p1", WorkstreamID: "ws1", SessionID: "sess-adopted"}

	launch, err := service.SessionLaunchConfig(context.Background(), ref)
	if err != nil {
		t.Fatalf("SessionLaunchConfig: %v", err)
	}
	if launch.ProviderID != "fake" || launch.OptionID != "codex" || launch.Config != nil {
		t.Errorf("launch config = %+v; want fake/codex with no config", launch)
	}

	// A record another provider made is not this session's: reported, like a
	// restart treats it, as no record at all.
	if err := projectStore.SetLaunchConfig("p1", "sess-adopted", domain.LaunchConfig{
		ProviderID: "other", OptionID: "sandboxed", Config: json.RawMessage(`{"model":"opus"}`),
	}); err != nil {
		t.Fatalf("SetLaunchConfig: %v", err)
	}
	if launch, _ := service.SessionLaunchConfig(context.Background(), ref); launch.OptionID != "codex" || launch.Config != nil {
		t.Errorf("launch config with another provider's record = %+v; want fake/codex with no config", launch)
	}
}

// A bare restart whose recorded settings no longer fit the option's schema is
// refused per field, saying it is the recorded settings that are at fault.
func TestRestartSessionRefusesRecordedSettingsThatNoLongerFit(t *testing.T) {
	svc, st := newService(t)
	ref := sandboxedSession(t, svc, st, `{"model":"opus"}`)
	awaitLaunchConfig(t, svc, ref)
	if err := st.SetLaunchConfig(ref.ProjectID, ref.SessionID, domain.LaunchConfig{
		ProviderID: "fake", OptionID: "sandboxed", Config: json.RawMessage(`{"model":"gpt-2"}`),
	}); err != nil {
		t.Fatalf("SetLaunchConfig: %v", err)
	}

	_, err := svc.RestartSession(context.Background(), provider.RestartSessionInput{Session: ref})
	var invalid *provider.ValidationError
	if !errors.As(err, &invalid) || !strings.Contains(invalid.Message, "last started with are no longer valid") {
		t.Fatalf("RestartSession = %v; want a ValidationError about the recorded settings", err)
	}
	if _, ok := invalid.Fields["/model"]; !ok {
		t.Errorf("fields = %v; want the offending field named", invalid.Fields)
	}
}

// A session recorded with the Enclave group's playwrightMcp, which Enclave 1.0
// dropped, still restarts bare; the record keeps the key the provider ignores.
func TestBareRestartAcceptsARecordedPlaywrightMCP(t *testing.T) {
	svc, st := newService(t)
	ref := sandboxedSession(t, svc, st, `{"model":"opus"}`)
	awaitLaunchConfig(t, svc, ref)
	recorded := `{"model":"opus","enclave":{"playwrightMcp":true}}`
	if err := st.SetLaunchConfig(ref.ProjectID, ref.SessionID, domain.LaunchConfig{
		ProviderID: "fake", OptionID: "sandboxed", Config: json.RawMessage(recorded),
	}); err != nil {
		t.Fatalf("SetLaunchConfig: %v", err)
	}

	restartOnceIdle(t, svc, provider.RestartSessionInput{Session: ref})
	if launch, _ := st.LaunchConfig(ref.SessionID); !sameJSON(launch.Config, recorded) {
		t.Errorf("launch config after a bare restart = %s; want the record kept, %s", launch.Config, recorded)
	}
}

// A restart is refused before anything is torn down when its config does not
// fit the option's schema, and the recorded settings stay as they were.
func TestRestartSessionValidatesTheConfig(t *testing.T) {
	svc, st := newService(t)
	original := `{"model":"opus"}`
	ref := sandboxedSession(t, svc, st, original)
	awaitLaunchConfig(t, svc, ref)

	_, err := svc.RestartSession(context.Background(), provider.RestartSessionInput{
		Session: ref, Config: json.RawMessage(`{"enclave":{"bridgePorts":[70000]}}`),
	})
	var verr *provider.ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("RestartSession = %v; want a ValidationError", err)
	}
	if _, ok := verr.Fields["/enclave/bridgePorts/0"]; !ok {
		t.Errorf("fields = %v; want the offending port named", verr.Fields)
	}
	if launch, _ := st.LaunchConfig(ref.SessionID); string(launch.Config) != original {
		t.Errorf("launch config = %s; want it unchanged", launch.Config)
	}
}

func TestRestartSessionRefusals(t *testing.T) {
	svc, st := newService(t)
	ref := sandboxedSession(t, svc, st, `{}`)

	unknown := ref
	unknown.SessionID = "sess-nope"
	if _, err := svc.RestartSession(context.Background(), provider.RestartSessionInput{Session: unknown}); !errors.Is(err, provider.ErrSessionNotFound) {
		t.Errorf("unknown session: %v; want ErrSessionNotFound", err)
	}
	if _, err := svc.SessionLaunchConfig(context.Background(), unknown); !errors.Is(err, provider.ErrSessionNotFound) {
		t.Errorf("launch config of an unknown session: %v; want ErrSessionNotFound", err)
	}
}

// placeholderOf creates a session of option in a new workstream, and returns a
// ref to its placeholder once the placeholder is in state.
func placeholderOf(t *testing.T, svc *provider.Service, st *store.Store, option string, state domain.SessionState) provider.SessionRef {
	t.Helper()
	wref := createWS(t, svc, st)
	created, err := svc.CreateSession(context.Background(), provider.CreateSessionInput{
		ProjectID: wref.ProjectID, WorkstreamID: wref.WorkstreamID, ProviderID: "fake", OptionID: option,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	ref := provider.SessionRef{ProjectID: wref.ProjectID, WorkstreamID: wref.WorkstreamID, SessionID: created.ID}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if ws, err := st.Workstream(ref.ProjectID, ref.WorkstreamID); err == nil {
			for _, sess := range ws.Sessions {
				if sess.ID == ref.SessionID && sess.State == state {
					return ref
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for placeholder %s to be %s", ref.SessionID, state)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A placeholder has no settings to restart with, and says why: it is still
// starting, or its create failed and nothing ever started.
func TestRestartSessionRefusesPlaceholders(t *testing.T) {
	svc, st := newSlowRestartService(t)
	tests := []struct {
		name   string
		option string
		state  domain.SessionState
		want   error
	}{
		{name: "still starting", option: "sandboxed", state: domain.SessionStarting, want: provider.ErrSessionStarting},
		{name: "a failed create", option: "no-such-agent", state: domain.SessionError, want: provider.ErrSessionNeverStarted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ref := placeholderOf(t, svc, st, tt.option, tt.state)
			if _, err := svc.SessionLaunchConfig(context.Background(), ref); !errors.Is(err, tt.want) {
				t.Errorf("SessionLaunchConfig = %v; want %v", err, tt.want)
			}
			if _, err := svc.RestartSession(context.Background(), provider.RestartSessionInput{Session: ref}); !errors.Is(err, tt.want) {
				t.Errorf("RestartSession = %v; want %v", err, tt.want)
			}
		})
	}
}

// newSlowRestartService is a service over the fake whose restarts take long enough
// to still be in flight when a test acts on them.
func newSlowRestartService(t *testing.T) (*provider.Service, *store.Store) {
	t.Helper()
	st := store.NewTestStore(t, []domain.Project{{ID: "p1", Name: "p1", Path: t.TempDir(), Workstreams: []domain.Workstream{}}})
	reg := provider.NewRegistry()
	registerFake(t, reg, fake.Config{SessionStartup: 300 * time.Millisecond})
	svc := provider.NewService(st, reg, 0)
	svc.Reconcile(context.Background())
	return svc, st
}

// A second restart of a session while the first is in flight is refused: it would
// stop the container the first is bringing up. Once the first ends, the session
// can be restarted again.
func TestRestartSessionRefusesARestartInFlight(t *testing.T) {
	svc, st := newSlowRestartService(t)
	ref := sandboxedSession(t, svc, st, `{}`)

	sess, err := svc.RestartSession(context.Background(), provider.RestartSessionInput{Session: ref})
	if err != nil {
		t.Fatalf("RestartSession: %v", err)
	}
	if sess.State != domain.SessionStarting {
		t.Errorf("RestartSession returned the session %s; want it starting", sess.State)
	}
	if _, err := svc.RestartSession(context.Background(), provider.RestartSessionInput{Session: ref}); !errors.Is(err, provider.ErrSessionStarting) {
		t.Errorf("second RestartSession = %v; want ErrSessionStarting", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, err := svc.RestartSession(context.Background(), provider.RestartSessionInput{Session: ref})
		if err == nil {
			break
		}
		if !errors.Is(err, provider.ErrSessionStarting) || time.Now().After(deadline) {
			t.Fatalf("RestartSession after the first finished: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Removing a session cancels its restart. The fake's restart brings the session
// back under its ID even when it was removed meanwhile, so a restart left running
// would resurrect it here; cancelling it is what keeps it gone.
func TestRemoveSessionCancelsItsRestart(t *testing.T) {
	svc, st := newSlowRestartService(t)
	removed := sandboxedSession(t, svc, st, `{}`)
	// Restarted alongside, to tell when the removed one's restart would have ended.
	if _, err := svc.CreateSession(context.Background(), provider.CreateSessionInput{
		ProjectID: removed.ProjectID, WorkstreamID: removed.WorkstreamID, ProviderID: "fake", OptionID: "claude",
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	witnessSession := awaitLandedSession(t, st, removed.ProjectID, removed.WorkstreamID, "Claude Code")
	witness := provider.SessionRef{ProjectID: removed.ProjectID, WorkstreamID: removed.WorkstreamID, SessionID: witnessSession.ID}

	for _, ref := range []provider.SessionRef{removed, witness} {
		if _, err := svc.RestartSession(context.Background(), provider.RestartSessionInput{Session: ref}); err != nil {
			t.Fatalf("RestartSession: %v", err)
		}
	}
	if err := svc.RemoveSession(context.Background(), removed); err != nil {
		t.Fatalf("RemoveSession: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		ws, _ := st.Workstream(witness.ProjectID, witness.WorkstreamID)
		if sess, ok := findStoredSession(ws, witness.SessionID); ok && sess.State == domain.SessionRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the other restart to end")
		}
		time.Sleep(10 * time.Millisecond)
	}
	svc.Reconcile(context.Background())
	ws, err := st.Workstream(removed.ProjectID, removed.WorkstreamID)
	if err != nil {
		t.Fatalf("Workstream: %v", err)
	}
	if sess, ok := findStoredSession(ws, removed.SessionID); ok {
		t.Errorf("the removed session is back: %+v", sess)
	}
}

// awaitLandedSession waits for a session with the given agent that is no starting
// placeholder.
func awaitLandedSession(t *testing.T, st *store.Store, projectID, workstreamID, wantAgent string) domain.Session {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if ws, err := st.Workstream(projectID, workstreamID); err == nil {
			for _, session := range ws.Sessions {
				if session.Agent == wantAgent && !provider.IsStartingPlaceholder(session) {
					return session
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for a landed session with agent %q", wantAgent)
		}
	}
}

// Removing a project leaves its sessions running, but cancels a restart in flight
// and removes its session. The fake's restart brings the session back running if
// it runs to the end; cancelled, it would leave it starting.
func TestRemoveProjectCancelsItsRestarts(t *testing.T) {
	fakeProvider := fake.New(fake.Config{SessionStartup: 300 * time.Millisecond})
	svc, st := newServiceWith(t, fakeProvider, fakeProvider)
	restarted := sandboxedSession(t, svc, st, `{}`)
	if _, err := svc.CreateSession(context.Background(), provider.CreateSessionInput{
		ProjectID: restarted.ProjectID, WorkstreamID: restarted.WorkstreamID, ProviderID: "fake", OptionID: "claude",
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	kept := awaitLandedSession(t, st, restarted.ProjectID, restarted.WorkstreamID, "Claude Code")
	project := st.Projects()[0]

	if _, err := svc.RestartSession(context.Background(), provider.RestartSessionInput{Session: restarted}); err != nil {
		t.Fatalf("RestartSession: %v", err)
	}
	if err := svc.RemoveProject(context.Background(), restarted.ProjectID); err != nil {
		t.Fatalf("RemoveProject: %v", err)
	}
	if len(st.Projects()) != 0 {
		t.Errorf("projects after the removal = %+v; want none", st.Projects())
	}
	// The interrupted restart's session is removed in the background.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	svc.Shutdown(ctx)
	if left := discoveredIDs(t, fakeProvider, project); !slices.Equal(left, []string{kept.ID}) {
		t.Errorf("sessions left running = %q; want only %q", left, kept.ID)
	}
}

// discoveredIDs lists the IDs of the sessions the fake finds in a project that
// have not ended.
func discoveredIDs(t *testing.T, fakeProvider *fake.Provider, project domain.Project) []string {
	t.Helper()
	discovered, err := fakeProvider.DiscoverSessions(context.Background(), provider.ProjectRef{ID: project.ID, Path: project.Path})
	if err != nil {
		t.Fatalf("DiscoverSessions: %v", err)
	}
	var ids []string
	for _, found := range discovered {
		if !found.Session.State.Terminal() {
			ids = append(ids, found.Session.ID)
		}
	}
	return ids
}

// Removing a project leaves alone a restart of a session another project still
// lists, as one sharing its directory does: the session is that project's too.
func TestRemoveProjectLeavesARestartOfASessionAnotherProjectLists(t *testing.T) {
	fakeProvider := fake.New(fake.Config{SessionStartup: 300 * time.Millisecond})
	svc, st := newServiceWith(t, fakeProvider, fakeProvider)
	restarted := sandboxedSession(t, svc, st, `{}`)
	project := st.Projects()[0]
	if err := st.AddProject(&domain.Project{ID: "p2", Name: "p2", Path: filepath.Dir(project.Path)}); err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	svc.Reconcile(context.Background())
	if !slices.ContainsFunc(st.Projects()[1].Workstreams, func(workstream domain.Workstream) bool {
		_, ok := findStoredSession(workstream, restarted.SessionID)
		return ok
	}) {
		t.Fatalf("the other project does not list the session: %+v", st.Projects()[1])
	}

	if _, err := svc.RestartSession(context.Background(), provider.RestartSessionInput{Session: restarted}); err != nil {
		t.Fatalf("RestartSession: %v", err)
	}
	if err := svc.RemoveProject(context.Background(), restarted.ProjectID); err != nil {
		t.Fatalf("RemoveProject: %v", err)
	}
	// Cancelled, the fake's restart would leave the session to be removed, never
	// running again.
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		discovered, err := fakeProvider.DiscoverSessions(context.Background(), provider.ProjectRef{ID: "p2", Path: filepath.Dir(project.Path)})
		if err != nil {
			t.Fatalf("DiscoverSessions: %v", err)
		}
		if len(discovered) == 1 && discovered[0].Session.State == domain.SessionRunning {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("sessions left = %+v; want %q, restarted", discovered, restarted.SessionID)
		}
	}
}

// surveyingFake is the fake, as a workstream provider that suggests the folder
// in folder, whoever claims it.
type surveyingFake struct {
	*fake.Provider
	folder string
}

func (surveying *surveyingFake) ListFolders(context.Context, provider.ProjectRef) ([]provider.Folder, error) {
	return []provider.Folder{{Path: surveying.folder}}, nil
}

func (surveying *surveyingFake) Survey(_ provider.ProjectRef, folders []provider.Folder, _ []domain.Workstream,
	_ []string) provider.Survey {
	var survey provider.Survey
	for _, folder := range folders {
		survey.Untracked = append(survey.Untracked, domain.UntrackedWorktree{Name: "wt", Path: folder.Path})
	}
	return survey
}

func (surveying *surveyingFake) TrackFolder(domain.UntrackedWorktree, func(string) bool) domain.Workstream {
	return domain.Workstream{}
}

// Removing a project cancels the restart of a session another project only
// hides on an untracked worktree: there is no workstream to show it in.
func TestRemoveProjectCancelsARestartOfASessionOnlyHiddenElsewhere(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	fakeProvider := fake.New(fake.Config{SessionStartup: 300 * time.Millisecond})
	surveying := &surveyingFake{Provider: fakeProvider}
	st := store.NewTestStore(t, []domain.Project{{ID: "p1", Name: "p1", Path: t.TempDir(), Workstreams: []domain.Workstream{}}})
	reg := provider.NewRegistry()
	if err := reg.RegisterWorkstreamProvider(surveying); err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterSessionProvider(fakeProvider); err != nil {
		t.Fatal(err)
	}
	svc := provider.NewService(st, reg, 0)
	svc.Reconcile(context.Background())
	restarted := sandboxedSession(t, svc, st, `{}`)
	project := st.Projects()[0]
	workstream, err := st.Workstream(restarted.ProjectID, restarted.WorkstreamID)
	if err != nil {
		t.Fatal(err)
	}
	// A git project holding p1's folder, its workstream's folder a worktree of it.
	surveying.folder = workstream.WorkingDir
	parent := filepath.Dir(project.Path)
	if out, err := exec.Command("git", "init", "-q", parent).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	if err := st.AddProject(&domain.Project{ID: "p2", Name: "p2", Path: parent, GitBacked: true}); err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	svc.Reconcile(context.Background())
	if hidden := st.Projects()[1].UntrackedWorktrees; len(hidden) != 1 || len(hidden[0].Sessions) == 0 {
		t.Fatalf("the other project does not hide the session: %+v", st.Projects()[1])
	}

	if _, err := svc.RestartSession(context.Background(), provider.RestartSessionInput{Session: restarted}); err != nil {
		t.Fatalf("RestartSession: %v", err)
	}
	if err := svc.RemoveProject(context.Background(), restarted.ProjectID); err != nil {
		t.Fatalf("RemoveProject: %v", err)
	}
	// The interrupted restart's session is removed in the background.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	svc.Shutdown(ctx)
	if left := discoveredIDs(t, fakeProvider, project); len(left) != 0 {
		t.Errorf("sessions left running = %q; want the restarted one removed", left)
	}
}

// failingFake is the fake, whose restarts wait for release, then fail without
// touching the session.
type failingFake struct {
	*fake.Provider
	release chan struct{}
}

func (failing *failingFake) PrepareRestart(context.Context, provider.RestartSessionRequest) (provider.RestartFunc, error) {
	return func(context.Context) error {
		<-failing.release
		return errors.New("docker is not running")
	}, nil
}

// A restart left to finish once its project is removed, because another project
// lists its session, finishes for that project: its failure shows there, and the
// session is left recorded as started with what it still runs with.
func TestARestartOutlivingItsProjectFinishesForTheOneListingItsSession(t *testing.T) {
	failing := &failingFake{Provider: fake.New(fake.Config{}), release: make(chan struct{})}
	svc, st := newServiceWith(t, failing.Provider, failing)
	original := `{"model":"opus"}`
	restarted := sandboxedSession(t, svc, st, original)
	awaitLaunchConfig(t, svc, restarted)
	if err := st.AddProject(&domain.Project{ID: "p2", Name: "p2", Path: filepath.Dir(st.Projects()[0].Path)}); err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	svc.Reconcile(context.Background())
	listing := slices.IndexFunc(st.Projects()[1].Workstreams, func(workstream domain.Workstream) bool {
		_, ok := findStoredSession(workstream, restarted.SessionID)
		return ok
	})
	if listing < 0 {
		t.Fatalf("the other project does not list the session: %+v", st.Projects()[1])
	}
	workstreamID := st.Projects()[1].Workstreams[listing].ID

	if _, err := svc.RestartSession(context.Background(), provider.RestartSessionInput{
		Session: restarted, Config: json.RawMessage(`{"model":"sonnet"}`),
	}); err != nil {
		t.Fatalf("RestartSession: %v", err)
	}
	if err := svc.RemoveProject(context.Background(), restarted.ProjectID); err != nil {
		t.Fatalf("RemoveProject: %v", err)
	}
	close(failing.release)
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		workstream, _ := st.Workstream("p2", workstreamID)
		launch, _ := st.LaunchConfig(restarted.SessionID)
		if strings.Contains(workstream.StatusMessage, "docker is not running") && sameJSON(launch.Config, original) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("status message = %q, launch config = %s; want the restart's failure and %s", workstream.StatusMessage, launch.Config, original)
		}
	}
}

// A restart's reserved slot moves with its session when its project is removed:
// the workstream that lists the session now counts it, so a provider's session
// limit holds there until the restart finishes.
func TestARestartOutlivingItsProjectKeepsItsSlot(t *testing.T) {
	failing := &failingFake{Provider: fake.New(fake.Config{MaxConcurrentSessions: 1}), release: make(chan struct{})}
	defer close(failing.release)
	svc, st := newServiceWith(t, failing.Provider, failing)
	restarted := sandboxedSession(t, svc, st, `{}`)
	if err := svc.StopSession(context.Background(), restarted); err != nil {
		t.Fatalf("StopSession: %v", err)
	}
	if err := st.AddProject(&domain.Project{ID: "p2", Name: "p2", Path: filepath.Dir(st.Projects()[0].Path)}); err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	svc.Reconcile(context.Background())
	listing := slices.IndexFunc(st.Projects()[1].Workstreams, func(workstream domain.Workstream) bool {
		_, ok := findStoredSession(workstream, restarted.SessionID)
		return ok
	})
	if listing < 0 {
		t.Fatalf("the other project does not list the session: %+v", st.Projects()[1])
	}
	workstreamID := st.Projects()[1].Workstreams[listing].ID

	if _, err := svc.RestartSession(context.Background(), provider.RestartSessionInput{Session: restarted, Fresh: true}); err != nil {
		t.Fatalf("RestartSession: %v", err)
	}
	if err := svc.RemoveProject(context.Background(), restarted.ProjectID); err != nil {
		t.Fatalf("RemoveProject: %v", err)
	}
	if _, err := svc.CreateSession(context.Background(), provider.CreateSessionInput{
		ProjectID: "p2", WorkstreamID: workstreamID, ProviderID: "fake", OptionID: "claude",
	}); !errors.Is(err, provider.ErrConcurrencyLimit) {
		t.Errorf("creating a second session while the restart holds the only slot = %v; want ErrConcurrencyLimit", err)
	}
}

// prepareHoldingFake is the fake, holding each restart in PrepareRestart until
// released.
type prepareHoldingFake struct {
	*fake.Provider
	entered, release chan struct{}
}

func (holding *prepareHoldingFake) PrepareRestart(ctx context.Context, req provider.RestartSessionRequest) (provider.RestartFunc, error) {
	close(holding.entered)
	<-holding.release
	return holding.Provider.PrepareRestart(ctx, req)
}

// Removing a project while a restart of one of its sessions is still being
// prepared cancels the restart before it touches the session, which is left
// running as the project's other sessions are: only a restart interrupted
// halfway leaves its session to be removed.
func TestRemoveProjectLeavesASessionWhoseRestartHadNotRun(t *testing.T) {
	holding := &prepareHoldingFake{
		Provider: fake.New(fake.Config{}),
		entered:  make(chan struct{}),
		release:  make(chan struct{}),
	}
	svc, st := newServiceWith(t, holding.Provider, holding)
	restarted := sandboxedSession(t, svc, st, `{}`)
	project := st.Projects()[0]
	restartErr := make(chan error, 1)
	go func() {
		_, err := svc.RestartSession(context.Background(), provider.RestartSessionInput{Session: restarted})
		restartErr <- err
	}()
	<-holding.entered

	if err := svc.RemoveProject(context.Background(), restarted.ProjectID); err != nil {
		t.Fatalf("RemoveProject: %v", err)
	}
	close(holding.release)
	if err := <-restartErr; !errors.Is(err, provider.ErrSessionNotFound) {
		t.Errorf("RestartSession = %v; want ErrSessionNotFound", err)
	}
	if left := discoveredIDs(t, holding.Provider, project); !slices.Equal(left, []string{restarted.SessionID}) {
		t.Errorf("sessions left running = %q; want %q, as it was", left, restarted.SessionID)
	}
}

// cancelWaitingFake is the fake, whose restarts wait for their cancellation and
// return without touching the session (or, with afterRun, once the fake's has
// run).
type cancelWaitingFake struct {
	*fake.Provider
	afterRun bool
	entered  chan struct{}
}

func (waiting *cancelWaitingFake) PrepareRestart(ctx context.Context, req provider.RestartSessionRequest) (provider.RestartFunc, error) {
	run, err := waiting.Provider.PrepareRestart(ctx, req)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context) error {
		if !waiting.afterRun {
			close(waiting.entered)
			<-ctx.Done()
			return ctx.Err()
		}
		err := run(ctx)
		close(waiting.entered)
		<-ctx.Done()
		return err
	}, nil
}

// Removing a project cancels a restart that is running, but removes its session
// only when the restart was interrupted halfway: one cancelled before it touched
// the session, or once it brought it back, leaves it running as the project's
// other sessions are.
func TestRemoveProjectLeavesASessionWhoseRestartWasNotInterrupted(t *testing.T) {
	tests := []struct {
		name     string
		afterRun bool
	}{
		{name: "cancelled before the restart touched the session"},
		{name: "cancelled once the restart brought the session back", afterRun: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			waiting := &cancelWaitingFake{Provider: fake.New(fake.Config{}), afterRun: tt.afterRun, entered: make(chan struct{})}
			svc, st := newServiceWith(t, waiting.Provider, waiting)
			restarted := sandboxedSession(t, svc, st, `{}`)
			project := st.Projects()[0]
			if _, err := svc.RestartSession(context.Background(), provider.RestartSessionInput{Session: restarted}); err != nil {
				t.Fatalf("RestartSession: %v", err)
			}
			<-waiting.entered

			if err := svc.RemoveProject(context.Background(), restarted.ProjectID); err != nil {
				t.Fatalf("RemoveProject: %v", err)
			}
			if left := discoveredIDs(t, waiting.Provider, project); !slices.Equal(left, []string{restarted.SessionID}) {
				t.Errorf("sessions left running = %q; want %q", left, restarted.SessionID)
			}
		})
	}
}

// interruptingFake is the fake, whose restarts wait for their cancellation and
// for release, then report themselves interrupted; it counts the removals.
type interruptingFake struct {
	*fake.Provider
	entered, release chan struct{}
	removals         atomic.Int32
}

func (interrupting *interruptingFake) PrepareRestart(context.Context, provider.RestartSessionRequest) (provider.RestartFunc, error) {
	return func(ctx context.Context) error {
		close(interrupting.entered)
		<-ctx.Done()
		<-interrupting.release
		return provider.ErrRestartInterrupted
	}, nil
}

func (interrupting *interruptingFake) RemoveSession(ctx context.Context, ref provider.SessionRef) error {
	interrupting.removals.Add(1)
	return interrupting.Provider.RemoveSession(ctx, ref)
}

// A second removal of a project, landing while the first one's interrupted
// restarts are still stopping, is refused and tears nothing down again.
func TestRemovingAProjectTwiceTearsItsRestartsDownOnce(t *testing.T) {
	interrupting := &interruptingFake{Provider: fake.New(fake.Config{}), entered: make(chan struct{}), release: make(chan struct{})}
	svc, st := newServiceWith(t, interrupting.Provider, interrupting)
	restarted := sandboxedSession(t, svc, st, `{}`)
	if _, err := svc.RestartSession(context.Background(), provider.RestartSessionInput{Session: restarted}); err != nil {
		t.Fatalf("RestartSession: %v", err)
	}
	<-interrupting.entered

	if err := svc.RemoveProject(context.Background(), restarted.ProjectID); err != nil {
		t.Fatalf("RemoveProject: %v", err)
	}
	if err := svc.RemoveProject(context.Background(), restarted.ProjectID); !errors.Is(err, provider.ErrProjectNotFound) {
		t.Errorf("second RemoveProject = %v; want ErrProjectNotFound", err)
	}
	close(interrupting.release)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	svc.Shutdown(ctx)
	if removals := interrupting.removals.Load(); removals != 1 {
		t.Errorf("the session was removed %d times; want once", removals)
	}
}

// Shutting the daemon down cancels a restart in flight, and waits for it.
func TestShutdownCancelsARestartInFlight(t *testing.T) {
	waiting := &cancelWaitingFake{Provider: fake.New(fake.Config{}), entered: make(chan struct{})}
	svc, st := newServiceWith(t, waiting.Provider, waiting)
	restarted := sandboxedSession(t, svc, st, `{}`)
	if _, err := svc.RestartSession(context.Background(), provider.RestartSessionInput{Session: restarted}); err != nil {
		t.Fatalf("RestartSession: %v", err)
	}
	<-waiting.entered

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	svc.Shutdown(ctx)

	if ctx.Err() != nil {
		t.Error("Shutdown waited out its deadline; the restart in flight was not cancelled")
	}
}

// Shutting the daemon down keeps what a session is recorded as started with
// true: a restart cancelled before it began leaves the previous settings, and
// one that brought the session back keeps the new ones, even if the
// cancellation lands before the service has seen it return.
func TestShutdownKeepsTheSettingsTheSessionRunsWith(t *testing.T) {
	original, requested := `{"model":"opus"}`, `{"model":"sonnet"}`
	tests := []struct {
		name     string
		afterRun bool
		want     string
	}{
		{name: "cancelled before the restart began", want: original},
		{name: "cancelled once the restart brought the session back", afterRun: true, want: requested},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			waiting := &cancelWaitingFake{Provider: fake.New(fake.Config{}), afterRun: tt.afterRun, entered: make(chan struct{})}
			svc, st := newServiceWith(t, waiting.Provider, waiting)
			restarted := sandboxedSession(t, svc, st, original)
			awaitLaunchConfig(t, svc, restarted)
			if _, err := svc.RestartSession(context.Background(), provider.RestartSessionInput{
				Session: restarted, Config: json.RawMessage(requested),
			}); err != nil {
				t.Fatalf("RestartSession: %v", err)
			}
			<-waiting.entered

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			svc.Shutdown(ctx)

			if launch, _ := st.LaunchConfig(restarted.SessionID); !sameJSON(launch.Config, tt.want) {
				t.Errorf("launch config = %s; want %s", launch.Config, tt.want)
			}
		})
	}
}

// storedSession is the session ref names, as the store lists it.
func storedSession(t *testing.T, st *store.Store, ref provider.SessionRef) domain.Session {
	t.Helper()
	workstream, err := st.Workstream(ref.ProjectID, ref.WorkstreamID)
	if err != nil {
		t.Fatalf("Workstream: %v", err)
	}
	session, _ := findStoredSession(workstream, ref.SessionID)
	return session
}

func findStoredSession(ws domain.Workstream, sessionID string) (domain.Session, bool) {
	for _, sess := range ws.Sessions {
		if sess.ID == sessionID {
			return sess, true
		}
	}
	return domain.Session{}, false
}

// Restarting a session that is not running starts it, and so needs a free slot,
// as StartSession does; restarting one that is running takes nothing new.
func TestRestartSessionRespectsTheConcurrencyLimit(t *testing.T) {
	svc, st := newServiceCap(t, 1)
	wref := createWS(t, svc, st)
	createSession := func(optionID, agent string) provider.SessionRef {
		t.Helper()
		if _, err := svc.CreateSession(context.Background(), provider.CreateSessionInput{
			ProjectID: wref.ProjectID, WorkstreamID: wref.WorkstreamID, ProviderID: "fake", OptionID: optionID,
		}); err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		sess := awaitSession(t, st, wref.ProjectID, wref.WorkstreamID, agent)
		return provider.SessionRef{ProjectID: wref.ProjectID, WorkstreamID: wref.WorkstreamID, SessionID: sess.ID}
	}
	stopped := createSession("claude", "Claude Code")
	if err := svc.StopSession(context.Background(), stopped); err != nil {
		t.Fatalf("StopSession: %v", err)
	}
	svc.Reconcile(context.Background())
	running := createSession("codex", "Codex")

	if _, err := svc.RestartSession(context.Background(), provider.RestartSessionInput{Session: stopped}); !errors.Is(err, provider.ErrConcurrencyLimit) {
		t.Errorf("restarting the stopped session = %v; want ErrConcurrencyLimit", err)
	}
	if _, err := svc.RestartSession(context.Background(), provider.RestartSessionInput{Session: running}); err != nil {
		t.Errorf("restarting the running session: %v", err)
	}
}

// A restart's reserved slot and its session coming back up are one session, not
// two: counting both would refuse a start the limit allows.
func TestRestartingSessionTakesOneSlot(t *testing.T) {
	dir := t.TempDir()
	sessions := []domain.Session{
		{ID: "sess-a", Title: "A", Agent: "Claude Code", State: domain.SessionStopped},
		{ID: "sess-b", Title: "B", Agent: "Claude Code", State: domain.SessionRunning},
		{ID: "sess-c", Title: "C", Agent: "Claude Code", State: domain.SessionStopped},
	}
	st := store.NewTestStore(t, []domain.Project{{ID: "p1", Name: "p1", Path: dir, Workstreams: []domain.Workstream{
		{ID: "ws1", Name: "ws", Provider: "fake", WorkingDir: dir},
	}}})
	reg := provider.NewRegistry()
	registerFake(t, reg, fake.Config{
		SessionStartup: 300 * time.Millisecond, MaxConcurrentSessions: 3,
		Seed: map[string][]domain.Workstream{"p1": {{WorkingDir: dir, Sessions: sessions}}},
	})
	svc := provider.NewService(st, reg, 0)
	svc.Reconcile(context.Background())
	restarted := provider.SessionRef{ProjectID: "p1", WorkstreamID: "ws1", SessionID: "sess-a"}
	other := provider.SessionRef{ProjectID: "p1", WorkstreamID: "ws1", SessionID: "sess-c"}

	if _, err := svc.RestartSession(context.Background(), provider.RestartSessionInput{Session: restarted, Fresh: true}); err != nil {
		t.Fatalf("RestartSession: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		svc.Reconcile(context.Background())
		ws, _ := st.Workstream(restarted.ProjectID, restarted.WorkstreamID)
		if sess, ok := findStoredSession(ws, restarted.SessionID); ok && sess.State == domain.SessionStarting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the restarting session to show as starting")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := svc.RestartSession(context.Background(), provider.RestartSessionInput{Session: other, Fresh: true}); err != nil {
		t.Errorf("restarting a second stopped session with two of three slots taken: %v", err)
	}
}

// A restart continues a session's conversation only when no other session of
// its option works in its directory — in its workstream, or another project's
// with the same working directory, or a create of it still in flight — since
// continuing picks up the latest one there. A fresh restart is accepted either
// way, and a failed create is no session.
func TestRestartCannotContinueAConversationItShares(t *testing.T) {
	stopped := func(id, agent string) domain.Session {
		return domain.Session{ID: id, Title: id, Agent: agent, State: domain.SessionStopped}
	}
	tests := []struct {
		name         string
		sameWS       []domain.Session
		otherWS      []domain.Session
		creating     string
		failedCreate bool
		// recorded records the option sess-a was started as, which a provider that
		// builds one option's schema alone looks up without listing them all.
		recorded bool
		shared   bool
	}{
		{name: "alone"},
		{name: "another option beside it", sameWS: []domain.Session{stopped("sess-b", "codex")}},
		{name: "the same option beside it", sameWS: []domain.Session{stopped("sess-b", "claude")}, shared: true},
		{name: "the same option in another project's workstream there", otherWS: []domain.Session{stopped("sess-b", "claude")}, shared: true},
		// With no recorded option, a neighbour is the option its agent names, by ID or
		// label, as the session itself is.
		{name: "the same option named by its label beside it", sameWS: []domain.Session{stopped("sess-b", "Claude Code")}, shared: true},
		{name: "the same option named by its label beside a recorded one", sameWS: []domain.Session{stopped("sess-b", "Claude Code")}, recorded: true, shared: true},
		// The placeholder is labelled "Claude Code", not by the option's ID.
		{name: "a create of the same option in flight", creating: "claude", shared: true},
		{name: "a create of another option in flight", creating: "codex"},
		{name: "a failed create of the same option", failedCreate: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			st := store.NewTestStore(t, []domain.Project{
				{ID: "p1", Name: "p1", Path: dir, Workstreams: []domain.Workstream{{ID: "ws1", Name: "ws", Provider: "fake", WorkingDir: dir}}},
				{ID: "p2", Name: "p2", Path: dir, Workstreams: []domain.Workstream{{ID: "ws2", Name: "ws", Provider: "fake", WorkingDir: dir}}},
			})
			held := &heldCreateFake{Provider: fake.New(fake.Config{Seed: map[string][]domain.Workstream{
				"p1": {{WorkingDir: dir, Sessions: append([]domain.Session{stopped("sess-a", "Claude Code")}, tt.sameWS...)}},
				"p2": {{WorkingDir: dir, Sessions: tt.otherWS}},
			}}), release: make(chan struct{})}
			var sessionProvider provider.SessionProvider = held
			if tt.recorded {
				sessionProvider = schemaBuildingFake{held}
			}
			reg := provider.NewRegistry()
			if err := reg.RegisterSessionProvider(sessionProvider); err != nil {
				t.Fatalf("RegisterSessionProvider: %v", err)
			}
			svc := provider.NewService(st, reg, 0)
			svc.Reconcile(context.Background())
			if tt.recorded {
				if err := st.SetLaunchConfig("p1", "sess-a", domain.LaunchConfig{ProviderID: "fake", OptionID: "claude", Config: json.RawMessage(`{}`)}); err != nil {
					t.Fatalf("SetLaunchConfig: %v", err)
				}
			}
			if tt.creating != "" {
				placeholder, err := svc.CreateSession(context.Background(), provider.CreateSessionInput{
					ProjectID: "p1", WorkstreamID: "ws1", ProviderID: "fake", OptionID: tt.creating, Config: json.RawMessage(`{}`),
				})
				if err != nil {
					t.Fatalf("CreateSession: %v", err)
				}
				defer awaitCreateLanded(t, st, placeholder.ID)
				defer close(held.release)
			}
			if tt.failedCreate {
				failed := domain.Session{ID: provider.StartingIDPrefix + "x", Title: "x", Agent: "claude", Provider: "fake", State: domain.SessionError}
				if err := st.AddEphemeralSession("p1", "ws1", failed); err != nil {
					t.Fatalf("AddEphemeralSession: %v", err)
				}
			}
			ref := provider.SessionRef{ProjectID: "p1", WorkstreamID: "ws1", SessionID: "sess-a"}

			launch, err := svc.SessionLaunchConfig(context.Background(), ref)
			if err != nil {
				t.Fatalf("SessionLaunchConfig: %v", err)
			}
			if got := launch.ContinueRefusal != ""; got != tt.shared {
				t.Errorf("continueRefusal = %q; want one: %v", launch.ContinueRefusal, tt.shared)
			}
			_, err = svc.RestartSession(context.Background(), provider.RestartSessionInput{Session: ref})
			if got := errors.Is(err, provider.ErrCannotContinue); got != tt.shared {
				t.Fatalf("RestartSession = %v; want ErrCannotContinue: %v", err, tt.shared)
			}
			if !tt.shared {
				return
			}
			if err.Error() != launch.ContinueRefusal {
				t.Errorf("refusal %q; want the launch config's, %q", err, launch.ContinueRefusal)
			}
			if _, err := svc.RestartSession(context.Background(), provider.RestartSessionInput{Session: ref, Fresh: true}); err != nil {
				t.Errorf("fresh RestartSession: %v", err)
			}
		})
	}
}

// heldCreateFake is the fake with session creates that wait to be let go.
type heldCreateFake struct {
	*fake.Provider
	release chan struct{}
}

func (held *heldCreateFake) CreateSession(ctx context.Context, req provider.CreateSessionRequest) (domain.Session, error) {
	<-held.release
	return held.Provider.CreateSession(ctx, req)
}

// schemaBuildingFake is heldCreateFake building one option's schema alone, as
// Enclave does.
type schemaBuildingFake struct {
	*heldCreateFake
}

func (building schemaBuildingFake) SessionSchema(ctx context.Context, ref provider.WorkstreamRef, optionID string) (json.RawMessage, error) {
	options, err := building.SessionSchemas(ctx, ref)
	if err != nil {
		return nil, err
	}
	for _, option := range options {
		if option.ID == optionID {
			return option.Schema, nil
		}
	}
	return nil, provider.ErrOptionNotOffered
}

// awaitCreateLanded waits until the session a create's placeholder stands for has
// landed in workstream ws1 of project p1, where
// TestRestartCannotContinueAConversationItShares creates it.
func awaitCreateLanded(t *testing.T, st *store.Store, placeholderID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		workstream, err := st.Workstream("p1", "ws1")
		if err != nil {
			t.Fatalf("Workstream: %v", err)
		}
		if slices.ContainsFunc(workstream.Sessions, func(session domain.Session) bool { return session.PlaceholderID == placeholderID }) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the create to land")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// instrumentedFake is the fake with hooks a test acts through: a removal that
// waits to be let go, a restart that cannot stop the session, and a count of the
// option listings it was asked for.
type instrumentedFake struct {
	*fake.Provider
	removalStarted chan struct{}
	finishRemoval  chan struct{}
	stopFailure    error
	removalFailure error
	schemaFailure  error
	// sandboxedSchema, when set, replaces the sandboxed option's schema.
	sandboxedSchema json.RawMessage
	schemaListings  atomic.Int32
	unavailable     atomic.Bool
}

func (instrumented *instrumentedFake) Available(ctx context.Context) (bool, error) {
	if instrumented.unavailable.Load() {
		return false, nil
	}
	return instrumented.Provider.Available(ctx)
}

func (instrumented *instrumentedFake) RemoveSession(ctx context.Context, ref provider.SessionRef) error {
	if instrumented.finishRemoval != nil {
		close(instrumented.removalStarted)
		<-instrumented.finishRemoval
	}
	if instrumented.removalFailure != nil {
		return instrumented.removalFailure
	}
	return instrumented.Provider.RemoveSession(ctx, ref)
}

func (instrumented *instrumentedFake) SessionSchemas(ctx context.Context, ref provider.WorkstreamRef) ([]provider.SessionSchemaOption, error) {
	instrumented.schemaListings.Add(1)
	if instrumented.schemaFailure != nil {
		return nil, instrumented.schemaFailure
	}
	options, err := instrumented.Provider.SessionSchemas(ctx, ref)
	for i := range options {
		if options[i].ID == "sandboxed" && instrumented.sandboxedSchema != nil {
			options[i].Schema = instrumented.sandboxedSchema
		}
	}
	return options, err
}

func (instrumented *instrumentedFake) PrepareRestart(ctx context.Context, req provider.RestartSessionRequest) (provider.RestartFunc, error) {
	if instrumented.stopFailure != nil {
		return func(context.Context) error { return instrumented.stopFailure }, nil
	}
	return instrumented.Provider.PrepareRestart(ctx, req)
}

// newInstrumentedService is a service over one project whose session provider is
// an instrumentedFake.
func newInstrumentedService(t *testing.T) (*provider.Service, *store.Store, *instrumentedFake) {
	t.Helper()
	instrumented := &instrumentedFake{Provider: fake.New(fake.Config{})}
	svc, st := newServiceWith(t, instrumented.Provider, instrumented)
	return svc, st, instrumented
}

// newServiceWith is a service over one project whose workstreams are the fake's,
// and whose sessions are sessionProvider's.
func newServiceWith(t *testing.T, fakeProvider *fake.Provider, sessionProvider provider.SessionProvider) (*provider.Service, *store.Store) {
	t.Helper()
	st := store.NewTestStore(t, []domain.Project{{ID: "p1", Name: "p1", Path: t.TempDir(), Workstreams: []domain.Workstream{}}})
	reg := provider.NewRegistry()
	if err := reg.RegisterWorkstreamProvider(fakeProvider); err != nil {
		t.Fatalf("register fake workstream provider: %v", err)
	}
	if err := reg.RegisterSessionProvider(sessionProvider); err != nil {
		t.Fatalf("register session provider: %v", err)
	}
	svc := provider.NewService(st, reg, 0)
	svc.Reconcile(context.Background())
	return svc, st
}

// singleSchemaFake is an instrumentedFake that builds one option's schema alone,
// counting the builds, and runs beforeBuild, if set, as it does.
type singleSchemaFake struct {
	*instrumentedFake
	schemaBuilds atomic.Int32
	beforeBuild  func()
}

func (single *singleSchemaFake) SessionSchema(ctx context.Context, ref provider.WorkstreamRef, optionID string) (json.RawMessage, error) {
	single.schemaBuilds.Add(1)
	if single.beforeBuild != nil {
		single.beforeBuild()
	}
	if single.schemaFailure != nil {
		return nil, single.schemaFailure
	}
	options, err := single.Provider.SessionSchemas(ctx, ref)
	for _, option := range options {
		if option.ID == optionID {
			return option.Schema, err
		}
	}
	return nil, err
}

func newSingleSchemaService(t *testing.T) (*provider.Service, *store.Store, *singleSchemaFake) {
	t.Helper()
	single := &singleSchemaFake{instrumentedFake: &instrumentedFake{Provider: fake.New(fake.Config{})}}
	svc, st := newServiceWith(t, single.Provider, single)
	return svc, st, single
}

// A restart of a session started as a recorded option validates against that
// option alone when its provider can build one: listing them all can mean asking
// about every one.
func TestRestartOfARecordedSessionBuildsOnlyItsOption(t *testing.T) {
	svc, st, single := newSingleSchemaService(t)
	ref := sandboxedSession(t, svc, st, `{"model":"opus"}`)
	awaitLaunchConfig(t, svc, ref)
	listings, builds := single.schemaListings.Load(), single.schemaBuilds.Load()

	_, err := svc.RestartSession(context.Background(), provider.RestartSessionInput{
		Session: ref, Config: json.RawMessage(`{"model":"gpt-2"}`),
	})
	var invalid *provider.ValidationError
	if !errors.As(err, &invalid) {
		t.Errorf("RestartSession with an invalid config = %v; want a ValidationError", err)
	}
	restartOnceIdle(t, svc, provider.RestartSessionInput{Session: ref, Config: json.RawMessage(`{"model":"sonnet"}`)})
	if more := single.schemaListings.Load() - listings; more != 0 {
		t.Errorf("listed the provider's options %d times; want none", more)
	}
	if more := single.schemaBuilds.Load() - builds; more != 2 {
		t.Errorf("built the option's schema %d times; want once per restart", more)
	}
}

// A session removed while its restart was being validated is not restarted: the
// restart was registered after the removal could cancel it.
func TestRestartSessionOfASessionRemovedMeanwhile(t *testing.T) {
	svc, st, single := newSingleSchemaService(t)
	ref := sandboxedSession(t, svc, st, `{"model":"opus"}`)
	awaitLaunchConfig(t, svc, ref)
	single.beforeBuild = func() {
		if err := st.RemoveSession(ref.ProjectID, ref.WorkstreamID, ref.SessionID); err != nil {
			t.Errorf("RemoveSession: %v", err)
		}
	}

	if _, err := svc.RestartSession(context.Background(), provider.RestartSessionInput{Session: ref}); !errors.Is(err, provider.ErrSessionNotFound) {
		t.Errorf("RestartSession = %v; want ErrSessionNotFound", err)
	}
	if _, ok := st.LaunchConfig(ref.SessionID); ok {
		t.Error("the removed session's launch config was recorded again")
	}
}

// A provider that is not available refuses the restart up front, as it refuses a
// create, rather than failing it in the background.
func TestRestartSessionOnAnUnavailableProvider(t *testing.T) {
	svc, st, instrumented := newInstrumentedService(t)
	ref := sandboxedSession(t, svc, st, `{}`)
	instrumented.unavailable.Store(true)

	if _, err := svc.RestartSession(context.Background(), provider.RestartSessionInput{Session: ref}); !errors.Is(err, provider.ErrProviderUnavailable) {
		t.Errorf("RestartSession = %v; want ErrProviderUnavailable", err)
	}
}

// nonRestarter is the fake without its SessionRestarter.
type nonRestarter struct{ provider.SessionProvider }

func TestRestartSessionOnAProviderThatCannotRestart(t *testing.T) {
	fakeProvider := fake.New(fake.Config{})
	svc, st := newServiceWith(t, fakeProvider, nonRestarter{fakeProvider})
	ref := sandboxedSession(t, svc, st, `{}`)

	if _, err := svc.SessionLaunchConfig(context.Background(), ref); !errors.Is(err, provider.ErrCapabilityUnsupported) {
		t.Errorf("SessionLaunchConfig = %v; want ErrCapabilityUnsupported", err)
	}
	if _, err := svc.RestartSession(context.Background(), provider.RestartSessionInput{Session: ref}); !errors.Is(err, provider.ErrCapabilityUnsupported) {
		t.Errorf("RestartSession = %v; want ErrCapabilityUnsupported", err)
	}
}

// A create's explicit null config is no config: a bare restart of the session has
// nothing to replay, rather than a null to validate.
func TestCreateWithANullConfigRecordsNone(t *testing.T) {
	svc, st := newService(t)
	ref := sandboxedSession(t, svc, st, `null`)

	launch, err := svc.SessionLaunchConfig(context.Background(), ref)
	if err != nil {
		t.Fatalf("SessionLaunchConfig: %v", err)
	}
	if launch.OptionID != "sandboxed" || launch.Config != nil {
		t.Errorf("launch config = %+v (config %s); want sandboxed with no config", launch, launch.Config)
	}
	if _, err := svc.RestartSession(context.Background(), provider.RestartSessionInput{Session: ref}); err != nil {
		t.Errorf("bare RestartSession = %v; want it accepted", err)
	}
}

// A restart asked for while the session is being removed is refused: the removal
// already cancelled the restarts in flight, and one registered after that would
// bring the session back once the removal is done.
func TestRestartSessionIsRefusedWhileTheSessionIsRemoved(t *testing.T) {
	svc, st, instrumented := newInstrumentedService(t)
	ref := sandboxedSession(t, svc, st, `{}`)
	instrumented.removalStarted = make(chan struct{})
	instrumented.finishRemoval = make(chan struct{})

	removed := make(chan error, 1)
	go func() { removed <- svc.RemoveSession(context.Background(), ref) }()
	<-instrumented.removalStarted
	_, err := svc.RestartSession(context.Background(), provider.RestartSessionInput{Session: ref})
	close(instrumented.finishRemoval)
	if err := <-removed; err != nil {
		t.Fatalf("RemoveSession: %v", err)
	}
	if !errors.Is(err, provider.ErrSessionNotFound) {
		t.Errorf("RestartSession during the removal = %v; want ErrSessionNotFound", err)
	}
}

// Reading back what a session was started with builds only its recorded option's
// schema, where the provider can; listing the options is for a session with no
// record, or a provider that cannot build one alone.
func TestSessionLaunchConfigOfARecordedSessionBuildsOnlyItsOption(t *testing.T) {
	svc, st, single := newSingleSchemaService(t)
	ref := sandboxedSession(t, svc, st, `{"model":"opus"}`)
	awaitLaunchConfig(t, svc, ref)

	listings, builds := single.schemaListings.Load(), single.schemaBuilds.Load()
	launch, err := svc.SessionLaunchConfig(context.Background(), ref)
	if err != nil {
		t.Fatalf("SessionLaunchConfig: %v", err)
	}
	if launch.OptionID != "sandboxed" || string(launch.Config) != `{"model":"opus"}` {
		t.Errorf("launch config = %+v; want the recorded one", launch)
	}
	if !strings.Contains(string(launch.Schema), `"enclave"`) {
		t.Errorf("schema = %s; want the sandboxed option's", launch.Schema)
	}
	if more := single.schemaListings.Load() - listings; more != 0 {
		t.Errorf("listed the provider's options %d times; want none", more)
	}
	if more := single.schemaBuilds.Load() - builds; more != 1 {
		t.Errorf("built the option's schema %d times; want once", more)
	}
}

// A session whose option its provider no longer offers is refused, by the form
// and by a restart alike, rather than offered a form with no fields.
func TestRestartOfAnOptionNoLongerOffered(t *testing.T) {
	svc, st := newService(t)
	ref := sandboxedSession(t, svc, st, `{"model":"opus"}`)
	awaitLaunchConfig(t, svc, ref)
	if err := st.SetLaunchConfig(ref.ProjectID, ref.SessionID, domain.LaunchConfig{ProviderID: "fake", OptionID: "retired"}); err != nil {
		t.Fatalf("SetLaunchConfig: %v", err)
	}

	if _, err := svc.SessionLaunchConfig(context.Background(), ref); !errors.Is(err, provider.ErrOptionNotOffered) {
		t.Errorf("SessionLaunchConfig = %v; want ErrOptionNotOffered", err)
	}
	if _, err := svc.RestartSession(context.Background(), provider.RestartSessionInput{Session: ref}); !errors.Is(err, provider.ErrOptionNotOffered) {
		t.Errorf("RestartSession = %v; want ErrOptionNotOffered", err)
	}
}

// A restart that could not take the session down leaves it as it was: the reason
// goes on its workstream, and the session is still recorded as started with what
// it runs with. A later restart clears the reason.
func TestRestartThatLeftTheSessionAsItWasReportsOnTheWorkstream(t *testing.T) {
	svc, st, instrumented := newInstrumentedService(t)
	original := `{"model":"opus"}`
	ref := sandboxedSession(t, svc, st, original)
	awaitLaunchConfig(t, svc, ref)
	instrumented.stopFailure = errors.New("docker is not running")

	if _, err := svc.RestartSession(context.Background(), provider.RestartSessionInput{
		Session: ref, Config: json.RawMessage(`{"model":"sonnet"}`),
	}); err != nil {
		t.Fatalf("RestartSession: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		ws, _ := st.Workstream(ref.ProjectID, ref.WorkstreamID)
		if strings.Contains(ws.StatusMessage, "docker is not running") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("status message = %q; want the restart's failure", ws.StatusMessage)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if launch, _ := st.LaunchConfig(ref.SessionID); string(launch.Config) != original {
		t.Errorf("launch config = %s; want what the session still runs with, %s", launch.Config, original)
	}

	instrumented.stopFailure = nil
	restartOnceIdle(t, svc, provider.RestartSessionInput{Session: ref})
	if ws, _ := st.Workstream(ref.ProjectID, ref.WorkstreamID); ws.StatusMessage != "" {
		t.Errorf("status message after a later restart = %q; want it cleared", ws.StatusMessage)
	}
}

// A config is checked against the option it is for, so one that names no option
// is refused rather than started unchecked as whatever the provider defaults to.
func TestConfigWithoutAnOptionIsRefused(t *testing.T) {
	svc, st := newService(t)
	wref := createWS(t, svc, st)
	_, err := svc.CreateSession(context.Background(), provider.CreateSessionInput{
		ProjectID: wref.ProjectID, WorkstreamID: wref.WorkstreamID, ProviderID: "fake",
		Config: json.RawMessage(`{"enclave":{"allowDomains":["--privileged"]}}`),
	})
	var invalid *provider.ValidationError
	if !errors.As(err, &invalid) {
		t.Errorf("CreateSession without an option = %v; want a ValidationError", err)
	}

	// A session whose option cannot be told is refused a config the same way.
	dir := t.TempDir()
	projectStore := store.NewTestStore(t, []domain.Project{{ID: "p1", Name: "p1", Path: dir, Workstreams: []domain.Workstream{
		{ID: "ws1", Name: "ws", Provider: "fake", WorkingDir: dir},
	}}})
	registry := provider.NewRegistry()
	registerFake(t, registry, fake.Config{Seed: map[string][]domain.Workstream{"p1": {{WorkingDir: dir, Sessions: []domain.Session{
		{ID: "sess-agentless", Title: "Agentless", State: domain.SessionRunning},
	}}}}})
	service := provider.NewService(projectStore, registry, 0)
	service.Reconcile(context.Background())
	_, err = service.RestartSession(context.Background(), provider.RestartSessionInput{
		Session: provider.SessionRef{ProjectID: "p1", WorkstreamID: "ws1", SessionID: "sess-agentless"},
		Config:  json.RawMessage(`{"model":"opus"}`),
	})
	if !errors.As(err, &invalid) {
		t.Errorf("RestartSession of a session with no option = %v; want a ValidationError", err)
	}
	// An empty config, as a form with no fields sends, sets nothing.
	if _, err := service.RestartSession(context.Background(), provider.RestartSessionInput{
		Session: provider.SessionRef{ProjectID: "p1", WorkstreamID: "ws1", SessionID: "sess-agentless"},
		Config:  json.RawMessage(`{}`),
	}); err != nil {
		t.Errorf("RestartSession of a session with no option, with {} = %v; want it restarted", err)
	}
}

// A provider that cannot report a session's settings refuses the form and every
// restart, rather than restart the session on defaults and record those in place
// of its settings.
func TestRestartWhileTheProviderCannotReportTheSettings(t *testing.T) {
	tests := []struct {
		name  string
		setUp func(t *testing.T) (*provider.Service, *store.Store, *instrumentedFake)
	}{
		{name: "listing the options", setUp: newInstrumentedService},
		{name: "building the option alone", setUp: func(t *testing.T) (*provider.Service, *store.Store, *instrumentedFake) {
			svc, st, single := newSingleSchemaService(t)
			return svc, st, single.instrumentedFake
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, st, instrumented := tt.setUp(t)
			original := `{"model":"opus","enclave":{"hostConfig":true}}`
			ref := sandboxedSession(t, svc, st, original)
			awaitLaunchConfig(t, svc, ref)
			instrumented.schemaFailure = errors.New("enclave config: exit status 1")

			if _, err := svc.SessionLaunchConfig(context.Background(), ref); !errors.Is(err, provider.ErrProviderUnavailable) {
				t.Errorf("SessionLaunchConfig = %v; want ErrProviderUnavailable", err)
			}
			for _, config := range []json.RawMessage{nil, json.RawMessage(`{}`)} {
				if _, err := svc.RestartSession(context.Background(), provider.RestartSessionInput{Session: ref, Config: config}); !errors.Is(err, provider.ErrProviderUnavailable) {
					t.Errorf("RestartSession(%s) = %v; want ErrProviderUnavailable", config, err)
				}
			}
			if launch, _ := st.LaunchConfig(ref.SessionID); string(launch.Config) != original {
				t.Errorf("launch config = %s; want it kept, %s", launch.Config, original)
			}

			// A provider that says it is unavailable is not said to be twice.
			instrumented.schemaFailure = fmt.Errorf("%w: enclave tools: exit status 1", provider.ErrProviderUnavailable)
			if _, err := svc.SessionLaunchConfig(context.Background(), ref); err == nil || strings.Count(err.Error(), provider.ErrProviderUnavailable.Error()) != 1 {
				t.Errorf("SessionLaunchConfig = %v; want it unavailable, said once", err)
			}
		})
	}
}

// Only what differs from the option's defaults is recorded, on create and on
// restart, so a default the provider changes later is not pinned.
func TestLaunchConfigsAreRecordedWithoutDefaults(t *testing.T) {
	svc, st := newService(t)
	full := func(domain string) string {
		return `{"model":"sonnet","enclave":{"features":["node-dev","devtools"],"dirs":[{"path":"/x","readonly":true}],"ports":[],"bridgePorts":[],"allowDomains":["` + domain + `"],"hostConfig":false}}`
	}
	want := func(domain string) string {
		return `{"model":"sonnet","enclave":{"dirs":[{"path":"/x","readonly":true}],"allowDomains":["` + domain + `"]}}`
	}
	ref := sandboxedSession(t, svc, st, full("a.example.com"))
	if launch := awaitLaunchConfig(t, svc, ref); !sameJSON(launch.Config, want("a.example.com")) {
		t.Errorf("launch config after create = %s; want %s", launch.Config, want("a.example.com"))
	}
	restartOnceIdle(t, svc, provider.RestartSessionInput{Session: ref, Config: json.RawMessage(full("b.example.com"))})
	if launch, _ := st.LaunchConfig(ref.SessionID); !sameJSON(launch.Config, want("b.example.com")) {
		t.Errorf("launch config after restart = %s; want %s", launch.Config, want("b.example.com"))
	}
}

// A bare restart replays the record as it is: a setting recorded because it
// differed from the default is kept when the default later becomes that value.
func TestBareRestartKeepsTheRecordWhenADefaultMoves(t *testing.T) {
	svc, st, instrumented := newInstrumentedService(t)
	original := `{"model":"opus","enclave":{"hostConfig":true}}`
	ref := sandboxedSession(t, svc, st, original)
	awaitLaunchConfig(t, svc, ref)
	instrumented.sandboxedSchema = json.RawMessage(`{"type":"object","properties":{"model":{"type":"string"},"enclave":{"type":"object","properties":{"hostConfig":{"type":"boolean","default":true}}}}}`)

	restartOnceIdle(t, svc, provider.RestartSessionInput{Session: ref})
	if launch, _ := st.LaunchConfig(ref.SessionID); !sameJSON(launch.Config, original) {
		t.Errorf("launch config after a bare restart = %s; want the record kept, %s", launch.Config, original)
	}
	// So does a restart submitting the recorded choice unchanged.
	restartOnceIdle(t, svc, provider.RestartSessionInput{Session: ref, Config: json.RawMessage(original)})
	if launch, _ := st.LaunchConfig(ref.SessionID); !sameJSON(launch.Config, original) {
		t.Errorf("launch config after an unchanged restart = %s; want the choice kept, %s", launch.Config, original)
	}
}

// A restart a removal cancelled did not happen, so when the removal fails too the
// session is still recorded as started with what it was before.
func TestRestartCancelledByAFailedRemovalKeepsThePreviousRecord(t *testing.T) {
	instrumented := &instrumentedFake{Provider: fake.New(fake.Config{SessionStartup: 300 * time.Millisecond})}
	svc, st := newServiceWith(t, instrumented.Provider, instrumented)
	original := `{"model":"opus"}`
	ref := sandboxedSession(t, svc, st, original)
	awaitLaunchConfig(t, svc, ref)
	instrumented.removalFailure = errors.New("docker is not running")

	if _, err := svc.RestartSession(context.Background(), provider.RestartSessionInput{
		Session: ref, Config: json.RawMessage(`{"model":"sonnet"}`),
	}); err != nil {
		t.Fatalf("RestartSession: %v", err)
	}
	if err := svc.RemoveSession(context.Background(), ref); err == nil {
		t.Fatal("RemoveSession: want the removal's failure")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		launch, _ := st.LaunchConfig(ref.SessionID)
		if string(launch.Config) == original {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("launch config = %s; want the one from before the restart, %s", launch.Config, original)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func sameJSON(got json.RawMessage, want string) bool {
	var gotValue, wantValue any
	return json.Unmarshal(got, &gotValue) == nil && json.Unmarshal([]byte(want), &wantValue) == nil && reflect.DeepEqual(gotValue, wantValue)
}

// refusingRestarter is the fake whose CheckRestart refuses one option.
type refusingRestarter struct {
	*fake.Provider
	refused string
}

func (refusing refusingRestarter) CheckRestart(ref provider.SessionRef, optionID string) error {
	if optionID == refusing.refused {
		return provider.ErrSessionNotRestartable
	}
	return nil
}

// startingReporter is the fake reporting its sessions as starting, as a provider
// does for a session whose agent is still coming up.
type startingReporter struct{ *fake.Provider }

func (starting startingReporter) CreateSession(ctx context.Context, req provider.CreateSessionRequest) (domain.Session, error) {
	session, err := starting.Provider.CreateSession(ctx, req)
	session.State = domain.SessionStarting
	return session, err
}

func (starting startingReporter) DiscoverSessions(ctx context.Context, project provider.ProjectRef) ([]provider.DiscoveredSession, error) {
	discovered, err := starting.Provider.DiscoverSessions(ctx, project)
	for i := range discovered {
		discovered[i].Session.State = domain.SessionStarting
	}
	return discovered, err
}

// A session is marked restartable only when the daemon can tell, without asking
// its provider anything costly, that a restart would not be refused outright.
func TestSessionsAreMarkedRestartable(t *testing.T) {
	// landed creates a session through wrap(fake), or the fake itself if wrap is nil.
	landed := func(wrap func(*fake.Provider) provider.SessionProvider) func(t *testing.T) (*provider.Service, *store.Store, provider.SessionRef) {
		return func(t *testing.T) (*provider.Service, *store.Store, provider.SessionRef) {
			fakeProvider := fake.New(fake.Config{})
			var sessionProvider provider.SessionProvider = fakeProvider
			if wrap != nil {
				sessionProvider = wrap(fakeProvider)
			}
			svc, st := newServiceWith(t, fakeProvider, sessionProvider)
			return svc, st, sandboxedSession(t, svc, st, `{}`)
		}
	}
	placeholder := func(option string, state domain.SessionState) func(t *testing.T) (*provider.Service, *store.Store, provider.SessionRef) {
		return func(t *testing.T) (*provider.Service, *store.Store, provider.SessionRef) {
			svc, st := newSlowRestartService(t)
			return svc, st, placeholderOf(t, svc, st, option, state)
		}
	}
	tests := []struct {
		name  string
		setup func(t *testing.T) (*provider.Service, *store.Store, provider.SessionRef)
		want  bool
	}{
		{name: "a session", setup: landed(nil), want: true},
		{name: "a starting placeholder", setup: placeholder("sandboxed", domain.SessionStarting)},
		{name: "a failed create", setup: placeholder("no-such-agent", domain.SessionError)},
		{name: "a starting session", setup: landed(func(fakeProvider *fake.Provider) provider.SessionProvider { return startingReporter{fakeProvider} })},
		{name: "a provider that cannot restart", setup: landed(func(fakeProvider *fake.Provider) provider.SessionProvider { return nonRestarter{fakeProvider} })},
		{name: "a provider refusing the recorded option", setup: landed(func(fakeProvider *fake.Provider) provider.SessionProvider {
			return refusingRestarter{fakeProvider, "sandboxed"}
		})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, st, ref := tt.setup(t)
			if got := storedSession(t, st, ref).Restartable; got != tt.want {
				t.Errorf("restartable as created = %v; want %v", got, tt.want)
			}
			svc.Reconcile(context.Background())
			if got := storedSession(t, st, ref).Restartable; got != tt.want {
				t.Errorf("restartable after a reconcile = %v; want %v", got, tt.want)
			}
			// What is marked so is what the daemon accepts, through the API or CLI too.
			if _, err := svc.SessionLaunchConfig(context.Background(), ref); (err == nil) != tt.want {
				t.Errorf("SessionLaunchConfig = %v; want accepted %v", err, tt.want)
			}
			if _, err := svc.RestartSession(context.Background(), provider.RestartSessionInput{Session: ref}); (err == nil) != tt.want {
				t.Errorf("RestartSession = %v; want accepted %v", err, tt.want)
			}
		})
	}
}

// A session is not restartable while a restart of it is in flight, and is again
// once the restart has brought it back.
func TestARestartingSessionIsNotRestartable(t *testing.T) {
	fakeProvider := fake.New(fake.Config{SessionStartup: 300 * time.Millisecond})
	svc, st := newServiceWith(t, fakeProvider, fakeProvider)
	ref := sandboxedSession(t, svc, st, `{}`)

	restarting, err := svc.RestartSession(context.Background(), provider.RestartSessionInput{Session: ref})
	if err != nil {
		t.Fatalf("RestartSession: %v", err)
	}
	if restarting.Restartable {
		t.Error("the restarting session RestartSession returned is marked restartable")
	}
	svc.Reconcile(context.Background())
	if storedSession(t, st, ref).Restartable {
		t.Error("a session restarting is marked restartable after a reconcile")
	}

	for deadline := time.Now().Add(5 * time.Second); storedSession(t, st, ref).State != domain.SessionRunning; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the restart to bring the session back")
		}
	}
	if !storedSession(t, st, ref).Restartable {
		t.Error("the restarted session is not marked restartable")
	}
}

// A restart marks its session restartable again in every project listing it: one
// sharing the directory, reconciled while the session restarted, has it marked
// as not restartable until the restart ends.
func TestARestartedSessionIsRestartableInEveryProjectListingIt(t *testing.T) {
	fakeProvider := fake.New(fake.Config{SessionStartup: 300 * time.Millisecond})
	svc, st := newServiceWith(t, fakeProvider, fakeProvider)
	restarted := sandboxedSession(t, svc, st, `{}`)
	if err := st.AddProject(&domain.Project{ID: "p2", Name: "p2", Path: filepath.Dir(st.Projects()[0].Path)}); err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	svc.Reconcile(context.Background())
	listing := slices.IndexFunc(st.Projects()[1].Workstreams, func(workstream domain.Workstream) bool {
		_, ok := findStoredSession(workstream, restarted.SessionID)
		return ok
	})
	if listing < 0 {
		t.Fatalf("the other project does not list the session: %+v", st.Projects()[1])
	}
	elsewhere := provider.SessionRef{ProjectID: "p2", WorkstreamID: st.Projects()[1].Workstreams[listing].ID, SessionID: restarted.SessionID}

	if _, err := svc.RestartSession(context.Background(), provider.RestartSessionInput{Session: restarted}); err != nil {
		t.Fatalf("RestartSession: %v", err)
	}
	svc.Reconcile(context.Background())
	if storedSession(t, st, elsewhere).Restartable {
		t.Fatal("the other project marks the session restartable while it restarts")
	}
	for deadline := time.Now().Add(5 * time.Second); !storedSession(t, st, elsewhere).Restartable; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the other project still marks the restarted session as not restartable")
		}
	}
}

// restartStartingFake starts its stopped sessions as Enclave does: by a restart.
type restartStartingFake struct{ *fake.Provider }

func (restartStartingFake) StartSession(context.Context, provider.SessionRef) error {
	return provider.ErrStartIsRestart
}

// A start that is a restart runs in the background like one: cancelling the
// request does not interrupt it, and a restart meanwhile is refused.
func TestStartByRestartOutlivesItsRequest(t *testing.T) {
	inner := fake.New(fake.Config{SessionStartup: 300 * time.Millisecond})
	svc, st := newServiceWith(t, inner, restartStartingFake{inner})
	ref := sandboxedSession(t, svc, st, `{}`)
	awaitLaunchConfig(t, svc, ref)
	if err := svc.StopSession(context.Background(), ref); err != nil {
		t.Fatalf("StopSession: %v", err)
	}

	request, cancel := context.WithCancel(context.Background())
	if err := svc.StartSession(request, ref); err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	cancel()
	if _, err := svc.RestartSession(context.Background(), provider.RestartSessionInput{Session: ref}); !errors.Is(err, provider.ErrSessionStarting) {
		t.Errorf("RestartSession during the start = %v; want ErrSessionStarting", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for storedSession(t, st, ref).State != domain.SessionRunning {
		if time.Now().After(deadline) {
			t.Fatalf("session after a cancelled start request = %s; want running", storedSession(t, st, ref).State)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A rejected Start must guide the user to the safe restart path without silently
// switching conversations or converting the request to a fresh restart.
func TestStartByRestartExplainsAmbiguousConversation(t *testing.T) {
	dir := t.TempDir()
	inner := fake.New(fake.Config{Seed: map[string][]domain.Workstream{"p1": {{WorkingDir: dir, Sessions: []domain.Session{
		{ID: "a", Agent: "claude", State: domain.SessionStopped},
		{ID: "b", Agent: "claude", State: domain.SessionStopped},
	}}}}})
	st := store.NewTestStore(t, []domain.Project{{ID: "p1", Path: dir, Workstreams: []domain.Workstream{{ID: "ws1", WorkingDir: dir}}}})
	reg := provider.NewRegistry()
	if err := reg.RegisterSessionProvider(restartStartingFake{inner}); err != nil {
		t.Fatal(err)
	}
	svc := provider.NewService(st, reg, 0)
	svc.Reconcile(context.Background())
	ref := provider.SessionRef{ProjectID: "p1", WorkstreamID: "ws1", SessionID: "a"}
	err := svc.StartSession(context.Background(), ref)
	if !errors.Is(err, provider.ErrCannotContinue) || !strings.Contains(err.Error(), "Restart with settings") || !strings.Contains(err.Error(), "fresh conversation") {
		t.Fatalf("StartSession refusal = %v; want actionable settings guidance", err)
	}
	if got := storedSession(t, st, ref).State; got != domain.SessionStopped {
		t.Fatalf("refused start changed state to %s", got)
	}
}
