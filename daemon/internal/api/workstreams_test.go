package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/notify"
	"github.com/eclipsesource/homeshell/daemon/internal/provider"
	"github.com/eclipsesource/homeshell/daemon/internal/provider/fake"
	"github.com/eclipsesource/homeshell/daemon/internal/store"
)

// newProviderHandler builds a handler over one empty project owned by a fake
// provider, wired through a reconciler.
func newProviderHandler(t *testing.T) (http.Handler, *store.Store) {
	t.Helper()
	st := store.NewTestStore(t, []domain.Project{{
		ID: "proj-test", Name: "test", Path: t.TempDir(), GitBacked: true,
		Workstreams: []domain.Workstream{},
	}})
	reg := provider.NewRegistry()
	registerFakeBothRoles(t, reg, fake.Config{})
	svc := provider.NewService(st, reg, 0)
	svc.Reconcile(context.Background())
	h := handlerForTest(Deps{Version: "test", Store: st, Heartbeat: time.Minute, Providers: svc, Notifications: notify.NewHub()})
	return h, st
}

// registerFakeBothRoles registers the fake as both a workstream provider and a
// session provider, mirroring the demo.
func registerFakeBothRoles(t *testing.T, reg *provider.Registry, cfg fake.Config) {
	t.Helper()
	fp := fake.New(cfg)
	if err := reg.RegisterWorkstreamProvider(fp); err != nil {
		t.Fatalf("register fake workstream provider: %v", err)
	}
	if err := reg.RegisterSessionProvider(fp); err != nil {
		t.Fatalf("register fake session provider: %v", err)
	}
}

func do(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	h.ServeHTTP(rec, r)
	return rec
}

// createTestWorkstream creates a workstream via the API and returns the real
// (post-creation) workstream. Creation is asynchronous: the POST returns a
// placeholder, so this polls GET /api/projects until the real one lands.
func createTestWorkstream(t *testing.T, h http.Handler) domain.Workstream {
	t.Helper()
	rec := do(t, h, http.MethodPost, "/api/projects/proj-test/workstreams",
		`{"providerId":"fake","name":"feat","config":{"branch":"main"}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d (%s), want 201", rec.Code, rec.Body.String())
	}
	return awaitRealWorkstream(t, h, "proj-test")
}

// awaitRealWorkstream polls GET /api/projects until a non-placeholder
// workstream appears in the project.
func awaitRealWorkstream(t *testing.T, h http.Handler, projectID string) domain.Workstream {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for real workstream")
		default:
		}
		time.Sleep(10 * time.Millisecond)
		rec := do(t, h, http.MethodGet, "/api/projects", "")
		var body struct {
			Projects []domain.Project `json:"projects"`
		}
		if json.Unmarshal(rec.Body.Bytes(), &body) != nil {
			continue
		}
		for _, p := range body.Projects {
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

func TestOpenSession(t *testing.T) {
	h, st := newProviderHandler(t)
	ws := createTestWorkstream(t, h)
	sess := createTestSession(t, h, st, ws.ID)

	rec := do(t, h, http.MethodPost, "/api/projects/proj-test/workstreams/"+ws.ID+"/sessions/"+sess.ID+"/open", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("open status = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	var res struct {
		Mode string `json:"mode"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode open result: %v", err)
	}
	if res.Mode != "terminal" {
		t.Errorf("open mode = %q, want terminal", res.Mode)
	}
}

func TestOpenSessionUnknownWorkstreamReturns404(t *testing.T) {
	h, _ := newProviderHandler(t)
	rec := do(t, h, http.MethodPost, "/api/projects/proj-test/workstreams/ghost/sessions/s1/open", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d (%s), want 404", rec.Code, rec.Body.String())
	}
}

func TestOpenSessionUnknownSessionReturns404(t *testing.T) {
	h, _ := newProviderHandler(t)
	ws := createTestWorkstream(t, h)
	rec := do(t, h, http.MethodPost, "/api/projects/proj-test/workstreams/"+ws.ID+"/sessions/ghost/open", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d (%s), want 404", rec.Code, rec.Body.String())
	}
}

