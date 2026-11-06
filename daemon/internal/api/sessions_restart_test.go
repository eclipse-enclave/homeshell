package api

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/notify"
	"github.com/eclipsesource/homeshell/daemon/internal/provider"
	"github.com/eclipsesource/homeshell/daemon/internal/provider/fake"
	"github.com/eclipsesource/homeshell/daemon/internal/store"
)

// awaitLaunchConfig polls the launch-config endpoint until it reports a config:
// the async create records it as the session lands, and until then the session
// reports none.
func awaitLaunchConfig(t *testing.T, h http.Handler, path string) provider.RestartSettings {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		rec := do(t, h, http.MethodGet, path, "")
		if rec.Code == http.StatusOK {
			var launch provider.RestartSettings
			if err := json.Unmarshal(rec.Body.Bytes(), &launch); err != nil {
				t.Fatalf("decode: %v (%s)", err, rec.Body.String())
			}
			if launch.Config != nil {
				return launch
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("launch config = %d (%s), want 200 with a config", rec.Code, rec.Body.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSessionRestartEndpoints(t *testing.T) {
	h, st := newProviderHandler(t)
	ws := createTestWorkstream(t, h)
	sess := createTestSession(t, h, st, ws.ID)
	base := "/api/projects/proj-test/workstreams/" + ws.ID + "/sessions/" + sess.ID

	launch := awaitLaunchConfig(t, h, base+"/launch-config")
	if launch.ProviderID != "fake" || launch.OptionID != "claude" || string(launch.Config) != `{"model":"opus"}` {
		t.Errorf("launch config = %+v; want what the session was created with", launch)
	}
	if !strings.Contains(string(launch.Schema), `"model"`) {
		t.Errorf("schema = %s; want the claude option's", launch.Schema)
	}

	rec := do(t, h, http.MethodPost, base+"/restart", `{"config":{"model":"haiku"},"fresh":true}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("restart status = %d (%s), want 202", rec.Code, rec.Body.String())
	}
	if launch := awaitLaunchConfig(t, h, base+"/launch-config"); string(launch.Config) != `{"model":"haiku"}` {
		t.Errorf("launch config after the restart = %s; want the new settings", launch.Config)
	}

	// Without a config — or without a body at all — the restart replays the
	// recorded one.
	for _, body := range []string{`{"fresh":true}`, ""} {
		deadline := time.Now().Add(5 * time.Second)
		for {
			rec = do(t, h, http.MethodPost, base+"/restart", body)
			if rec.Code == http.StatusAccepted {
				break
			}
			if rec.Code != http.StatusConflict || time.Now().After(deadline) {
				t.Fatalf("restart with body %q = %d (%s), want 202", body, rec.Code, rec.Body.String())
			}
			time.Sleep(10 * time.Millisecond)
		}
		if launch := awaitLaunchConfig(t, h, base+"/launch-config"); string(launch.Config) != `{"model":"haiku"}` {
			t.Errorf("launch config after a restart with body %q = %s; want the recorded settings", body, launch.Config)
		}
	}

	rec = do(t, h, http.MethodPost, base+"/restart", `{"config":{"model":"nope"}}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("restart with a bad config = %d (%s), want 400", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodPost, base+"/restart", `not json`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("restart with a body that is not JSON = %d, want 400", rec.Code)
	}

	gone := "/api/projects/proj-test/workstreams/" + ws.ID + "/sessions/sess-nope"
	if rec := do(t, h, http.MethodPost, gone+"/restart", `{}`); rec.Code != http.StatusNotFound {
		t.Errorf("restart of an unknown session = %d, want 404", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, gone+"/launch-config", ""); rec.Code != http.StatusNotFound {
		t.Errorf("launch config of an unknown session = %d, want 404", rec.Code)
	}
}

// A session HomeShell did not start is still known: its launch config is the
// option a restart would start it as, with a null config, rather than a 404.
func TestLaunchConfigOfAnAdoptedSession(t *testing.T) {
	dir := t.TempDir()
	projectStore := store.NewTestStore(t, []domain.Project{{ID: "proj-test", Name: "test", Path: dir, Workstreams: []domain.Workstream{
		{ID: "ws-1", Name: "ws", Provider: "fake", WorkingDir: dir},
	}}})
	registry := provider.NewRegistry()
	registerFakeBothRoles(t, registry, fake.Config{Seed: map[string][]domain.Workstream{"proj-test": {{WorkingDir: dir, Sessions: []domain.Session{
		{ID: "sess-adopted", Title: "Adopted", Agent: "Codex", State: domain.SessionRunning},
	}}}}})
	service := provider.NewService(projectStore, registry, 0)
	service.Reconcile(context.Background())
	handler := handlerForTest(Deps{Version: "test", Store: projectStore, Heartbeat: time.Minute, Providers: service, Notifications: notify.NewHub()})

	recorder := do(t, handler, http.MethodGet, "/api/projects/proj-test/workstreams/ws-1/sessions/sess-adopted/launch-config", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("launch config status = %d (%s), want 200", recorder.Code, recorder.Body.String())
	}
	var launch map[string]json.RawMessage
	if err := json.Unmarshal(recorder.Body.Bytes(), &launch); err != nil {
		t.Fatalf("decode: %v (%s)", err, recorder.Body.String())
	}
	if string(launch["providerId"]) != `"fake"` || string(launch["optionId"]) != `"codex"` || string(launch["config"]) != "null" {
		t.Errorf("launch config = %s; want fake/codex with a null config", recorder.Body.String())
	}
}

// A session that shares its directory with another of the same type cannot
// continue its conversation: the launch config says why, and only a fresh restart
// is accepted.
func TestSessionRestartEndpointsOfASharedConversation(t *testing.T) {
	dir := t.TempDir()
	projectStore := store.NewTestStore(t, []domain.Project{{ID: "proj-test", Name: "test", Path: dir, Workstreams: []domain.Workstream{
		{ID: "ws-1", Name: "ws", Provider: "fake", WorkingDir: dir},
	}}})
	registry := provider.NewRegistry()
	registerFakeBothRoles(t, registry, fake.Config{Seed: map[string][]domain.Workstream{"proj-test": {{WorkingDir: dir, Sessions: []domain.Session{
		{ID: "sess-a", Title: "A", Agent: "claude", State: domain.SessionRunning},
		{ID: "sess-b", Title: "B", Agent: "claude", State: domain.SessionStopped},
	}}}}})
	service := provider.NewService(projectStore, registry, 0)
	service.Reconcile(context.Background())
	handler := handlerForTest(Deps{Version: "test", Store: projectStore, Heartbeat: time.Minute, Providers: service, Notifications: notify.NewHub()})
	base := "/api/projects/proj-test/workstreams/ws-1/sessions/sess-a"

	var launch provider.RestartSettings
	recorder := do(t, handler, http.MethodGet, base+"/launch-config", "")
	if err := json.Unmarshal(recorder.Body.Bytes(), &launch); err != nil || recorder.Code != http.StatusOK || launch.ContinueRefusal == "" {
		t.Fatalf("launch config = %d (%s), want 200 with a continueRefusal", recorder.Code, recorder.Body.String())
	}
	var refusal struct {
		Error string `json:"error"`
	}
	recorder = do(t, handler, http.MethodPost, base+"/restart", `{"fresh":false}`)
	_ = json.Unmarshal(recorder.Body.Bytes(), &refusal)
	if recorder.Code != http.StatusConflict || refusal.Error != launch.ContinueRefusal {
		t.Errorf("restart = %d (%s), want 409 saying %q", recorder.Code, recorder.Body.String(), launch.ContinueRefusal)
	}
	if recorder := do(t, handler, http.MethodPost, base+"/restart", `{"fresh":true}`); recorder.Code != http.StatusAccepted {
		t.Errorf("fresh restart = %d (%s), want 202", recorder.Code, recorder.Body.String())
	}
}

// schemaFailingFake is the fake, as a session provider that cannot list its
// session options.
type schemaFailingFake struct{ *fake.Provider }

func (schemaFailingFake) SessionSchemas(context.Context, provider.WorkstreamRef) ([]provider.SessionSchemaOption, error) {
	return nil, errors.New("enclave config: exit status 1")
}

// A provider that cannot report a session's settings is unavailable to restart
// it with: the form is not opened without them, nor the session restarted on
// defaults.
func TestSessionRestartEndpointsWhileTheSettingsCannotBeRead(t *testing.T) {
	dir := t.TempDir()
	projectStore := store.NewTestStore(t, []domain.Project{{ID: "proj-test", Name: "test", Path: dir, Workstreams: []domain.Workstream{
		{ID: "ws-1", Name: "ws", Provider: "fake", WorkingDir: dir},
	}}})
	fakeProvider := fake.New(fake.Config{Seed: map[string][]domain.Workstream{"proj-test": {{WorkingDir: dir, Sessions: []domain.Session{
		{ID: "sess-1", Title: "Session", Agent: "codex", State: domain.SessionRunning},
	}}}}})
	registry := provider.NewRegistry()
	if err := registry.RegisterWorkstreamProvider(fakeProvider); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterSessionProvider(schemaFailingFake{fakeProvider}); err != nil {
		t.Fatal(err)
	}
	service := provider.NewService(projectStore, registry, 0)
	service.Reconcile(context.Background())
	handler := handlerForTest(Deps{Version: "test", Store: projectStore, Heartbeat: time.Minute, Providers: service, Notifications: notify.NewHub()})

	base := "/api/projects/proj-test/workstreams/ws-1/sessions/sess-1"
	if recorder := do(t, handler, http.MethodGet, base+"/launch-config", ""); recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), "try again") {
		t.Errorf("launch config = %d (%s), want 503 saying to try again", recorder.Code, recorder.Body.String())
	}
	if recorder := do(t, handler, http.MethodPost, base+"/restart", `{"config":{}}`); recorder.Code != http.StatusServiceUnavailable {
		t.Errorf("restart = %d (%s), want 503", recorder.Code, recorder.Body.String())
	}
}

// A failed create's row has no settings to restart with. Both endpoints say so
// as a conflict with what the row is, not as a server fault, and not as the
// "still starting" a row that will land gets.
func TestSessionRestartEndpointsRefuseAFailedCreate(t *testing.T) {
	h, st := newProviderHandlerCap(t, 0)
	ws := createTestWorkstream(t, h)
	failed := domain.Session{
		ID:       provider.StartingIDPrefix + "abc",
		Title:    "Claude Code session",
		Agent:    "Claude Code",
		Provider: "fake",
		State:    domain.SessionError,
		Error:    "unknown session option",
	}
	if err := st.AddEphemeralSession("proj-test", ws.ID, failed); err != nil {
		t.Fatalf("seeding the failed create: %v", err)
	}
	base := sessionPath(ws.ID, failed.ID)

	for _, tt := range []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "launch config", method: http.MethodGet, path: base + "/launch-config"},
		{name: "restart", method: http.MethodPost, path: base + "/restart", body: `{"config":{}}`},
	} {
		recorder := do(t, h, tt.method, tt.path, tt.body)
		var body struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(recorder.Body.Bytes(), &body)
		if recorder.Code != http.StatusConflict || body.Error != provider.ErrSessionNeverStarted.Error() {
			t.Errorf("%s = %d (%s), want 409 saying it never started", tt.name, recorder.Code, recorder.Body.String())
		}
	}
}

// unrestartable is the fake refusing to restart any session as anything.
type unrestartable struct{ *fake.Provider }

func (unrestartable) CheckRestart(provider.SessionRef, string) error {
	return fmt.Errorf("%w: it has no name", provider.ErrSessionNotRestartable)
}

// nonRestarter is the fake without its SessionRestarter.
type nonRestarter struct{ provider.SessionProvider }

// A session its provider cannot restart is refused as a conflict by the
// launch-config endpoint, which is what the restart form opens with; a provider
// that cannot restart at all refuses the restart as not implemented.
func TestSessionRestartEndpointsOfSessionsThatCannotBeRestarted(t *testing.T) {
	for _, tt := range []struct {
		name       string
		agent      string
		wrap       func(*fake.Provider) provider.SessionProvider
		method     string
		endpoint   string
		wantStatus int
		wantError  string
	}{
		{
			name:       "a session of a type no longer offered",
			agent:      "Retired",
			wrap:       func(fakeProvider *fake.Provider) provider.SessionProvider { return fakeProvider },
			method:     http.MethodGet,
			endpoint:   "/launch-config",
			wantStatus: http.StatusConflict,
			wantError:  provider.ErrOptionNotOffered.Error() + ` ("Retired")`,
		},
		{
			name:       "a session its provider cannot restart",
			wrap:       func(fakeProvider *fake.Provider) provider.SessionProvider { return unrestartable{fakeProvider} },
			method:     http.MethodGet,
			endpoint:   "/launch-config",
			wantStatus: http.StatusConflict,
			wantError:  "session cannot be restarted: it has no name",
		},
		{
			name:       "the settings of a session on a provider that cannot restart",
			wrap:       func(fakeProvider *fake.Provider) provider.SessionProvider { return nonRestarter{fakeProvider} },
			method:     http.MethodGet,
			endpoint:   "/launch-config",
			wantStatus: http.StatusNotImplemented,
			wantError:  provider.ErrCapabilityUnsupported.Error(),
		},
		{
			name:       "a provider that cannot restart",
			wrap:       func(fakeProvider *fake.Provider) provider.SessionProvider { return nonRestarter{fakeProvider} },
			method:     http.MethodPost,
			endpoint:   "/restart",
			wantStatus: http.StatusNotImplemented,
			wantError:  provider.ErrCapabilityUnsupported.Error(),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			projectStore := store.NewTestStore(t, []domain.Project{{ID: "proj-test", Name: "test", Path: dir, Workstreams: []domain.Workstream{
				{ID: "ws-1", Name: "ws", Provider: "fake", WorkingDir: dir},
			}}})
			fakeProvider := fake.New(fake.Config{Seed: map[string][]domain.Workstream{"proj-test": {{WorkingDir: dir, Sessions: []domain.Session{
				{ID: "sess-adopted", Title: "Adopted", Agent: cmp.Or(tt.agent, "Codex"), State: domain.SessionRunning},
			}}}}})
			registry := provider.NewRegistry()
			if err := registry.RegisterWorkstreamProvider(fakeProvider); err != nil {
				t.Fatalf("register fake workstream provider: %v", err)
			}
			if err := registry.RegisterSessionProvider(tt.wrap(fakeProvider)); err != nil {
				t.Fatalf("register session provider: %v", err)
			}
			service := provider.NewService(projectStore, registry, 0)
			service.Reconcile(context.Background())
			handler := handlerForTest(Deps{Version: "test", Store: projectStore, Heartbeat: time.Minute, Providers: service, Notifications: notify.NewHub()})

			recorder := do(t, handler, tt.method, "/api/projects/proj-test/workstreams/ws-1/sessions/sess-adopted"+tt.endpoint, "")
			var body struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(recorder.Body.Bytes(), &body)
			if recorder.Code != tt.wantStatus || body.Error != tt.wantError {
				t.Errorf("%s %s = %d (%s), want %d saying %q", tt.method, tt.endpoint, recorder.Code, recorder.Body.String(), tt.wantStatus, tt.wantError)
			}
		})
	}
}
