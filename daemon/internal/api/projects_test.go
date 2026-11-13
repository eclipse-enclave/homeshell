package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/git"
	"github.com/eclipsesource/homeshell/daemon/internal/notify"
	"github.com/eclipsesource/homeshell/daemon/internal/provider"
	"github.com/eclipsesource/homeshell/daemon/internal/store"
)

func TestProjectsEndpoint(t *testing.T) {
	handler, _ := testHandler(t, time.Minute)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/projects", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if contentType := recorder.Header().Get("Content-Type"); contentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", contentType)
	}

	var payload struct {
		Projects []domain.Project `json:"projects"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if len(payload.Projects) != 5 {
		t.Fatalf("got %d projects, want 5", len(payload.Projects))
	}
	if got := payload.Projects[0].State; got != domain.SessionWaiting {
		t.Errorf("first project state = %q, want %q (derived, not authored)", got, domain.SessionWaiting)
	}
}

func TestAddProjectEndpoint(t *testing.T) {
	dir := t.TempDir()
	handler, _ := testHandler(t, time.Minute)

	body := fmt.Sprintf(`{"path":%q}`, dir)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/projects", strings.NewReader(body)))

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body: %s", recorder.Code, http.StatusCreated, recorder.Body.String())
	}
	if contentType := recorder.Header().Get("Content-Type"); !strings.Contains(contentType, "application/json") {
		t.Errorf("Content-Type = %q", contentType)
	}

	var project domain.Project
	if err := json.Unmarshal(recorder.Body.Bytes(), &project); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if project.ID == "" {
		t.Error("project ID is empty")
	}
	wantName := filepath.Base(dir)
	if project.Name != wantName {
		t.Errorf("name = %q, want %q (defaulted from dir)", project.Name, wantName)
	}
	if project.Path != dir {
		t.Errorf("path = %q, want %q", project.Path, dir)
	}
}

// A project is added with a workstream to start sessions in: in place, in the
// project folder. The response carries it, so the client has it at once.
func TestAddProjectComesWithAnInPlaceWorkstream(t *testing.T) {
	dir := t.TempDir()
	handler := projectRemovalHandler(t)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/projects", strings.NewReader(fmt.Sprintf(`{"path":%q}`, dir))))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body: %s", recorder.Code, http.StatusCreated, recorder.Body.String())
	}
	var project domain.Project
	if err := json.Unmarshal(recorder.Body.Bytes(), &project); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if len(project.Workstreams) != 1 {
		t.Fatalf("workstreams = %+v, want the default one", project.Workstreams)
	}
	if ws := project.Workstreams[0]; ws.StrategyID != "in-place" || ws.WorkingDir != dir || ws.Name != filepath.Base(dir) {
		t.Errorf("default workstream = %+v, want one in place in %q named after it", ws, dir)
	}
}

func TestAddProjectWithExplicitName(t *testing.T) {
	dir := t.TempDir()
	handler, _ := testHandler(t, time.Minute)

	body := fmt.Sprintf(`{"path":%q,"name":"my project"}`, dir)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/projects", strings.NewReader(body)))

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusCreated)
	}
	var project domain.Project
	json.Unmarshal(recorder.Body.Bytes(), &project)
	if project.Name != "my project" {
		t.Errorf("name = %q, want %q", project.Name, "my project")
	}
}

func TestAddProjectRejectsInvalidInput(t *testing.T) {
	handler, _ := testHandler(t, time.Minute)

	tests := []struct {
		name string
		body string
		want int
	}{
		{"empty body", `{}`, http.StatusBadRequest},
		{"missing path", `{"name":"x"}`, http.StatusBadRequest},
		{"non-existent path", `{"path":"/no/such/dir/zzz"}`, http.StatusBadRequest},
		{"relative path", `{"path":"some/relative/dir"}`, http.StatusBadRequest},
		{"path is a file", "", http.StatusBadRequest}, // set up below
		{"invalid JSON", `not json`, http.StatusBadRequest},
	}

	// Set up "path is a file" test.
	tmpFile := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(tmpFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests[4].body = fmt.Sprintf(`{"path":%q}`, tmpFile)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/projects", strings.NewReader(tt.body)))
			if recorder.Code != tt.want {
				t.Errorf("status = %d, want %d; body: %s", recorder.Code, tt.want, recorder.Body.String())
			}
		})
	}
}

func TestAddProjectRejectsDuplicatePath(t *testing.T) {
	dir := t.TempDir()
	handler, _ := testHandler(t, time.Minute)

	body := fmt.Sprintf(`{"path":%q}`, dir)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/projects", strings.NewReader(body)))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("first add: status = %d", recorder.Code)
	}

	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/projects", strings.NewReader(body)))
	if recorder.Code != http.StatusConflict {
		t.Errorf("duplicate add: status = %d, want %d", recorder.Code, http.StatusConflict)
	}
}

func TestRenameProjectEndpoint(t *testing.T) {
	handler, _ := testHandler(t, time.Minute)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPatch, "/api/projects/"+store.DemoProjectID, strings.NewReader(`{"name":"renamed"}`)))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d; body: %s", recorder.Code, http.StatusNoContent, recorder.Body.String())
	}

	// Verify via GET /api/projects.
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/projects", nil))
	var payload struct {
		Projects []domain.Project `json:"projects"`
	}
	json.Unmarshal(recorder.Body.Bytes(), &payload)
	for _, project := range payload.Projects {
		if project.ID == store.DemoProjectID {
			if project.Name != "renamed" {
				t.Errorf("name = %q, want %q", project.Name, "renamed")
			}
			return
		}
	}
	t.Error("demo project not found after rename")
}

func TestRenameProjectRejectsEmpty(t *testing.T) {
	handler, _ := testHandler(t, time.Minute)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPatch, "/api/projects/"+store.DemoProjectID, strings.NewReader(`{"name":""}`)))
	if recorder.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
}

func TestRenameProjectNotFound(t *testing.T) {
	handler, _ := testHandler(t, time.Minute)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPatch, "/api/projects/nope", strings.NewReader(`{"name":"x"}`)))
	if recorder.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", recorder.Code, http.StatusNotFound)
	}
}

// projectRemovalHandler is testHandler with the provider service a project
// removal goes through.
func projectRemovalHandler(t *testing.T) http.Handler {
	t.Helper()
	projectStore := store.NewTestStore(t, store.Seed())
	return handlerForTest(Deps{
		Version: "1.2.3", Store: projectStore, Heartbeat: time.Minute,
		Providers: provider.NewService(projectStore, provider.NewRegistry(), 0), Notifications: notify.NewHub(),
	})
}

func TestRemoveProjectEndpoint(t *testing.T) {
	handler := projectRemovalHandler(t)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodDelete, "/api/projects/"+store.DemoProjectID, nil))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
	}

	// Verify it's gone.
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/projects", nil))
	var payload struct {
		Projects []domain.Project `json:"projects"`
	}
	json.Unmarshal(recorder.Body.Bytes(), &payload)
	for _, project := range payload.Projects {
		if project.ID == store.DemoProjectID {
			t.Error("demo project still present after removal")
		}
	}
}

func TestRemoveProjectNotFound(t *testing.T) {
	handler := projectRemovalHandler(t)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodDelete, "/api/projects/nope", nil))
	if recorder.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", recorder.Code, http.StatusNotFound)
	}
}

func TestAddProjectAppearsInEventStream(t *testing.T) {
	dir := t.TempDir()
	handler, _ := testHandler(t, time.Minute)
	server := httptest.NewServer(handler)
	defer server.Close()

	stream, closeStream := openStream(t, server.URL+"/api/events")
	defer closeStream()

	// Drain the initial snapshot.
	if event := readEvent(t, stream); event.Type != "state" {
		t.Fatalf("first event type = %q, want state", event.Type)
	}

	// Add a project via the API.
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/projects", strings.NewReader(fmt.Sprintf(`{"path":%q}`, dir)))
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("add: status = %d", response.StatusCode)
	}

	// The event stream must push the new state.
	event := readEvent(t, stream)
	if event.Type != "state" {
		t.Fatalf("pushed event type = %q, want state", event.Type)
	}
	if !strings.Contains(event.Data, filepath.Base(dir)) {
		t.Errorf("pushed event does not contain the new project: %s", event.Data)
	}
}

func TestRefreshProjectNotFound(t *testing.T) {
	handler, _ := testHandler(t, time.Minute)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/projects/nope/refresh", nil))
	if recorder.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", recorder.Code, http.StatusNotFound)
	}
}

func TestRefreshProjectDetectsGitBacked(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed; skipping refresh detection test")
	}
	dir := t.TempDir()
	projectStore := store.NewTestStore(t, nil, store.WithGitDetector(git.New()))
	handler := handlerForTest(Deps{Version: "1.2.3", Store: projectStore, Heartbeat: time.Minute, Notifications: notify.NewHub()})

	// Add the project while its directory is a plain directory.
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/projects", strings.NewReader(fmt.Sprintf(`{"path":%q}`, dir))))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("add: status = %d; body: %s", recorder.Code, recorder.Body.String())
	}
	var project domain.Project
	json.Unmarshal(recorder.Body.Bytes(), &project)
	if project.GitBacked {
		t.Fatal("plain directory reported as git-backed on creation")
	}

	// Make the directory a git repository, then refresh.
	initGitRepo(t, dir)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/projects/"+project.ID+"/refresh", nil))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("refresh: status = %d; body: %s", recorder.Code, recorder.Body.String())
	}

	// Verify via GET.
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/projects", nil))
	var payload struct {
		Projects []domain.Project `json:"projects"`
	}
	json.Unmarshal(recorder.Body.Bytes(), &payload)
	if len(payload.Projects) != 1 || !payload.Projects[0].GitBacked {
		t.Errorf("project not marked git-backed after refresh: %+v", payload.Projects)
	}
}

// initGitRepo turns dir into a git repository, skipping the test if git fails.
func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
}