func TestOpenSessionUnwiredReturns503(t *testing.T) {
	st := store.NewTestStore(t, []domain.Project{{ID: "p", Path: "/p", Workstreams: []domain.Workstream{}}})
	h := handlerForTest(Deps{Store: st, Heartbeat: time.Minute, Notifications: notify.NewHub()}) // no Providers
	rec := do(t, h, http.MethodPost, "/api/projects/p/workstreams/ws/sessions/s/open", "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

func TestCreateWorkstream(t *testing.T) {
	h, st := newProviderHandler(t)
	ws := createTestWorkstream(t, h)
	if ws.Provider != "fake" || ws.Name != "feat" || ws.Branch != "main" {
		t.Errorf("workstream = %+v; want provider=fake name=feat branch=main", ws)
	}
	// After async creation completes, the real workstream has replaced the
	// placeholder in the store.
	projects := st.Projects()
	if len(projects[0].Workstreams) != 1 {
		t.Fatalf("store has %d workstreams after create, want 1", len(projects[0].Workstreams))
	}
	if projects[0].Workstreams[0].ID != ws.ID {
		t.Errorf("projected workstream id = %q, want %q", projects[0].Workstreams[0].ID, ws.ID)
	}
}

// A draft answers what a create would be, and creates nothing.
func TestWorkstreamDraft(t *testing.T) {
	handler, projectStore := newProviderHandler(t)
	recorder := do(t, handler, http.MethodPost, "/api/projects/proj-test/workstream-draft",
		`{"providerId":"fake","name":" feat ","config":{"branch":"main"}}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200", recorder.Code, recorder.Body.String())
	}
	var draft provider.Draft
	if err := json.Unmarshal(recorder.Body.Bytes(), &draft); err != nil || draft.Name != "feat" {
		t.Errorf("draft = %s, want the name %q", recorder.Body.String(), "feat")
	}
	if got := len(projectStore.Projects()[0].Workstreams); got != 0 {
		t.Errorf("workstreams = %d after a draft, want 0", got)
	}
}

func TestWorkstreamDraftRefusals(t *testing.T) {
	handler, _ := newProviderHandler(t)
	for _, tt := range []struct {
		path, body string
		want       int
	}{
		{"/api/projects/proj-test/workstream-draft", `{`, http.StatusBadRequest},
		{"/api/projects/nope/workstream-draft", `{}`, http.StatusNotFound},
		{"/api/projects/proj-test/workstream-draft", `{"providerId":"nope"}`, http.StatusBadRequest},
	} {
		if recorder := do(t, handler, http.MethodPost, tt.path, tt.body); recorder.Code != tt.want {
			t.Errorf("POST %s %s: status = %d, want %d", tt.path, tt.body, recorder.Code, tt.want)
		}
	}
}

func TestCreateWorkstreamValidationError(t *testing.T) {
	h, _ := newProviderHandler(t)
	rec := do(t, h, http.MethodPost, "/api/projects/proj-test/workstreams",
		`{"providerId":"fake","config":{}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	var body struct {
		Error  string            `json:"error"`
		Fields map[string]string `json:"fields"`
	}
	json.Unmarshal(rec.Body.Bytes(), &body)
	if len(body.Fields) == 0 {
		t.Errorf("no field-level detail in %s", rec.Body.String())
	}
}

// A taken name is a conflict, said on the name field.
func TestCreateWorkstreamDuplicateNameRefusedOnTheName(t *testing.T) {
	h, st := newProviderHandler(t)
	if err := st.AddWorkstream("proj-test", domain.Workstream{
		ID: "ws-1", Name: "copy one", Provider: "fake", WorkingDir: "/work/one", Sessions: []domain.Session{},
	}); err != nil {
		t.Fatalf("AddWorkstream: %v", err)
	}
	rec := do(t, h, http.MethodPost, "/api/projects/proj-test/workstreams",
		`{"providerId":"fake","name":"copy one","config":{"branch":"other"}}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d (%s), want 409", rec.Code, rec.Body.String())
	}
	var body struct {
		Fields map[string]string `json:"fields"`
	}
	json.Unmarshal(rec.Body.Bytes(), &body)
	if want := `"copy one" is already used in this project; enter another`; body.Fields["/name"] != want {
		t.Errorf("fields = %v, want /name: %q", body.Fields, want)
	}
}

func TestCreateWorkstreamErrors(t *testing.T) {
	h, _ := newProviderHandler(t)
	tests := []struct {
		name, path, body string
		want             int
	}{
		{"unknown project", "/api/projects/nope/workstreams", `{"providerId":"fake","config":{"branch":"m"}}`, http.StatusNotFound},
		{"unknown provider", "/api/projects/proj-test/workstreams", `{"providerId":"ghost"}`, http.StatusBadRequest},
		{"bad json", "/api/projects/proj-test/workstreams", `{`, http.StatusBadRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(t, h, http.MethodPost, tc.path, tc.body)
			if rec.Code != tc.want {
				t.Errorf("status = %d, want %d (%s)", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

func TestWorkstreamLifecycleUnwiredReturns503(t *testing.T) {
	st := store.NewTestStore(t, []domain.Project{{ID: "p", Path: "/p", Workstreams: []domain.Workstream{}}})
	h := handlerForTest(Deps{Store: st, Heartbeat: time.Minute, Notifications: notify.NewHub()}) // no Providers
	rec := do(t, h, http.MethodPost, "/api/projects/p/workstreams", `{"providerId":"fake"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

func TestRenameWorkstream(t *testing.T) {
	h, st := newProviderHandler(t)
	ws := createTestWorkstream(t, h)
	rec := do(t, h, http.MethodPatch, "/api/projects/proj-test/workstreams/"+ws.ID, `{"name":"renamed"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d (%s), want 204", rec.Code, rec.Body.String())
	}
	// Re-read after reconciliation.
	found := false
	for _, p := range st.Projects() {
		if p.ID == "proj-test" {
			for _, w := range p.Workstreams {
				if w.ID == ws.ID && w.Name == "renamed" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Errorf("workstream not renamed; store = %+v", st.Projects()[0].Workstreams)
	}
}

// A rename onto a taken name is the same conflict as a create, said the same way.
func TestRenameWorkstreamDuplicateNameRefusedOnTheName(t *testing.T) {
	h, st := newProviderHandler(t)
	ws := createTestWorkstream(t, h)
	if err := st.AddWorkstream("proj-test", domain.Workstream{
		ID: "ws-1", Name: "Copy one", Provider: "fake", WorkingDir: "/work/one", Sessions: []domain.Session{},
	}); err != nil {
		t.Fatalf("AddWorkstream: %v", err)
	}
	rec := do(t, h, http.MethodPatch, "/api/projects/proj-test/workstreams/"+ws.ID, `{"name":"copy one"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d (%s), want 409", rec.Code, rec.Body.String())
	}
	var body struct {
		Fields map[string]string `json:"fields"`
	}
	json.Unmarshal(rec.Body.Bytes(), &body)
	if want := `"Copy one" is already used in this project; enter another`; body.Fields["/name"] != want {
		t.Errorf("fields = %v, want /name: %q", body.Fields, want)
	}
}

func TestRenameWorkstreamEmptyNameRejected(t *testing.T) {
	h, _ := newProviderHandler(t)
	ws := createTestWorkstream(t, h)
	rec := do(t, h, http.MethodPatch, "/api/projects/proj-test/workstreams/"+ws.ID, `{"name":""}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestDeleteWorkstream(t *testing.T) {
	h, st := newProviderHandler(t)
	ws := createTestWorkstream(t, h)
	rec := do(t, h, http.MethodDelete, "/api/projects/proj-test/workstreams/"+ws.ID, "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if n := len(st.Projects()[0].Workstreams); n != 0 {
		t.Errorf("store has %d workstreams after delete, want 0", n)
	}
	// Deleting again is a 404 (no longer present).
	if again := do(t, h, http.MethodDelete, "/api/projects/proj-test/workstreams/"+ws.ID, ""); again.Code != http.StatusNotFound {
		t.Errorf("second delete status = %d, want 404", again.Code)
	}
}

func TestDeleteWorkstreamWithKeepFiles(t *testing.T) {
	h, st := newProviderHandler(t)
	ws := createTestWorkstream(t, h)
	// DELETE with keepFiles=true should still remove the workstream.
	rec := do(t, h, http.MethodDelete, "/api/projects/proj-test/workstreams/"+ws.ID,
		`{"keepFiles":true}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if n := len(st.Projects()[0].Workstreams); n != 0 {
		t.Errorf("store has %d workstreams after delete, want 0", n)
	}
}

func TestCreatedWorkstreamAppearsOnEventStream(t *testing.T) {
	h, _ := newProviderHandler(t)
	server := httptest.NewServer(h)
	defer server.Close()

	stream, closeStream := openStream(t, server.URL+"/api/events")
	defer closeStream()
	if snap := readEvent(t, stream); snap.Type != "state" {
		t.Fatalf("first event = %q, want state", snap.Type)
	}

	res, err := http.Post(server.URL+"/api/projects/proj-test/workstreams", "application/json",
		bytes.NewReader([]byte(`{"providerId":"fake","name":"streamed","config":{"branch":"stream"}}`)))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", res.StatusCode)
	}
	// The placeholder insert triggers an SSE event immediately.
	update := readEvent(t, stream)
	if update.Type != "state" {
		t.Fatalf("update event = %q, want state", update.Type)
	}
	if !strings.Contains(update.Data, "streamed") {
		t.Errorf("pushed state lacks the new workstream: %s", update.Data)
	}
}

// worktreeHandler builds a handler over a git project proj-git with one linked
// worktree on branch wt-a, and returns the worktree's path.
func worktreeHandler(t *testing.T) (http.Handler, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo, dir := t.TempDir(), filepath.Join(t.TempDir(), "wt")
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"commit", "-q", "--allow-empty", "-m", "initial"},
		{"worktree", "add", "-q", "-b", "wt-a", dir},
	} {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	projectStore := store.NewTestStore(t, []domain.Project{{
		ID: "proj-git", Name: "git", Path: repo, GitBacked: true, Workstreams: []domain.Workstream{},
	}})
	service := provider.NewService(projectStore, provider.NewRegistry(), 0)
	service.Reconcile(context.Background())
	return handlerForTest(Deps{Version: "test", Store: projectStore, Heartbeat: time.Minute, Providers: service,
		Notifications: notify.NewHub()}), dir
}

func TestAddUntrackedWorktree(t *testing.T) {
	handler, dir := worktreeHandler(t)
	body, _ := json.Marshal(map[string]string{"path": dir})

	recorder := do(t, handler, http.MethodPost, "/api/projects/proj-git/worktree-workstreams", string(body))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d (%s), want 201", recorder.Code, recorder.Body.String())
	}
	var added domain.Workstream
	if err := json.Unmarshal(recorder.Body.Bytes(), &added); err != nil {
		t.Fatal(err)
	}
	if added.ID == "" || added.Name != "wt-a" || added.WorkingDir != dir {
		t.Errorf("workstream = %+v, want wt-a in %s", added, dir)
	}
}

func TestAddUntrackedWorktreeRefusals(t *testing.T) {
	handler, _ := worktreeHandler(t)
	tests := []struct {
		name string
		body string
		want int
	}{
		{name: "no path", body: `{}`, want: http.StatusBadRequest},
		{name: "a blank path", body: `{"path":"  "}`, want: http.StatusBadRequest},
		{name: "not an untracked worktree", body: `{"path":"/nowhere"}`, want: http.StatusConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := do(t, handler, http.MethodPost, "/api/projects/proj-git/worktree-workstreams", tt.body)
			var body struct {
				Error string `json:"error"`
			}
			if recorder.Code != tt.want || json.Unmarshal(recorder.Body.Bytes(), &body) != nil || body.Error == "" {
				t.Errorf("status = %d (%s), want %d with an error", recorder.Code, recorder.Body.String(), tt.want)
			}
		})
	}
}
