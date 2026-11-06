package api

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/notify"
	"github.com/eclipsesource/homeshell/daemon/internal/provider"
	"github.com/eclipsesource/homeshell/daemon/internal/settings"
	"github.com/eclipsesource/homeshell/daemon/internal/store"
)

// fakeTerminal records launches instead of spawning a window.
type fakeTerminal struct {
	calls int
	cmd   []string
	title string
}

func (f *fakeTerminal) Launch(_ context.Context, cmd []string, title string) error {
	f.calls++
	f.cmd = cmd
	f.title = title
	return nil
}

// newOpenWorkstreamHandler builds a handler over one project holding a single
// workstream at workingDir; no entries keeps the shipped default list.
func newOpenWorkstreamHandler(t *testing.T, workingDir string, entries ...settings.OpenWithEntry) (http.Handler, *fakeTerminal) {
	t.Helper()
	st := store.NewTestStore(t, []domain.Project{{
		ID: "proj-test", Name: "test", Path: t.TempDir(), GitBacked: true,
		Workstreams: []domain.Workstream{{
			ID: "ws-1", Name: "test", Provider: provider.CoreProviderID,
			StrategyID: "worktree", WorkingDir: workingDir, Sessions: []domain.Session{},
		}},
	}})
	term := &fakeTerminal{}
	svc := provider.NewService(st, provider.NewRegistry(), 0, provider.WithTerminal(term))
	settingsStore, err := settings.Load(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}
	if len(entries) > 0 {
		next := settingsStore.Get()
		next.OpenWith.Entries = append(next.OpenWith.Entries, entries...)
		if _, err := settingsStore.Set(next); err != nil {
			t.Fatalf("set settings: %v", err)
		}
	}
	svc.SetSettings(settingsStore)
	// Project once so the workstream carries its advertised open modes.
	svc.Reconcile(context.Background())
	h := handlerForTest(Deps{
		Version: "test", Store: st, Heartbeat: time.Minute,
		Providers: svc, Notifications: notify.NewHub(),
	})
	return h, term
}

