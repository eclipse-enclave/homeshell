package provider

import (
	"context"
	"testing"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/settings"
	"github.com/eclipsesource/homeshell/daemon/internal/store"
)

// shellStub's sessions run in an environment of their own; linkStub also
// declares a browser-openable link.
type shellStub struct{ stubSessionProvider }

func (s *shellStub) OpenShell(context.Context, SessionRef) (OpenResult, error) {
	return OpenResult{}, nil
}

type linkStub struct {
	shellStub
	links []Link
}

func (s *linkStub) Links(domain.Session) []Link { return s.links }

func (s *linkStub) LinkURL(context.Context, SessionRef, string) (string, error) {
	return "http://127.0.0.1:0/", nil
}

func registryWith(t *testing.T, providers ...SessionProvider) *Registry {
	t.Helper()
	reg := NewRegistry()
	for _, p := range providers {
		if err := reg.RegisterSessionProvider(p); err != nil {
			t.Fatalf("register %q: %v", p.ID(), err)
		}
	}
	return reg
}

func modeIDs(modes []domain.OpenMode) []string {
	ids := make([]string, len(modes))
	for i, m := range modes {
		ids[i] = m.ID
	}
	return ids
}

func actionIDs(list []domain.Action) []string {
	ids := make([]string, len(list))
	for i, a := range list {
		ids[i] = a.ID
	}
	return ids
}

func equal(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// The shipped default row: an empty command means an interactive shell.
var terminalEntry = settings.OpenWithEntry{
	ID: settings.DefaultTerminalEntryID, Label: "Terminal", InTerminal: true,
}

func TestOpenModes(t *testing.T) {
	dir := t.TempDir()
	shellProvider := &shellStub{stubSessionProvider{id: "enclave", available: true}}
	linkProvider := &linkStub{
		shellStub: shellStub{stubSessionProvider{id: "theia", available: true}},
		links:     []Link{{ID: "web-ide", Label: "Web IDE"}},
	}
	hostProvider := &stubSessionProvider{id: "local", available: true}

	live := func(providerID string) domain.Session {
		return domain.Session{ID: "s1", Provider: providerID, State: domain.SessionRunning}
	}

	tests := []struct {
		name    string
		ws      domain.Workstream
		entries []settings.OpenWithEntry
		want    []string
	}{
		{
			name:    "a workstream whose files are here offers the terminal entry",
			ws:      domain.Workstream{WorkingDir: dir},
			entries: []settings.OpenWithEntry{terminalEntry},
			want:    []string{"open-with:terminal"},
		},
		{
			name:    "a workstream with neither files nor sessions offers nothing",
			ws:      domain.Workstream{},
			entries: []settings.OpenWithEntry{terminalEntry},
			want:    []string{},
		},
		{
			name:    "a container session offers its shell beside the host terminal",
			ws:      domain.Workstream{WorkingDir: dir, Sessions: []domain.Session{live("enclave")}},
			entries: []settings.OpenWithEntry{terminalEntry},
			want:    []string{"open-with:terminal", "session-shell:s1"},
		},
		{
			// The host terminal opens the folder, which is not here.
			name:    "a container session without host files offers only its shell",
			ws:      domain.Workstream{Sessions: []domain.Session{live("enclave")}},
			entries: []settings.OpenWithEntry{terminalEntry},
			want:    []string{"session-shell:s1"},
		},
		{
			name: "each live container session offers its own shell",
			ws: domain.Workstream{WorkingDir: dir, Sessions: []domain.Session{
				live("enclave"),
				{ID: "s2", Provider: "enclave", State: domain.SessionIdle},
			}},
			entries: []settings.OpenWithEntry{terminalEntry},
			want:    []string{"open-with:terminal", "session-shell:s1", "session-shell:s2"},
		},
		{
			name: "a session with no container behind it offers no shell",
			ws: domain.Workstream{WorkingDir: dir, Sessions: []domain.Session{
				{ID: "s1", Provider: "enclave", State: domain.SessionStarting},
				{ID: "s2", Provider: "enclave", State: domain.SessionError},
				{ID: "s3", Provider: "enclave", State: domain.SessionDone},
				{ID: "s4", Provider: "enclave", State: domain.SessionStopped},
			}},
			entries: []settings.OpenWithEntry{terminalEntry},
			want:    []string{"open-with:terminal"},
		},
		{
			name:    "a host-session provider adds nothing",
			ws:      domain.Workstream{Sessions: []domain.Session{live("local")}},
			entries: []settings.OpenWithEntry{terminalEntry},
			want:    []string{},
		},
		{
			name: "a stopped session offers nothing",
			ws: domain.Workstream{Sessions: []domain.Session{
				{ID: "s1", Provider: "theia", State: domain.SessionStopped},
			}},
			entries: []settings.OpenWithEntry{terminalEntry},
			want:    []string{},
		},
		{
			name: "each entry the user added is its own mode",
			ws:   domain.Workstream{WorkingDir: dir},
			entries: []settings.OpenWithEntry{
				terminalEntry,
				{ID: "open-with-1", Label: "Kitty", Command: "kitty ."},
				{ID: "open-with-2", Label: "VS Code", Command: "code ."},
				{ID: "open-with-3", Label: "Vim", Command: "vim .", InTerminal: true},
			},
			want: []string{
				"open-with:terminal",
				"open-with:open-with-1",
				"open-with:open-with-2",
				"open-with:open-with-3",
			},
		},
		{
			// Every command entry opens the workstream's folder, which is not here.
			name: "command entries are not offered for a workstream with no files here",
			ws:   domain.Workstream{Sessions: []domain.Session{live("enclave")}},
			entries: []settings.OpenWithEntry{
				terminalEntry,
				{ID: "open-with-1", Label: "Kitty", Command: "kitty ."},
			},
			want: []string{"session-shell:s1"},
		},
		{
			name:    "a declared link is a mode, even without host files",
			ws:      domain.Workstream{Sessions: []domain.Session{live("theia")}},
			entries: []settings.OpenWithEntry{terminalEntry},
			want:    []string{"session-shell:s1", "link:web-ide"},
		},
		{
			name: "duplicate link declarations collapse to one mode",
			ws: domain.Workstream{WorkingDir: dir, Sessions: []domain.Session{
				live("theia"),
				{ID: "s2", Provider: "theia", State: domain.SessionRunning},
			}},
			entries: []settings.OpenWithEntry{terminalEntry},
			want:    []string{"open-with:terminal", "session-shell:s1", "session-shell:s2", "link:web-ide"},
		},
		{
			name: "a stopped session's links are not offered",
			ws: domain.Workstream{WorkingDir: dir, Sessions: []domain.Session{
				{ID: "s1", Provider: "theia", State: domain.SessionStopped},
			}},
			entries: []settings.OpenWithEntry{terminalEntry},
			want:    []string{"open-with:terminal"},
		},
	}

	reg := registryWith(t, shellProvider, linkProvider, hostProvider)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := modeIDs(openModes(tt.ws, reg, tt.entries, materialized(tt.ws.WorkingDir)))
			if !equal(got, tt.want) {
				t.Errorf("open modes = %v; want %v", got, tt.want)
			}
		})
	}
}

