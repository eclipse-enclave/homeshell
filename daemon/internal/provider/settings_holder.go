package provider

import (
	"context"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/settings"
	"github.com/eclipsesource/homeshell/daemon/internal/terminal"
)

// SettingsHolder is an embeddable helper for providers that read the user's
// settings. Embedding it satisfies SettingsAware and supplies RefreshInterval
// for WatchLoop and TerminalPreference for the provider's terminal launcher.
type SettingsHolder struct {
	settings *settings.Store
}

// SetSettings wires the settings store (implements SettingsAware).
func (h *SettingsHolder) SetSettings(s *settings.Store) { h.settings = s }

// RefreshInterval is the configured watcher poll period, or 0 when no settings
// store is wired. Read live, so a settings change takes effect within one
// poll period.
func (h *SettingsHolder) RefreshInterval() time.Duration {
	if h.settings == nil {
		return 0
	}
	return h.settings.Get().Providers.RefreshInterval()
}

// CaptureContext bounds terminal reads to 500 ms through one second, independent
// of faster polling intervals. Lifecycle commands keep their own, longer timeouts.
// Parent deadlines still take precedence over the capture floor.
func (h *SettingsHolder) CaptureContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, min(max(h.RefreshInterval(), 500*time.Millisecond), time.Second))
}

// TerminalPreference is the user's terminal emulator, or the shipped default
// when no store is wired. Read at each launch, so a window opened after a
// settings change opens in the newly chosen emulator.
func (h *SettingsHolder) TerminalPreference() terminal.Preference {
	if h.settings == nil {
		return settings.Defaults().Terminal
	}
	return h.settings.Get().Terminal
}

// OpenWithEntries is the user's "open with" list, or the shipped default when
// no store is wired. Read live, like the others.
func (h *SettingsHolder) OpenWithEntries() []settings.OpenWithEntry {
	if h.settings == nil {
		return settings.Defaults().OpenWith.Entries
	}
	return h.settings.Get().OpenWith.Entries
}
