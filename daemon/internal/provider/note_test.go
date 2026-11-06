package provider_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/provider"
	"github.com/eclipsesource/homeshell/daemon/internal/store"
)

// recordingOpener stands in for the desktop hand-off, so a test run never pops
// an application open on the machine running it.
type recordingOpener struct {
	calls int
	path  string
	fail  error
}

func (o *recordingOpener) Open(_ context.Context, path string) error {
	o.calls++
	o.path = path
	return o.fail
}

// newNoteService wires a service over one workstream with a recording opener.
func newNoteService(t *testing.T, ws domain.Workstream) (*provider.Service, *recordingOpener) {
	t.Helper()
	st := store.NewTestStore(t, []domain.Project{{
		ID: "p1", Name: "p1", Path: t.TempDir(), Workstreams: []domain.Workstream{ws},
	}})
	desktop := &recordingOpener{}
	svc := provider.NewService(st, provider.NewRegistry(), 0, provider.WithOpener(desktop))
	return svc, desktop
}

// noteRef names the workstream newNoteService seeds.
var noteRef = provider.WorkstreamRef{ProjectID: "p1", WorkstreamID: "w1"}

func TestOpenNoteCreatesTheNoteAndHandsItOver(t *testing.T) {
	dir := t.TempDir()
	svc, desktop := newNoteService(t, domain.Workstream{ID: "w1", Name: "feature/x", WorkingDir: dir})

	res, err := svc.OpenNote(context.Background(), noteRef)
	if err != nil {
		t.Fatalf("OpenNote: %v", err)
	}
	want := filepath.Join(dir, provider.NoteFileName)
	if res.Path != want || !res.Created {
		t.Errorf("result = %+v; want %q created", res, want)
	}
	// The heading names the workstream, so a note opened weeks later says what
	// it is about.
	content, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("read note: %v", err)
	}
	if string(content) != "# feature/x\n\n" {
		t.Errorf("note content = %q; want a heading naming the workstream", content)
	}
	if desktop.calls != 1 || desktop.path != want {
		t.Errorf("opened %q (%d calls); want %q once", desktop.path, desktop.calls, want)
	}
}

// The interesting half of the feature: the note is the user's (or the agent's)
// file after the first click, so a second click must open it and nothing more.
func TestOpenNoteLeavesAnExistingNoteAlone(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, provider.NoteFileName)
	const written = "# feature/x\n\nthe deploy key is rotated on fridays\n"
	if err := os.WriteFile(path, []byte(written), 0o644); err != nil {
		t.Fatalf("seed note: %v", err)
	}
	svc, desktop := newNoteService(t, domain.Workstream{ID: "w1", Name: "feature/x", WorkingDir: dir})

	res, err := svc.OpenNote(context.Background(), noteRef)
	if err != nil {
		t.Fatalf("OpenNote: %v", err)
	}
	if res.Created {
		t.Error("result reports created for a note that was already there")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read note: %v", err)
	}
	if string(content) != written {
		t.Errorf("note content = %q; want it untouched (%q)", content, written)
	}
	if desktop.calls != 1 {
		t.Errorf("opened %d times; want the existing note handed over once", desktop.calls)
	}
}

// A workstream can reach the store without a name (a provider that never set
// one); the heading still has to say something.
func TestOpenNoteHeadsAnUnnamedWorkstreamGenerically(t *testing.T) {
	dir := t.TempDir()
	svc, _ := newNoteService(t, domain.Workstream{ID: "w1", WorkingDir: dir})

	if _, err := svc.OpenNote(context.Background(), noteRef); err != nil {
		t.Fatalf("OpenNote: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(dir, provider.NoteFileName))
	if err != nil {
		t.Fatalf("read note: %v", err)
	}
	if string(content) != "# Workstream\n\n" {
		t.Errorf("note content = %q; want a generic heading", content)
	}
}

func TestOpenNoteWithoutFilesIsRefused(t *testing.T) {
	for _, tt := range []struct {
		name string
		dir  string
	}{
		{"never materialized", ""},
		{"directory is gone", filepath.Join(t.TempDir(), "missing")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			svc, desktop := newNoteService(t, domain.Workstream{ID: "w1", Name: "x", WorkingDir: tt.dir})

			_, err := svc.OpenNote(context.Background(), noteRef)
			if !errors.Is(err, provider.ErrNoWorkingDir) {
				t.Fatalf("error = %v; want ErrNoWorkingDir", err)
			}
			if desktop.calls != 0 {
				t.Errorf("opened %d times; want nothing handed over", desktop.calls)
			}
		})
	}
}

func TestOpenNoteForAnUnknownWorkstream(t *testing.T) {
	svc, _ := newNoteService(t, domain.Workstream{ID: "w1", Name: "x", WorkingDir: t.TempDir()})

	_, err := svc.OpenNote(context.Background(), provider.WorkstreamRef{ProjectID: "p1", WorkstreamID: "ghost"})
	if !errors.Is(err, provider.ErrWorkstreamNotFound) {
		t.Fatalf("error = %v; want ErrWorkstreamNotFound", err)
	}
}

// A host with no desktop opener installed — a headless server, a minimal
// container — is a normal configuration, not a server fault, so the hand-off
// failure has its own sentinel the API can answer as a conflict.
func TestOpenNoteReportsAFailedHandoffAsSuch(t *testing.T) {
	dir := t.TempDir()
	svc, desktop := newNoteService(t, domain.Workstream{ID: "w1", Name: "x", WorkingDir: dir})
	desktop.fail = errors.New("no desktop opener (xdg-open) on this host")

	_, err := svc.OpenNote(context.Background(), noteRef)
	if !errors.Is(err, provider.ErrHandoffFailed) {
		t.Fatalf("error = %v; want ErrHandoffFailed", err)
	}
	// The file is still there: the note was created before the hand-off, and a
	// missing opener is no reason to throw the user's file away.
	if _, err := os.Stat(filepath.Join(dir, provider.NoteFileName)); err != nil {
		t.Errorf("note after a failed hand-off: %v", err)
	}
}
