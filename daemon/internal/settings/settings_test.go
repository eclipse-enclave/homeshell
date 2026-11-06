package settings

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/terminal"
)

func TestLoadMissingFileReturnsDefaults(t *testing.T) {
	store, err := Load(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := store.Get(); !reflect.DeepEqual(got, Defaults()) {
		t.Errorf("Get() = %+v, want defaults", got)
	}
}

func TestSetPersistsAndReloads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	store, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	next := Defaults()
	next.Workstreams.NameTemplate = "{project}-ws"
	next.Sessions.PreviewLines = 12
	last := "worktree"
	next.Workstreams.DefaultStrategy = Defaultable[string]{Value: "copy", UseLastUsed: true, LastUsed: &last}
	if _, err := store.Set(next); err != nil {
		t.Fatalf("Set: %v", err)
	}

	if store.Get().Workstreams.NameTemplate != "{project}-ws" {
		t.Error("Get did not reflect the set value")
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got := reloaded.Get()
	if got.Workstreams.NameTemplate != "{project}-ws" || got.Sessions.PreviewLines != 12 {
		t.Errorf("reloaded = %+v, want template and port persisted", got)
	}
	if got.Workstreams.DefaultStrategy.LastUsed == nil || *got.Workstreams.DefaultStrategy.LastUsed != "worktree" {
		t.Errorf("reloaded lastUsed = %v, want worktree", got.Workstreams.DefaultStrategy.LastUsed)
	}
}

func TestSetRejectsInvalidSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	store, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	bad := Defaults()
	bad.Projects.SidebarSort = "sideways"
	if _, err := store.Set(bad); err == nil {
		t.Fatal("Set accepted an invalid sidebar sort")
	}
	if store.Get().Projects.SidebarSort != "insertion" {
		t.Error("invalid settings mutated the store")
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Error("invalid Set wrote a file")
	}
}

func TestValidate(t *testing.T) {
	tests := map[string]func(*Settings){
		"bad sort":           func(s *Settings) { s.Projects.SidebarSort = "nope" },
		"zero refresh":       func(s *Settings) { s.Providers.RefreshIntervalSec = 0 },
		"bad notify style":   func(s *Settings) { s.Notifications.Style = "email" },
		"empty notify style": func(s *Settings) { s.Notifications.Style = "" },
		"zero preview lines": func(s *Settings) { s.Sessions.PreviewLines = 0 },
		"too many preview lines": func(s *Settings) {
			s.Sessions.PreviewLines = 25
		},
		"negative quiet period": func(s *Settings) { s.Notifications.QuietSeconds = -1 },
		"quiet period beyond the ceiling": func(s *Settings) {
			s.Notifications.QuietSeconds = MaxQuietSec + 1
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			s := Defaults()
			mutate(&s)
			if err := s.Validate(); err == nil {
				t.Errorf("Validate accepted invalid settings (%s)", name)
			}
		})
	}
	if err := Defaults().Validate(); err != nil {
		t.Errorf("Validate rejected defaults: %v", err)
	}
}