func TestOpenModesLabelsAnEntryAsTheUserNamedIt(t *testing.T) {
	entries := []settings.OpenWithEntry{
		{ID: "terminal", Label: "Shell", InTerminal: true},
		{ID: "open-with-1", Label: "Kitty, please", Command: "kitty ."},
	}
	modes := openModes(domain.Workstream{WorkingDir: t.TempDir()}, NewRegistry(), entries, true)

	want := map[string]string{
		"open-with:terminal":    "Shell",
		"open-with:open-with-1": "Kitty, please",
	}
	if len(modes) != len(want) {
		t.Fatalf("open modes = %v; want %d entries", modeIDs(modes), len(want))
	}
	for _, mode := range modes {
		if wantLabel := want[mode.ID]; mode.Label != wantLabel {
			t.Errorf("mode %s label = %q; want %q", mode.ID, mode.Label, wantLabel)
		}
	}
}

// Two sessions of one agent share a title; their shells must still read apart.
func TestShellModesAreLabelledBySession(t *testing.T) {
	reg := registryWith(t, &shellStub{stubSessionProvider{id: "enclave", available: true}})
	ws := domain.Workstream{Sessions: []domain.Session{
		{ID: "c1", Title: "claude session", Provider: "enclave", State: domain.SessionRunning},
		{ID: "c2", Title: "claude session", Provider: "enclave", State: domain.SessionIdle},
		{ID: "c3", Title: "codex session", Provider: "enclave", State: domain.SessionRunning},
		{ID: "c4", Provider: "enclave", State: domain.SessionRunning},
		{ID: "c5", Provider: "enclave", State: domain.SessionRunning},
	}}
	var got []string
	for _, mode := range openModes(ws, reg, nil, false) {
		got = append(got, mode.Label)
	}
	want := []string{
		"Shell in claude session (c1)",
		"Shell in claude session (c2)",
		"Shell in codex session",
		// Untitled, the label is the ID, which is unique on its own.
		"Shell in c4",
		"Shell in c5",
	}
	if !equal(got, want) {
		t.Errorf("labels = %q; want %q", got, want)
	}
}