func TestOpenWorkstream(t *testing.T) {
	dir := t.TempDir()
	h, term := newOpenWorkstreamHandler(t, dir)

	rec := do(t, h, http.MethodPost, "/api/projects/proj-test/workstreams/ws-1/open", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("open status = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	var res struct {
		Mode string `json:"mode"`
		Data struct {
			WorkingDir string `json:"workingDir"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode open result: %v", err)
	}
	if res.Mode != "terminal" {
		t.Errorf("open mode = %q, want terminal", res.Mode)
	}
	if res.Data.WorkingDir != dir {
		t.Errorf("open workingDir = %q, want %q", res.Data.WorkingDir, dir)
	}
	if term.calls != 1 {
		t.Fatalf("terminal launched %d times, want 1", term.calls)
	}
	if !strings.Contains(strings.Join(term.cmd, " "), dir) {
		t.Errorf("launched command %q does not mention %q", term.cmd, dir)
	}
}

// A client must not be able to smuggle a mode past the open-with endpoint.
func TestOpenWorkstreamIgnoresABody(t *testing.T) {
	h, term := newOpenWorkstreamHandler(t, t.TempDir())

	rec := do(t, h, http.MethodPost, "/api/projects/proj-test/workstreams/ws-1/open", `{"mode":"telepathy"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("open status = %d (%s), want 200 (the body is not read)", rec.Code, rec.Body.String())
	}
	if term.calls != 1 {
		t.Errorf("terminal launched %d times, want 1", term.calls)
	}
}

func TestOpenWorkstreamUnknown(t *testing.T) {
	h, term := newOpenWorkstreamHandler(t, t.TempDir())

	rec := do(t, h, http.MethodPost, "/api/projects/proj-test/workstreams/ghost/open", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("open status = %d (%s), want 404", rec.Code, rec.Body.String())
	}
	if term.calls != 0 {
		t.Errorf("terminal launched %d times, want none", term.calls)
	}
}

// The workstream exists; it just has no directory to hand to a terminal.
func TestOpenWorkstreamWithoutFilesIsAConflict(t *testing.T) {
	h, term := newOpenWorkstreamHandler(t, "")

	rec := do(t, h, http.MethodPost, "/api/projects/proj-test/workstreams/ws-1/open", "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("open status = %d (%s), want 409", rec.Code, rec.Body.String())
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if !strings.Contains(body.Error, "working directory") {
		t.Errorf("error = %q, want it to explain the missing working directory", body.Error)
	}
	if term.calls != 0 {
		t.Errorf("terminal launched %d times, want none", term.calls)
	}
}

func TestOpenWorkstreamWithoutProviders(t *testing.T) {
	st := store.NewTestStore(t, store.Seed())
	h := handlerForTest(Deps{Version: "test", Store: st, Heartbeat: time.Minute, Notifications: notify.NewHub()})

	rec := do(t, h, http.MethodPost, "/api/projects/p/workstreams/ws/open", "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("open status = %d (%s), want 503", rec.Code, rec.Body.String())
	}

	rec = do(t, h, http.MethodPost, "/api/projects/p/workstreams/ws/open-with", `{"mode":"open-with:terminal"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("open-with status = %d (%s), want 503", rec.Code, rec.Body.String())
	}
}

func TestOpenWorkstreamWithRejectsAnUnknownMode(t *testing.T) {
	h, term := newOpenWorkstreamHandler(t, t.TempDir())

	rec := do(t, h, http.MethodPost, "/api/projects/proj-test/workstreams/ws-1/open-with", `{"mode":"telepathy"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("open-with status = %d (%s), want 400", rec.Code, rec.Body.String())
	}
	if term.calls != 0 {
		t.Errorf("terminal launched %d times, want none", term.calls)
	}
}

// A missing mode is a bad request, not a silent default: /open is the modeless one.
func TestOpenWorkstreamWithRequiresAMode(t *testing.T) {
	h, term := newOpenWorkstreamHandler(t, t.TempDir())

	for _, body := range []string{"", `{}`, `{"mode":"  "}`} {
		rec := do(t, h, http.MethodPost, "/api/projects/proj-test/workstreams/ws-1/open-with", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("open-with %q status = %d (%s), want 400", body, rec.Code, rec.Body.String())
		}
	}
	if term.calls != 0 {
		t.Errorf("terminal launched %d times, want none", term.calls)
	}
}

func TestOpenWorkstreamWithAnEntry(t *testing.T) {
	dir := t.TempDir()
	h, term := newOpenWorkstreamHandler(t, dir,
		settings.OpenWithEntry{ID: "e1", Label: "Neovim", Command: "nvim .", InTerminal: true})

	rec := do(t, h, http.MethodPost, "/api/projects/proj-test/workstreams/ws-1/open-with", `{"mode":"open-with:e1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("open-with status = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	var res struct {
		Mode string `json:"mode"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode open result: %v", err)
	}
	if res.Mode != "open-with:e1" {
		t.Errorf("mode = %q, want open-with:e1", res.Mode)
	}
	if term.calls != 1 {
		t.Fatalf("terminal launched %d times, want 1", term.calls)
	}
	if script := strings.Join(term.cmd, " "); !strings.Contains(script, "nvim .") {
		t.Errorf("launched command %q does not run the entry's line", script)
	}
}

// A deleted row is a 400, like a mode the daemon never implemented.
func TestOpenWorkstreamWithADeletedEntry(t *testing.T) {
	h, term := newOpenWorkstreamHandler(t, t.TempDir())

	rec := do(t, h, http.MethodPost, "/api/projects/proj-test/workstreams/ws-1/open-with", `{"mode":"open-with:gone"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("open-with status = %d (%s), want 400", rec.Code, rec.Body.String())
	}
	if term.calls != 0 {
		t.Errorf("terminal launched %d times, want none", term.calls)
	}
}

// A client renders one action per mode without asking per workstream.
func TestProjectsAdvertiseWorkstreamOpenModes(t *testing.T) {
	h, _ := newOpenWorkstreamHandler(t, t.TempDir(),
		settings.OpenWithEntry{ID: "e1", Label: "VS Code", Command: "code ."})

	rec := do(t, h, http.MethodGet, "/api/projects", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("projects status = %d, want 200", rec.Code)
	}
	var body struct {
		Projects []domain.Project `json:"projects"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode projects: %v", err)
	}
	modes := body.Projects[0].Workstreams[0].OpenModes
	want := map[string]bool{"open-with:terminal": false, "open-with:e1": false}
	for _, mode := range modes {
		if _, expected := want[mode.ID]; expected {
			want[mode.ID] = true
		}
		if mode.Label == "" {
			t.Errorf("mode %s has no label", mode.ID)
		}
	}
	for id, seen := range want {
		if !seen {
			t.Errorf("open modes = %+v; want %s among them", modes, id)
		}
	}
}
