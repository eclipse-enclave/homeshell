package provider_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/provider"
	"github.com/eclipsesource/homeshell/daemon/internal/settings"
	"github.com/eclipsesource/homeshell/daemon/internal/store"
)

// recordingRunner records commands instead of starting a real process.
type recordingRunner struct {
	calls   int
	dir     string
	command string
	fail    error
}

func (r *recordingRunner) Start(_ context.Context, dir, command string) error {
	r.calls++
	r.dir = dir
	r.command = command
	return r.fail
}

// settingsWithEntries replaces the shipped open-with list with entries.
func settingsWithEntries(t *testing.T, entries ...settings.OpenWithEntry) *settings.Store {
	t.Helper()
	settingsStore, err := settings.Load(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}
	next := settingsStore.Get()
	next.OpenWith.Entries = entries
	if _, err := settingsStore.Set(next); err != nil {
		t.Fatalf("set settings: %v", err)
	}
	return settingsStore
}

// newOpenWithService wires a service over one workstream with recording
// launchers.
func newOpenWithService(t *testing.T, ws domain.Workstream, entries ...settings.OpenWithEntry) (*provider.Service, *recordingLauncher, *recordingRunner) {
	t.Helper()
	st := store.NewTestStore(t, []domain.Project{{
		ID: "p1", Name: "p1", Path: t.TempDir(), Workstreams: []domain.Workstream{ws},
	}})
	launcher := &recordingLauncher{}
	runner := &recordingRunner{}
	svc := provider.NewService(st, provider.NewRegistry(), 0,
		provider.WithTerminal(launcher), provider.WithCommandRunner(runner))
	svc.SetSettings(settingsWithEntries(t, entries...))
	return svc, launcher, runner
}

func openWith(t *testing.T, svc *provider.Service, entryID string) (provider.OpenResult, error) {
	t.Helper()
	return svc.OpenWorkstream(context.Background(),
		provider.WorkstreamRef{ProjectID: "p1", WorkstreamID: "ws-1"},
		domain.OpenWithModeFor(entryID))
}

func TestOpenWithStartsADetachedCommandInTheDirectory(t *testing.T) {
	dir := t.TempDir()
	svc, launcher, runner := newOpenWithService(t, workstreamAt(dir),
		settings.OpenWithEntry{ID: "e1", Label: "VS Code", Command: "code ."})

	res, err := openWith(t, svc, "e1")
	if err != nil {
		t.Fatalf("OpenWorkstream: %v", err)
	}
	if runner.calls != 1 || runner.dir != dir || runner.command != "code ." {
		t.Errorf("runner ran %q in %q (%d calls); want `code .` in %q once",
			runner.command, runner.dir, runner.calls, dir)
	}
	if launcher.calls != 0 {
		t.Errorf("terminal launched %d times; want none", launcher.calls)
	}
	if res.Mode != "open-with:e1" {
		t.Errorf("mode = %q; want open-with:e1", res.Mode)
	}
	var data struct {
		Target     string `json:"target"`
		WorkingDir string `json:"workingDir"`
		Command    string `json:"command"`
	}
	if err := json.Unmarshal(res.Data, &data); err != nil {
		t.Fatalf("unmarshal result data: %v", err)
	}
	if data.Target != "host" || data.WorkingDir != dir || data.Command != "code ." {
		t.Errorf("result data = %+v; want the host launch reported", data)
	}
}

// The flag is the user's own statement; nothing is detected from the host.
func TestOpenWithRunsAnInTerminalEntryInsideATerminal(t *testing.T) {
	dir := t.TempDir()
	svc, launcher, runner := newOpenWithService(t, workstreamAt(dir),
		settings.OpenWithEntry{ID: "e1", Label: "Neovim", Command: "nvim .", InTerminal: true})

	if _, err := openWith(t, svc, "e1"); err != nil {
		t.Fatalf("OpenWorkstream: %v", err)
	}
	if launcher.calls != 1 {
		t.Fatalf("terminal launched %d times; want 1", launcher.calls)
	}
	script := strings.Join(launcher.cmd, " ")
	if !strings.Contains(script, dir) || !strings.Contains(script, "nvim .") {
		t.Errorf("launched command %q; want it to cd into %q and run the line", script, dir)
	}
	if launcher.title != "test" {
		t.Errorf("window title = %q; want the workstream name", launcher.title)
	}
	if runner.calls != 0 {
		t.Errorf("detached runner called %d times; want none", runner.calls)
	}
}

