package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/notify"
	"github.com/eclipsesource/homeshell/daemon/internal/provider"
	"github.com/eclipsesource/homeshell/daemon/internal/store"
)

// fakeOpener stands in for the desktop hand-off so the api tests never pop an
// application open.
type fakeOpener struct {
	calls int
	path  string
	fail  error
}

func (f *fakeOpener) Open(_ context.Context, path string) error {
	f.calls++
	f.path = path
	return f.fail
}

// newNoteHandler builds a handler over one project holding a workstream at
// workingDir, with a recording desktop opener.
func newNoteHandler(t *testing.T, workingDir string) (http.Handler, *fakeOpener) {
	t.Helper()
	st := store.NewTestStore(t, []domain.Project{{
		ID: "proj-test", Name: "test", Path: t.TempDir(), GitBacked: true,
		Workstreams: []domain.Workstream{{
			ID: "ws-1", Name: "test", Provider: provider.CoreProviderID,
			StrategyID: "worktree", WorkingDir: workingDir, Sessions: []domain.Session{},
		}},
	}})
	desktop := &fakeOpener{}
	svc := provider.NewService(st, provider.NewRegistry(), 0, provider.WithOpener(desktop))
	svc.Reconcile(context.Background())
	h := handlerForTest(Deps{
		Version: "test", Store: st, Heartbeat: time.Minute,
		Providers: svc, Notifications: notify.NewHub(),
	})
	return h, desktop
}

func TestWorkstreamNote(t *testing.T) {
	dir := t.TempDir()
	h, desktop := newNoteHandler(t, dir)

	rec := do(t, h, http.MethodPost, "/api/projects/proj-test/workstreams/ws-1/note", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("note status = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	var res struct {
		Path    string `json:"path"`
		Created bool   `json:"created"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode note result: %v", err)
	}
	want := filepath.Join(dir, provider.NoteFileName)
	if res.Path != want || !res.Created {
		t.Errorf("note result = %+v; want %q created", res, want)
	}
	if desktop.calls != 1 || desktop.path != want {
		t.Errorf("opened %q (%d calls); want %q once", desktop.path, desktop.calls, want)
	}
}

// A workstream with nothing on disk has nowhere to keep a note: a conflict the
// client can explain, not a server failure.
func TestWorkstreamNoteWithoutFilesIsAConflict(t *testing.T) {
	h, desktop := newNoteHandler(t, "")

	rec := do(t, h, http.MethodPost, "/api/projects/proj-test/workstreams/ws-1/note", "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("note status = %d (%s), want 409", rec.Code, rec.Body.String())
	}
	if desktop.calls != 0 {
		t.Errorf("opened %d times, want none", desktop.calls)
	}
}

func TestWorkstreamNoteUnknownWorkstream(t *testing.T) {
	h, _ := newNoteHandler(t, t.TempDir())

	rec := do(t, h, http.MethodPost, "/api/projects/proj-test/workstreams/ghost/note", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("note status = %d (%s), want 404", rec.Code, rec.Body.String())
	}
}

// A host with no desktop opener installed cannot hand the file over. That is how
// the daemon's host is set up, not a fault in the daemon, so the client is told
// it is a conflict and can say so rather than showing a server error.
func TestWorkstreamNoteWithoutADesktopOpenerIsAConflict(t *testing.T) {
	h, desktop := newNoteHandler(t, t.TempDir())
	desktop.fail = errors.New(`no desktop opener (xdg-open) on this host: exec: "xdg-open": not found`)

	rec := do(t, h, http.MethodPost, "/api/projects/proj-test/workstreams/ws-1/note", "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("note status = %d (%s), want 409", rec.Code, rec.Body.String())
	}
}

// The projection names the actions a workstream supports right now, which is
// what a UI renders. The note needs files, so it appears only where they are.
func TestProjectsReportTheNoteActionWhereTheFilesAre(t *testing.T) {
	h, _ := newNoteHandler(t, t.TempDir())
	if got := workstreamActionIDs(t, h); !slices.Contains(got, domain.ActionNote) {
		t.Errorf("actions = %v; want the note offered for a real directory", got)
	}

	gone, _ := newNoteHandler(t, filepath.Join(t.TempDir(), "missing"))
	if got := workstreamActionIDs(t, gone); slices.Contains(got, domain.ActionNote) {
		t.Errorf("actions = %v; want no note where the directory is gone", got)
	}
}

// workstreamActionIDs reads the first workstream's action IDs off GET /api/projects.
func workstreamActionIDs(t *testing.T, h http.Handler) []string {
	t.Helper()
	rec := do(t, h, http.MethodGet, "/api/projects", "")
	var body struct {
		Projects []domain.Project `json:"projects"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode projects: %v", err)
	}
	ids := []string{}
	for _, action := range body.Projects[0].Workstreams[0].Actions {
		ids = append(ids, action.ID)
	}
	return ids
}
