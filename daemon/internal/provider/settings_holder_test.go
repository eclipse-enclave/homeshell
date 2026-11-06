package provider

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/settings"
	"github.com/eclipsesource/homeshell/daemon/internal/terminal"
)

func TestSettingsHolderRefreshInterval(t *testing.T) {
	var h SettingsHolder
	// No settings wired: reported as 0 so WatchLoop falls back to its default.
	if got := h.RefreshInterval(); got != 0 {
		t.Errorf("RefreshInterval with no settings = %v, want 0", got)
	}

	st, err := settings.Load(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	next := settings.Defaults()
	next.Providers.RefreshIntervalSec = 0.25
	if _, err := st.Set(next); err != nil {
		t.Fatal(err)
	}
	h.SetSettings(st)
	if got := h.RefreshInterval(); got != 250*time.Millisecond {
		t.Errorf("RefreshInterval = %v, want 250ms (from settings)", got)
	}

	// Read live: a later settings change is reflected without re-wiring.
	next.Providers.RefreshIntervalSec = 1
	if _, err := st.Set(next); err != nil {
		t.Fatal(err)
	}
	if got := h.RefreshInterval(); got != time.Second {
		t.Errorf("RefreshInterval after change = %v, want 1s", got)
	}
}

// A terminal window opens in the emulator chosen when it is opened, not the one
// chosen when the launcher was built.
func TestSettingsHolderTerminalPreference(t *testing.T) {
	var holder SettingsHolder
	if got := holder.TerminalPreference(); got != (terminal.Preference{Emulator: terminal.Auto}) {
		t.Errorf("TerminalPreference with no settings = %+v; want auto", got)
	}

	settingsStore, err := settings.Load(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	holder.SetSettings(settingsStore)
	want := terminal.Preference{Emulator: terminal.Custom, Command: "foot -e {command}"}
	next := settings.Defaults()
	next.Terminal = want
	if _, err := settingsStore.Set(next); err != nil {
		t.Fatal(err)
	}
	if got := holder.TerminalPreference(); got != want {
		t.Errorf("TerminalPreference = %+v; want %+v", got, want)
	}
}

func TestSettingsHolderOpenWithEntries(t *testing.T) {
	var holder SettingsHolder
	if got, want := holder.OpenWithEntries(), settings.Defaults().OpenWith.Entries; !slices.Equal(got, want) {
		t.Errorf("OpenWithEntries with no settings = %+v; want the defaults %+v", got, want)
	}

	settingsStore, err := settings.Load(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	holder.SetSettings(settingsStore)
	next := settings.Defaults()
	next.OpenWith.Entries = append(next.OpenWith.Entries, settings.OpenWithEntry{ID: "editor", Label: "Editor", Command: "code {dir}"})
	stored, err := settingsStore.Set(next)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := holder.OpenWithEntries(), stored.OpenWith.Entries; len(got) != 2 || !slices.Equal(got, want) {
		t.Errorf("OpenWithEntries = %+v; want %+v", got, want)
	}
}

func TestCaptureContextTracksPollingAndHonorsParentDeadline(t *testing.T) {
	for _, tc := range []struct {
		interval       float64
		parent, budget time.Duration
	}{
		{0, 0, 500 * time.Millisecond},
		{0.1, 0, 500 * time.Millisecond},
		{0.25, 0, 500 * time.Millisecond},
		{0.5, 0, 500 * time.Millisecond},
		{0.7, 0, 700 * time.Millisecond},
		{60, 0, time.Second},
		{0.5, 20 * time.Millisecond, 20 * time.Millisecond},
	} {
		var holder SettingsHolder
		if tc.interval != 0 {
			st, err := settings.Load(filepath.Join(t.TempDir(), "settings.json"))
			if err != nil {
				t.Fatal(err)
			}
			next := settings.Defaults()
			next.Providers.RefreshIntervalSec = tc.interval
			if _, err := st.Set(next); err != nil {
				t.Fatal(err)
			}
			holder.SetSettings(st)
		}
		parent := context.Background()
		var parentCancel context.CancelFunc
		if tc.parent != 0 {
			parent, parentCancel = context.WithTimeout(parent, tc.parent)
			defer parentCancel()
		}
		before := time.Now()
		capture, cancel := holder.CaptureContext(parent)
		after := time.Now()
		deadline, ok := capture.Deadline()
		if parentDeadline, inherited := parent.Deadline(); inherited {
			if !ok || !deadline.Equal(parentDeadline) {
				t.Fatal("capture floor extended the parent's deadline")
			}
		} else if !ok || deadline.Before(before.Add(tc.budget)) || deadline.After(after.Add(tc.budget)) {
			t.Fatalf("capture deadline=%s budget=%s", deadline.Sub(before), tc.budget)
		}
		cancel()
		if capture.Err() != context.Canceled {
			t.Fatal("capture cancellation did not propagate")
		}
	}
}