func TestOpenWithEmptyCommandIsTheTerminalHandOff(t *testing.T) {
	dir := t.TempDir()
	svc, launcher, runner := newOpenWithService(t, workstreamAt(dir),
		settings.OpenWithEntry{ID: "terminal", Label: "Terminal", InTerminal: true})

	res, err := openWith(t, svc, "terminal")
	if err != nil {
		t.Fatalf("OpenWorkstream: %v", err)
	}
	if res.Mode != "terminal" {
		t.Errorf("mode = %q; want the terminal hand-off", res.Mode)
	}
	if launcher.calls != 1 {
		t.Errorf("terminal launched %d times; want 1", launcher.calls)
	}
	if !strings.Contains(strings.Join(launcher.cmd, " "), dir) {
		t.Errorf("launched command %q does not open the working directory", launcher.cmd)
	}
	if runner.calls != 0 {
		t.Errorf("detached runner called %d times; want none", runner.calls)
	}
}

func TestOpenWithRejectsAnUnknownEntry(t *testing.T) {
	svc, launcher, runner := newOpenWithService(t, workstreamAt(t.TempDir()),
		settings.OpenWithEntry{ID: "e1", Label: "VS Code", Command: "code ."})

	_, err := openWith(t, svc, "deleted")
	if !errors.Is(err, provider.ErrUnknownOpenMode) {
		t.Errorf("error = %v; want ErrUnknownOpenMode", err)
	}
	if launcher.calls != 0 || runner.calls != 0 {
		t.Error("an unknown entry must never launch anything")
	}
}

// Every command entry runs on the workstream's folder.
func TestOpenWithNeedsAMaterializedDirectory(t *testing.T) {
	svc, launcher, runner := newOpenWithService(t, workstreamAt(""),
		settings.OpenWithEntry{ID: "e1", Label: "VS Code", Command: "code ."})

	_, err := openWith(t, svc, "e1")
	if !errors.Is(err, provider.ErrNoWorkingDir) {
		t.Errorf("error = %v; want ErrNoWorkingDir", err)
	}
	if launcher.calls != 0 || runner.calls != 0 {
		t.Error("nothing may launch without a directory")
	}
}

func TestOpenWithReportsALaunchFailure(t *testing.T) {
	svc, _, runner := newOpenWithService(t, workstreamAt(t.TempDir()),
		settings.OpenWithEntry{ID: "e1", Label: "VS Code", Command: "code ."})
	runner.fail = errors.New("fork failed")

	_, err := openWith(t, svc, "e1")
	if !errors.Is(err, provider.ErrOpenWithFailed) {
		t.Fatalf("error = %v; want ErrOpenWithFailed", err)
	}
	if !strings.Contains(err.Error(), "VS Code") {
		t.Errorf("error = %v; want it to name the entry", err)
	}
}

// The same, for a terminal-wrapped row whose emulator fails to open.
func TestOpenWithReportsATerminalLaunchFailure(t *testing.T) {
	svc, launcher, _ := newOpenWithService(t, workstreamAt(t.TempDir()),
		settings.OpenWithEntry{ID: "e1", Label: "Neovim", Command: "nvim .", InTerminal: true})
	launcher.fail = errors.New("no terminal emulator found")

	_, err := openWith(t, svc, "e1")
	if !errors.Is(err, provider.ErrOpenWithFailed) {
		t.Fatalf("error = %v; want ErrOpenWithFailed", err)
	}
}