// The plain open runs the terminal row, so that row is the default mode, and no
// mode is when the folder it opens is not here.
func TestOpenModesMarkTheDefaultOpen(t *testing.T) {
	reg := registryWith(t, &shellStub{stubSessionProvider{id: "enclave", available: true}})
	entries := []settings.OpenWithEntry{
		{ID: "open-with-1", Label: "VS Code", Command: "code ."},
		terminalEntry,
	}
	live := []domain.Session{{ID: "s1", Provider: "enclave", State: domain.SessionRunning}}

	var defaults []string
	for _, mode := range openModes(domain.Workstream{WorkingDir: t.TempDir(), Sessions: live}, reg, entries, true) {
		if mode.Default {
			defaults = append(defaults, mode.ID)
		}
	}
	if !equal(defaults, []string{"open-with:terminal"}) {
		t.Errorf("default modes = %v; want [open-with:terminal]", defaults)
	}
	for _, mode := range openModes(domain.Workstream{Sessions: live}, reg, entries, false) {
		if mode.Default {
			t.Errorf("mode %q is the default without a folder to open", mode.ID)
		}
	}
}

// A JSON null would force every client to handle two empty shapes.
func TestDerivedListsAreNeverNil(t *testing.T) {
	if modes := openModes(domain.Workstream{}, NewRegistry(), nil, false); modes == nil {
		t.Error("open modes = nil; want an empty slice")
	}
	if list := actions(false); list == nil {
		t.Error("actions = nil; want an empty slice")
	}
}

func TestAnnotateSetsModesAndActions(t *testing.T) {
	dir := t.TempDir()
	svc := NewService(store.NewTestStore(t, nil), NewRegistry(), 0)
	workstreams := []domain.Workstream{
		{ID: "w1", WorkingDir: dir},
		{ID: "w2", WorkingDir: "/nonexistent/homeshell-nowhere"},
	}
	svc.annotate("p1", workstreams)
	// With no settings store wired the shipped defaults answer.
	if got := modeIDs(workstreams[0].OpenModes); !equal(got, []string{"open-with:terminal"}) {
		t.Errorf("annotated open modes = %v; want the default terminal entry", got)
	}
	if got := modeIDs(workstreams[1].OpenModes); !equal(got, []string{}) {
		t.Errorf("annotated open modes = %v; want none without files or sessions", got)
	}
	// The actions ride the same projection-time answer about the files: the note
	// is offered only where there is a directory to keep it in.
	if got := actionIDs(workstreams[0].Actions); !equal(got, []string{domain.ActionNote}) {
		t.Errorf("annotated actions = %v; want the note", got)
	}
	if got := actionIDs(workstreams[1].Actions); !equal(got, []string{}) {
		t.Errorf("annotated actions = %v; want none without files", got)
	}
}

func TestAnnotateSetsProviderLabel(t *testing.T) {
	registry := NewRegistry()
	if err := registry.RegisterWorkstreamProvider(&stubWorkstreamProvider{id: "remote", label: "Remote Box"}); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterSessionProvider(&stubSessionProvider{id: "local", label: "Local System"}); err != nil {
		t.Fatal(err)
	}
	service := NewService(store.NewTestStore(t, nil), registry, 0)

	tests := []struct {
		name     string
		provider string
		want     string
	}{
		{name: "built-in provider", provider: CoreProviderID, want: ""},
		{name: "other workstream provider", provider: "remote", want: "Remote Box"},
		{name: "adopted by a session provider", provider: "local", want: "Local System"},
		{name: "unregistered provider", provider: "gone", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workstreams := []domain.Workstream{{ID: "w1", Provider: tt.provider}}
			service.annotate("p1", workstreams)
			if got := workstreams[0].ProviderLabel; got != tt.want {
				t.Errorf("provider label = %q; want %q", got, tt.want)
			}
		})
	}
}

// The note action is labelled in the plural: a workstream keeps one file of
// notes, and "Note" read as a request to write a single one.
func TestNoteActionIsLabelledNotes(t *testing.T) {
	list := actions(true)
	if len(list) != 1 || list[0].ID != domain.ActionNote {
		t.Fatalf("actions(true) = %+v; want the note action alone", list)
	}
	if list[0].Label != "Notes" {
		t.Errorf("label = %q; want %q", list[0].Label, "Notes")
	}
}