func TestRefreshInterval(t *testing.T) {
	tests := map[string]struct {
		sec  float64
		want time.Duration
	}{
		"whole seconds":   {5, 5 * time.Second},
		"fractional":      {2.5, 2500 * time.Millisecond},
		"one millisecond": {0.001, time.Millisecond},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			p := ProvidersSettings{RefreshIntervalSec: tc.sec}
			if got := p.RefreshInterval(); got != tc.want {
				t.Errorf("RefreshInterval() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestValidateRefreshIntervalFractional(t *testing.T) {
	// Fractional seconds are allowed, between the 0.1 s floor and 24 h ceiling.
	for _, v := range []float64{0.1, 0.5, 1, 5, 3600, MaxRefreshSec} {
		s := Defaults()
		s.Providers.RefreshIntervalSec = v
		if err := s.Validate(); err != nil {
			t.Errorf("Validate rejected valid refreshIntervalSec=%v: %v", v, err)
		}
	}
	// An unbounded top would saturate the Duration so the watcher never fires.
	for _, v := range []float64{0, -1, 0.001, 0.05, MaxRefreshSec + 1, 1e10} {
		s := Defaults()
		s.Providers.RefreshIntervalSec = v
		if err := s.Validate(); err == nil {
			t.Errorf("Validate accepted out-of-range refreshIntervalSec=%v", v)
		}
	}
}

func TestValidateQuietSeconds(t *testing.T) {
	// Zero is legal and means "no suppression"; the ceiling is inclusive.
	for _, v := range []float64{0, 0.5, 15, 300, MaxQuietSec} {
		s := Defaults()
		s.Notifications.QuietSeconds = v
		if err := s.Validate(); err != nil {
			t.Errorf("Validate rejected valid notifications.quietSeconds=%v: %v", v, err)
		}
	}
	for _, v := range []float64{-0.1, -30, MaxQuietSec + 1, 1e10} {
		s := Defaults()
		s.Notifications.QuietSeconds = v
		if err := s.Validate(); err == nil {
			t.Errorf("Validate accepted out-of-range notifications.quietSeconds=%v", v)
		}
	}
}

func TestQuietPeriod(t *testing.T) {
	tests := map[string]struct {
		sec  float64
		want time.Duration
	}{
		"disabled":      {0, 0},
		"whole seconds": {15, 15 * time.Second},
		"fractional":    {2.5, 2500 * time.Millisecond},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			n := NotificationsSettings{QuietSeconds: tc.sec}
			if got := n.QuietPeriod(); got != tc.want {
				t.Errorf("QuietPeriod() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestDefaultsNotifyOnBothEvents pins the shipped notification defaults: both
// notify-worthy events are announced, audibly, with a quiet period holding back
// repeats. A zero value here would mean a fresh daemon never notifies at all.
func TestDefaultsNotifyOnBothEvents(t *testing.T) {
	n := Defaults().Notifications
	if !n.Waiting.Notify || !n.Waiting.Sound {
		t.Errorf("default waiting event = %+v, want notify and sound", n.Waiting)
	}
	if !n.Finished.Notify || !n.Finished.Sound {
		t.Errorf("default finished event = %+v, want notify and sound", n.Finished)
	}
	if n.QuietSeconds <= 0 {
		t.Errorf("default quietSeconds = %v, want a non-zero window", n.QuietSeconds)
	}
}

// TestLoadKeepsNotificationDefaultsForAbsentKeys covers the upgrade path: a
// settings.json written before the per-event switches existed must not read as
// "every event off" — the merge over Defaults has to leave them on.
func TestLoadKeepsNotificationDefaultsForAbsentKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	old := `{"schemaVersion":1,"settings":{"notifications":{"style":"in-app","sound":"chime"}}}`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := store.Get().Notifications
	if got.Style != "in-app" || got.Sound != "chime" {
		t.Errorf("stored style/sound = %q/%q, want in-app/chime", got.Style, got.Sound)
	}
	if !got.Waiting.Notify || !got.Finished.Notify {
		t.Errorf("absent per-event keys = %+v/%+v, want the defaults kept", got.Waiting, got.Finished)
	}
	if got.QuietSeconds != Defaults().Notifications.QuietSeconds {
		t.Errorf("quietSeconds = %v, want the default kept", got.QuietSeconds)
	}
}

func TestValidateLocation(t *testing.T) {
	// Empty defers to the default root; a relative one is taken from the project.
	valid := []string{"", "  ", "/tmp/homeshell/ws", "~/homeshell", "relative/path", "./here", "../up"}
	for _, loc := range valid {
		s := Defaults()
		s.Workstreams.Location = loc
		if err := s.Validate(); err != nil {
			t.Errorf("Validate rejected valid workstreams.location=%q: %v", loc, err)
		}
	}
	s := Defaults()
	s.Workstreams.Location = "~other/worktrees"
	if err := s.Validate(); err == nil {
		t.Error("Validate accepted another user's home as workstreams.location")
	}
}

func TestLoadMergesOverDefaults(t *testing.T) {
	// A file missing newer keys keeps their defaults for the absent ones.
	path := filepath.Join(t.TempDir(), "settings.json")
	partial := `{"schemaVersion":1,"settings":{"sessions":{"previewLines":12}}}`
	if err := os.WriteFile(path, []byte(partial), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := store.Get()
	if got.Sessions.PreviewLines != 12 {
		t.Errorf("previewLines = %d, want 12 (from file)", got.Sessions.PreviewLines)
	}
	if got.Providers.RefreshIntervalSec != 0.5 {
		t.Errorf("refreshIntervalSec = %v, want 0.5 (default kept)", got.Providers.RefreshIntervalSec)
	}
	if got.Projects.SidebarSort != "insertion" {
		t.Errorf("sidebarSort = %q, want insertion (default kept)", got.Projects.SidebarSort)
	}
}

func TestSetInvalidReturnsValidationError(t *testing.T) {
	store, err := Load(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	bad := Defaults()
	bad.Sessions.PreviewLines = 0
	var invalid *ValidationError
	if _, err := store.Set(bad); !errors.As(err, &invalid) {
		t.Fatalf("Set error = %v, want *ValidationError (client's fault → 400)", err)
	}
}

func TestSetKeepsMemoryWhenWriteFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	store, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	// An unwritable directory fails the atomic write at temp-file creation.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })

	next := Defaults()
	next.Workstreams.NameTemplate = "should-not-stick"
	_, err = store.Set(next)
	if err == nil {
		t.Fatal("expected a write error")
	}
	var invalid *ValidationError
	if errors.As(err, &invalid) {
		t.Fatal("a write failure must not be a *ValidationError")
	}
	// Memory must not claim a value the disk does not hold.
	if store.Get().Workstreams.NameTemplate != "" {
		t.Error("in-memory settings changed despite a failed write")
	}
}

func TestLoadValidatesAndFallsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	// previewLines:0 is out of range; a read must not bypass a write's constraints.
	bad := `{"schemaVersion":1,"settings":{"sessions":{"previewLines":0}}}`
	if err := os.WriteFile(path, []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := store.Get().Sessions.PreviewLines; got != Defaults().Sessions.PreviewLines {
		t.Errorf("previewLines = %d, want default %d (invalid stored value must not load)", got, Defaults().Sessions.PreviewLines)
	}
}

func TestLogLevelPersistsAndSurvivesSettingsWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	store, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if store.LogLevel() != "" {
		t.Errorf("fresh LogLevel = %q, want empty", store.LogLevel())
	}
	if err := store.SetLogLevel("debug"); err != nil {
		t.Fatalf("SetLogLevel: %v", err)
	}
	// A settings-document write must not clobber the separately-persisted level.
	if _, err := store.Set(Defaults()); err != nil {
		t.Fatalf("Set: %v", err)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.LogLevel() != "debug" {
		t.Errorf("reloaded LogLevel = %q, want debug", reloaded.LogLevel())
	}
}

func TestLoadRejectsUnknownSchemaVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"schemaVersion":999,"settings":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load accepted an unknown schema version")
	}
}

func TestDefaultsShipTheTerminalEntry(t *testing.T) {
	entries := Defaults().OpenWith.Entries
	if len(entries) != 1 {
		t.Fatalf("default entries = %d, want exactly the terminal entry", len(entries))
	}
	entry := entries[0]
	if entry.ID != DefaultTerminalEntryID || entry.Label != "Terminal" || entry.Command != "" || !entry.InTerminal {
		t.Errorf("default terminal entry = %+v", entry)
	}
}

func TestValidateOpenWithEntries(t *testing.T) {
	invalid := map[string]func(*Settings){
		"empty label": func(s *Settings) {
			s.OpenWith.Entries = append(s.OpenWith.Entries, OpenWithEntry{ID: "a", Label: " ", Command: "code ."})
		},
		"empty command outside a terminal": func(s *Settings) {
			s.OpenWith.Entries = append(s.OpenWith.Entries, OpenWithEntry{ID: "a", Label: "Broken", Command: "", InTerminal: false})
		},
		"duplicate ids": func(s *Settings) {
			s.OpenWith.Entries = append(s.OpenWith.Entries,
				OpenWithEntry{ID: "a", Label: "One", Command: "code ."},
				OpenWithEntry{ID: "a", Label: "Two", Command: "vim .", InTerminal: true})
		},
	}
	for name, mutate := range invalid {
		t.Run(name, func(t *testing.T) {
			s := Defaults()
			mutate(&s)
			if err := s.Validate(); err == nil {
				t.Errorf("Validate accepted invalid open-with entries (%s)", name)
			}
		})
	}
	// An empty list is valid; normalize is what puts the default-open row back.
	s := Defaults()
	s.OpenWith.Entries = nil
	if err := s.Validate(); err != nil {
		t.Errorf("Validate rejected an empty open-with list: %v", err)
	}
}

// The default-open row is what the plain "Open" runs, so a document without it
// must not leave that action with nothing behind it.
func TestNormalizeRestoresTheDefaultOpenRow(t *testing.T) {
	store, err := Load(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil {
		t.Fatal(err)
	}

	// A list that never mentions it: restored, at the front.
	next := Defaults()
	next.OpenWith.Entries = []OpenWithEntry{
		{ID: "e1", Label: "VS Code", Command: "code ."},
	}
	stored, err := store.Set(next)
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
	entries := stored.OpenWith.Entries
	if len(entries) != 2 || entries[0].ID != DefaultTerminalEntryID {
		t.Fatalf("entries = %+v; want the default-open row restored first", entries)
	}
	if entries[1].ID != "e1" {
		t.Errorf("the user's own rows must keep their order, got %+v", entries)
	}

	// An existing one is left exactly as the user has it.
	next = Defaults()
	next.OpenWith.Entries = []OpenWithEntry{
		{ID: "e1", Label: "VS Code", Command: "code ."},
		{ID: DefaultTerminalEntryID, Label: "My editor", Command: "code .", InTerminal: false},
	}
	stored, err = store.Set(next)
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
	entries = stored.OpenWith.Entries
	if len(entries) != 2 {
		t.Fatalf("entries = %+v; want the two rows given", entries)
	}
	if entries[1].ID != DefaultTerminalEntryID {
		t.Errorf("an existing default row must keep its place, got %+v", entries)
	}
	if entries[1].Label != "My editor" || entries[1].Command != "code ." || entries[1].InTerminal {
		t.Errorf("the default row's name and command are the user's to change, got %+v", entries[1])
	}
}

func TestSetNormalizesOpenWithEntries(t *testing.T) {
	store, err := Load(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	next := Defaults()
	next.OpenWith.Entries = append(next.OpenWith.Entries,
		OpenWithEntry{Label: "  VS Code  ", Command: " code . "},
		OpenWithEntry{Label: "Neovim", Command: "nvim .", InTerminal: true},
	)
	stored, err := store.Set(next)
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
	added := stored.OpenWith.Entries[1:]
	if added[0].Label != "VS Code" || added[0].Command != "code ." {
		t.Errorf("entry not trimmed: %+v", added[0])
	}
	seen := map[string]bool{DefaultTerminalEntryID: true}
	for _, entry := range added {
		if !strings.HasPrefix(entry.ID, "open-with-") {
			t.Errorf("entry %q id = %q, want a generated open-with-<hex> id", entry.Label, entry.ID)
		}
		if seen[entry.ID] {
			t.Errorf("duplicate generated id %q", entry.ID)
		}
		seen[entry.ID] = true
	}
	if got := store.Get(); !reflect.DeepEqual(got.OpenWith, stored.OpenWith) {
		t.Errorf("Get().OpenWith = %+v, want the stored %+v", got.OpenWith, stored.OpenWith)
	}
}

func TestLoadNormalizesStoredOpenWithEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	partial := `{"schemaVersion":1,"settings":{"openWith":{"entries":[{"label":"VS Code","command":"code ."}]}}}`
	if err := os.WriteFile(path, []byte(partial), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	entries := store.Get().OpenWith.Entries
	if len(entries) != 1 {
		t.Fatalf("entries = %+v, want exactly the stored row (stored list replaces the default)", entries)
	}
	if entries[0].ID == "" {
		t.Error("a hand-edited entry without an id must be assigned one on load")
	}
}

// Clients decide whether to offer a remove action from this flag rather than
// knowing which id is special, so the daemon owns it: it is set on the
// default-open row and cleared on every other, whatever a client sent.
func TestNormalizeOwnsTheFixedFlag(t *testing.T) {
	store, err := Load(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	next := Defaults()
	next.OpenWith.Entries = []OpenWithEntry{
		{ID: DefaultTerminalEntryID, Label: "Terminal", InTerminal: true, Fixed: false},
		{ID: "e1", Label: "VS Code", Command: "code .", Fixed: true},
	}
	stored, err := store.Set(next)
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
	if !stored.OpenWith.Entries[0].Fixed {
		t.Error("the default-open row must be marked fixed even when a client clears it")
	}
	if stored.OpenWith.Entries[1].Fixed {
		t.Error("an ordinary row must not be marked fixed even when a client sets it")
	}
}

func TestValidateTerminal(t *testing.T) {
	tests := []struct {
		name       string
		preference terminal.Preference
		wantErr    string
	}{
		{name: "auto", preference: terminal.Preference{Emulator: terminal.Auto}},
		{name: "a supported emulator", preference: terminal.Preference{Emulator: terminal.Emulators().Emulators[0].ID}},
		{name: "custom with the placeholder", preference: terminal.Preference{Emulator: terminal.Custom, Command: "foot -e {command}"}},
		{
			// The template is only read while custom is chosen.
			name:       "another emulator keeps an incomplete template",
			preference: terminal.Preference{Emulator: terminal.Auto, Command: "foot -e"},
		},
		{name: "custom without the placeholder", preference: terminal.Preference{Emulator: terminal.Custom, Command: "foot -e"}, wantErr: "terminal.command must contain {command}"},
		{name: "an unknown emulator", preference: terminal.Preference{Emulator: "no-such-terminal"}, wantErr: "terminal.emulator must be"},
		{name: "no emulator", preference: terminal.Preference{}, wantErr: "terminal.emulator must be"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := Defaults()
			candidate.Terminal = tt.preference
			err := candidate.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("Validate rejected %+v: %v", tt.preference, err)
				}
				return
			}
			if err == nil || !strings.HasPrefix(err.Error(), tt.wantErr) {
				t.Errorf("Validate(%+v) = %v; want an error starting %q", tt.preference, err, tt.wantErr)
			}
		})
	}
}

func TestSetTrimsTheTerminalCommand(t *testing.T) {
	store, err := Load(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	next := Defaults()
	next.Terminal = terminal.Preference{Emulator: terminal.Custom, Command: "  foot -e {command} "}
	stored, err := store.Set(next)
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
	if stored.Terminal.Command != "foot -e {command}" {
		t.Errorf("command = %q; want it trimmed", stored.Terminal.Command)
	}
}

// A settings file brought from another platform can name an emulator this one
// lacks, and a hand-edited one can hold a broken template; only the terminal
// choice falls back, not the whole document.
func TestLoadFallsBackAnInvalidTerminalChoiceAlone(t *testing.T) {
	tests := []struct {
		name     string
		terminal string
	}{
		{name: "an unsupported emulator", terminal: `{"emulator":"no-such-terminal","command":"foot -e {command}"}`},
		{name: "custom without the placeholder", terminal: `{"emulator":"custom","command":"foot -e"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.json")
			stored := `{"schemaVersion":1,"settings":{"sessions":{"previewLines":7},"terminal":` + tt.terminal + `}}`
			if err := os.WriteFile(path, []byte(stored), 0o600); err != nil {
				t.Fatal(err)
			}
			store, err := Load(path)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			got := store.Get()
			if got.Terminal != (terminal.Preference{Emulator: terminal.Auto}) {
				t.Errorf("terminal = %+v; want auto", got.Terminal)
			}
			if got.Sessions.PreviewLines != 7 {
				t.Errorf("previewLines = %d; want the stored 7 kept", got.Sessions.PreviewLines)
			}
		})
	}
}