// With no settings store wired the shipped defaults answer.
func TestOpenWithFallsBackToTheDefaultEntries(t *testing.T) {
	dir := t.TempDir()
	st := store.NewTestStore(t, []domain.Project{{
		ID: "p1", Name: "p1", Path: t.TempDir(), Workstreams: []domain.Workstream{workstreamAt(dir)},
	}})
	launcher := &recordingLauncher{}
	svc := provider.NewService(st, provider.NewRegistry(), 0, provider.WithTerminal(launcher))

	if _, err := openWith(t, svc, settings.DefaultTerminalEntryID); err != nil {
		t.Fatalf("OpenWorkstream: %v", err)
	}
	if launcher.calls != 1 {
		t.Errorf("terminal launched %d times; want 1", launcher.calls)
	}
}

// The plain "Open" runs the user's default-open row, whatever it points at.
func TestDefaultOpenRunsTheDefaultEntry(t *testing.T) {
	dir := t.TempDir()
	svc, launcher, runner := newOpenWithService(t, workstreamAt(dir),
		settings.OpenWithEntry{
			ID: settings.DefaultTerminalEntryID, Label: "My editor",
			Command: "code .", InTerminal: false,
		})

	// No mode at all: the default open.
	res, err := svc.OpenWorkstream(context.Background(),
		provider.WorkstreamRef{ProjectID: "p1", WorkstreamID: "ws-1"}, "")
	if err != nil {
		t.Fatalf("OpenWorkstream: %v", err)
	}
	if runner.calls != 1 || runner.command != "code ." || runner.dir != dir {
		t.Errorf("runner ran %q in %q (%d calls); want `code .` in %q once",
			runner.command, runner.dir, runner.calls, dir)
	}
	if launcher.calls != 0 {
		t.Errorf("terminal launched %d times; want none", launcher.calls)
	}
	if res.Mode != domain.OpenWithModeFor(settings.DefaultTerminalEntryID) {
		t.Errorf("mode = %q; want the default row's own mode", res.Mode)
	}
}

func TestDefaultOpenWithTheShippedRowIsTheTerminal(t *testing.T) {
	dir := t.TempDir()
	svc, launcher, runner := newOpenWithService(t, workstreamAt(dir),
		settings.OpenWithEntry{
			ID: settings.DefaultTerminalEntryID, Label: "Terminal", InTerminal: true,
		})

	res, err := svc.OpenWorkstream(context.Background(),
		provider.WorkstreamRef{ProjectID: "p1", WorkstreamID: "ws-1"}, "")
	if err != nil {
		t.Fatalf("OpenWorkstream: %v", err)
	}
	if res.Mode != domain.OpenModeTerminal {
		t.Errorf("mode = %q; want the terminal hand-off", res.Mode)
	}
	if launcher.calls != 1 {
		t.Errorf("terminal launched %d times; want 1", launcher.calls)
	}
	if runner.calls != 0 {
		t.Errorf("detached runner called %d times; want none", runner.calls)
	}
}

// A client that asks for a terminal gets one, not the repurposed default row.
func TestExplicitTerminalModeIgnoresARepurposedDefaultRow(t *testing.T) {
	dir := t.TempDir()
	svc, launcher, runner := newOpenWithService(t, workstreamAt(dir),
		settings.OpenWithEntry{
			ID: settings.DefaultTerminalEntryID, Label: "My editor",
			Command: "code .", InTerminal: false,
		})

	if _, err := svc.OpenWorkstream(context.Background(),
		provider.WorkstreamRef{ProjectID: "p1", WorkstreamID: "ws-1"},
		domain.OpenModeTerminal); err != nil {
		t.Fatalf("OpenWorkstream: %v", err)
	}
	if launcher.calls != 1 {
		t.Errorf("terminal launched %d times; want 1", launcher.calls)
	}
	if runner.calls != 0 {
		t.Errorf("detached runner called %d times; want none", runner.calls)
	}
}
