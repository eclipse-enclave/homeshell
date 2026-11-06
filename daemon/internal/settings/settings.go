// Package settings holds the daemon-persisted user settings, stored as
// settings.json beside the state file. The daemon owns the schema, validates
// writes, and persists atomically.
package settings

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/pathutil"
	"github.com/eclipsesource/homeshell/daemon/internal/persist"
	"github.com/eclipsesource/homeshell/daemon/internal/terminal"
)

// fileName is the well-known settings file inside the data directory.
const fileName = "settings.json"

// schemaVersion is bumped whenever the on-disk format changes in a way that
// requires migration logic.
const schemaVersion = 2

// Defaultable is a setting that is either a fixed value or "remember the last
// used value": when UseLastUsed is set and a LastUsed value is recorded, that
// value is the effective default.
type Defaultable[T any] struct {
	Value       T    `json:"value"`
	UseLastUsed bool `json:"useLastUsed"`
	LastUsed    *T   `json:"lastUsed,omitempty"`
}

// Settings is the full set of daemon-persisted client settings, grouped by the
// domain concept each applies to.
type Settings struct {
	Projects      ProjectsSettings      `json:"projects"`
	Workstreams   WorkstreamsSettings   `json:"workstreams"`
	Sessions      SessionsSettings      `json:"sessions"`
	Providers     ProvidersSettings     `json:"providers"`
	Notifications NotificationsSettings `json:"notifications"`
	OpenWith      OpenWithSettings      `json:"openWith"`
	// Terminal is the emulator every terminal window the daemon opens comes up
	// in: a session attach, a workstream shell, an "open with" entry run in a
	// terminal.
	Terminal terminal.Preference `json:"terminal"`
}

// OpenWithSettings is the user's "open with" list: the commands a workstream
// can be handed to. Every entry becomes an open mode on every openable
// workstream; nothing is discovered on the host.
type OpenWithSettings struct {
	Entries []OpenWithEntry `json:"entries"`
}

// OpenWithEntry is one row of the user's "open with" list. Command is a shell
// line run with the workstream's directory as its working directory; an empty
// command means an interactive shell and is only meaningful with InTerminal.
type OpenWithEntry struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Command string `json:"command"`
	// InTerminal runs the command inside a terminal-emulator window on the
	// daemon host, for programs that need a terminal around them. Off, the
	// command is started detached.
	InTerminal bool `json:"inTerminal"`
	// Fixed marks the row the plain "Open" runs, which cannot be removed. The
	// daemon owns it: normalize sets it, and a value sent by a client is
	// replaced. Clients read it to decide whether to offer a remove action,
	// rather than knowing which id is special.
	Fixed bool `json:"fixed"`
}

// DefaultTerminalEntryID is the id of the row the plain "Open" runs. It always
// exists: normalize restores it when a document arrives without one. Everything
// about the row except its existence is the user's to change.
const DefaultTerminalEntryID = "terminal"

// defaultTerminalEntry is the shipped default open: an interactive shell in
// this host's terminal.
func defaultTerminalEntry() OpenWithEntry {
	return OpenWithEntry{
		ID: DefaultTerminalEntryID, Label: "Terminal",
		Command: "", InTerminal: true, Fixed: true,
	}
}

type ProjectsSettings struct {
	RememberLastSelected bool   `json:"rememberLastSelected"`
	SidebarSort          string `json:"sidebarSort"`
	ConfirmRemove        bool   `json:"confirmRemove"`
}

type WorkstreamsSettings struct {
	DefaultProvider Defaultable[string] `json:"defaultProvider"`
	DefaultStrategy Defaultable[string] `json:"defaultStrategy"`
	NameTemplate    string              `json:"nameTemplate"`
	Location        string              `json:"location"`
	ConfirmRemove   bool                `json:"confirmRemove"`
}

type SessionsSettings struct {
	// DefaultAgent is which agent a new session defaults to; the agent picks its
	// own model, so no default model is stored.
	DefaultAgent Defaultable[string] `json:"defaultAgent"`
	// PreviewLines is how many terminal-tail lines an expanded session row shows,
	// bounded by the daemon's captured screen (1..24).
	PreviewLines int `json:"previewLines"`
}

// maxPreviewLines bounds the session-preview line preference. It matches the
// daemon's tail capture (detect.TailLines), the most a client can be given.
const maxPreviewLines = 24

type ProvidersSettings struct {
	// RefreshIntervalSec is how often the provider re-checks each session's
	// state, in seconds. Fractional values are allowed, bounded by MinRefreshSec
	// and MaxRefreshSec.
	RefreshIntervalSec float64 `json:"refreshIntervalSec"`
}

// MinRefreshSec is the smallest accepted refresh interval: 0.1 s. The floor
// bounds provider load, including providers that still use subprocesses.
const MinRefreshSec = 0.1

// MaxRefreshSec is the largest accepted refresh interval: 24 h. The ceiling
// keeps RefreshInterval's multiplication by time.Second clear of saturating a
// time.Duration, which would stop the watcher's timer firing at all.
const MaxRefreshSec float64 = 24 * 60 * 60

// RefreshInterval is the configured session-state refresh interval as a
// duration.
func (p ProvidersSettings) RefreshInterval() time.Duration {
	return time.Duration(p.RefreshIntervalSec * float64(time.Second))
}

// NotificationsSettings controls activity notifications. Style selects the
// delivery channels ("native", "in-app", "both" or "none"); Sound is the sound
// name to play whenever the style is not "none".
//
// Style is the global on/off; Waiting and Finished then gate the two
// notify-worthy transitions independently, each with its own "notify at all"
// and "make a sound" switch. Sound is therefore two decisions, not one: which
// sound (Sound) and for which events (the per-event Sound flags) — that split is
// what lets a user keep the alert for a session that needs input audible while
// a finished session only lands silently.
type NotificationsSettings struct {
	Style string `json:"style"`
	Sound string `json:"sound"`
	// Waiting is the alert for a session that has become ready for input.
	Waiting NotificationEvent `json:"waiting"`
	// Finished is the alert for a session that has finished its work.
	Finished NotificationEvent `json:"finished"`
	// QuietSeconds is how long a session must stay away from a state before its
	// return to it is announced again. Session state is detected from the
	// terminal screen, which can flap (a working agent whose title momentarily
	// stops matching reads as idle for one poll, then works again), and each
	// flap would otherwise be an alert. Measuring the absence rather than the
	// time since the last alert keeps short real turns audible. 0 disables the
	// quiet period. Bounded by MaxQuietSec.
	QuietSeconds float64 `json:"quietSeconds"`
}

// NotificationEvent is the per-event half of the notification settings: whether
// this kind of activity raises a notification at all, and whether that
// notification is accompanied by a sound. Sound only applies when Notify is set
// — a notification that is never raised cannot be heard.
type NotificationEvent struct {
	Notify bool `json:"notify"`
	Sound  bool `json:"sound"`
}

// MaxQuietSec bounds the notification quiet period at an hour. A ceiling is
// needed for the same reason the refresh interval has one: the value becomes a
// time.Duration, and an unbounded one would suppress every repeat alert for
// effectively ever, which reads as "notifications are broken".
const MaxQuietSec float64 = 60 * 60

// QuietPeriod is the configured quiet period as a duration. Zero means no
// suppression.
func (n NotificationsSettings) QuietPeriod() time.Duration {
	return time.Duration(n.QuietSeconds * float64(time.Second))
}

// Notification styles. These values are the contract with the web UI and the
// notify package.
const (
	NotifyStyleNone   = "none"
	NotifyStyleInApp  = "in-app"
	NotifyStyleNative = "native"
	NotifyStyleBoth   = "both"
)

// ValidNotifyStyle reports whether a string is one of the four notification
// styles. Exported because the style also arrives outside a settings document —
// POST /api/notifications/test carries the style to demonstrate — and both
// paths must accept exactly the same vocabulary.
func ValidNotifyStyle(style string) bool {
	switch style {
	case NotifyStyleNone, NotifyStyleInApp, NotifyStyleNative, NotifyStyleBoth:
		return true
	default:
		return false
	}
}

// Defaults returns the settings a fresh daemon serves before anything is saved.
// Empty workstream defaults defer to a provider's own schema default.
func Defaults() Settings {
	return Settings{
		Projects: ProjectsSettings{
			RememberLastSelected: true,
			SidebarSort:          "insertion",
			ConfirmRemove:        true,
		},
		Workstreams: WorkstreamsSettings{
			DefaultProvider: Defaultable[string]{Value: ""},
			DefaultStrategy: Defaultable[string]{Value: ""},
			NameTemplate:    "",
			Location:        "",
			ConfirmRemove:   true,
		},
		Sessions: SessionsSettings{
			DefaultAgent: Defaultable[string]{Value: "claude"},
			PreviewLines: 20,
		},
		Providers: ProvidersSettings{
			RefreshIntervalSec: 0.5,
		},
		Notifications: NotificationsSettings{
			Style:    NotifyStyleNative,
			Sound:    "default",
			Waiting:  NotificationEvent{Notify: true, Sound: true},
			Finished: NotificationEvent{Notify: true, Sound: true},
			// Keep notification suppression independent of the polling cadence.
			QuietSeconds: 8,
		},
		OpenWith: OpenWithSettings{
			Entries: []OpenWithEntry{defaultTerminalEntry()},
		},
		Terminal: terminal.Preference{Emulator: terminal.Auto},
	}
}

// ValidationError wraps an error from Validate: a rejection caused by the
// caller's input rather than a persistence failure. The API layer maps it to
// 400 and everything else to 500.
type ValidationError struct{ Err error }

func (e *ValidationError) Error() string { return e.Err.Error() }
func (e *ValidationError) Unwrap() error { return e.Err }

// Validate rejects settings the daemon cannot accept: an unknown sidebar sort,
// intervals and ports outside their sane ranges, a workstream location that
// does not resolve, or a terminal choice the launcher cannot act on. The name
// template remains free-form.
func (s Settings) Validate() error {
	if s.Projects.SidebarSort != "insertion" && s.Projects.SidebarSort != "name" {
		return fmt.Errorf(`projects.sidebarSort must be "insertion" or "name", got %q`, s.Projects.SidebarSort)
	}
	// An empty location defers to the default root, and a relative one resolves
	// against each project at create time. A ~ or absolute one must resolve now,
	// so ~otheruser/... surfaces on save.
	if loc := strings.TrimSpace(s.Workstreams.Location); pathutil.IsAnchored(loc) {
		if _, err := pathutil.Resolve(loc); err != nil {
			return fmt.Errorf("workstreams.location %w", err)
		}
	}
	if s.Providers.RefreshIntervalSec < MinRefreshSec || s.Providers.RefreshIntervalSec > MaxRefreshSec {
		return fmt.Errorf("providers.refreshIntervalSec must be between %g and %g, got %g", MinRefreshSec, MaxRefreshSec, s.Providers.RefreshIntervalSec)
	}
	if s.Sessions.PreviewLines < 1 || s.Sessions.PreviewLines > maxPreviewLines {
		return fmt.Errorf("sessions.previewLines must be between 1 and %d, got %d", maxPreviewLines, s.Sessions.PreviewLines)
	}
	if !ValidNotifyStyle(s.Notifications.Style) {
		return fmt.Errorf(`notifications.style must be "none", "in-app", "native", or "both", got %q`, s.Notifications.Style)
	}
	if q := s.Notifications.QuietSeconds; q < 0 || q > MaxQuietSec {
		return fmt.Errorf("notifications.quietSeconds must be between 0 and %g, got %g", MaxQuietSec, q)
	}
	seenEntryIDs := map[string]bool{}
	for i, entry := range s.OpenWith.Entries {
		if strings.TrimSpace(entry.Label) == "" {
			return fmt.Errorf("openWith.entries[%d]: label must not be empty", i)
		}
		// An empty command only means something inside a terminal window;
		// detached it would start nothing.
		if strings.TrimSpace(entry.Command) == "" && !entry.InTerminal {
			return fmt.Errorf("openWith.entries[%d] (%s): command must not be empty unless the entry runs in a terminal", i, entry.Label)
		}
		if entry.ID != "" && seenEntryIDs[entry.ID] {
			return fmt.Errorf("openWith.entries[%d] (%s): duplicate id %q", i, entry.Label, entry.ID)
		}
		seenEntryIDs[entry.ID] = true
	}
	return validateTerminal(s.Terminal)
}

// validateTerminal rejects a terminal choice the launcher cannot act on: an
// emulator this platform does not list, or a custom template with nowhere to
// put the command.
func validateTerminal(preference terminal.Preference) error {
	if !terminal.Supported(preference.Emulator) {
		return fmt.Errorf(`terminal.emulator must be "auto", "custom", or an emulator this platform supports, got %q`, preference.Emulator)
	}
	if preference.Emulator == terminal.Custom && !strings.Contains(preference.Command, terminal.CommandPlaceholder) {
		return fmt.Errorf("terminal.command must contain %s where the command goes", terminal.CommandPlaceholder)
	}
	return nil
}

// normalize brings a settings document into stored form: open-with entries and
// the terminal command are trimmed, and any entry without an id is assigned
// one, so every entry is addressable as an open mode. Called on every write and
// on load.
func (s *Settings) normalize() error {
	s.Terminal.Command = strings.TrimSpace(s.Terminal.Command)
	// Clients are promised an array, never null.
	if s.OpenWith.Entries == nil {
		s.OpenWith.Entries = []OpenWithEntry{}
	}
	// Restore the default-open row rather than trust it to be there, so the
	// plain "Open" always has something behind it. An existing one keeps its
	// place and contents.
	if !hasEntry(s.OpenWith.Entries, DefaultTerminalEntryID) {
		s.OpenWith.Entries = append([]OpenWithEntry{defaultTerminalEntry()}, s.OpenWith.Entries...)
	}
	for i := range s.OpenWith.Entries {
		entry := &s.OpenWith.Entries[i]
		entry.Label = strings.TrimSpace(entry.Label)
		entry.Command = strings.TrimSpace(entry.Command)
		entry.ID = strings.TrimSpace(entry.ID)
		if entry.ID == "" {
			id, err := newEntryID()
			if err != nil {
				return err
			}
			entry.ID = id
		}
		entry.Fixed = entry.ID == DefaultTerminalEntryID
	}
	return nil
}

// hasEntry reports whether the list holds an entry with the given id.
func hasEntry(entries []OpenWithEntry, id string) bool {
	for _, entry := range entries {
		if strings.TrimSpace(entry.ID) == id {
			return true
		}
	}
	return false
}

// newEntryID produces a fresh open-with entry id.
func newEntryID() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generating an open-with entry id: %w", err)
	}
	return fmt.Sprintf("open-with-%x", buf), nil
}

// DefaultPath resolves the settings file location: the HOMESHELL_SETTINGS_FILE
// override if set (used by tests), otherwise settings.json in the platform data
// directory beside the state file.
func DefaultPath() (string, error) {
	if p := os.Getenv("HOMESHELL_SETTINGS_FILE"); p != "" {
		return p, nil
	}
	dir, err := persist.DefaultDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, fileName), nil
}

// diskFile is the on-disk envelope: a schema version, the settings payload, and
// the persisted log level. The stored log level is only a startup default; the
// live one is owned by /api/config.
type diskFile struct {
	SchemaVersion int             `json:"schemaVersion"`
	Settings      json.RawMessage `json:"settings"`
	LogLevel      string          `json:"logLevel,omitempty"`
}

// Store holds the current settings and persists changes to a file. It is safe
// for concurrent use.
type Store struct {
	mu       sync.Mutex
	path     string
	cur      Settings
	logLevel string
}

// Load reads settings from path, falling back to Defaults when the file does
// not exist. The stored JSON is merged over Defaults, so missing keys keep
// their default. A corrupt file or unsupported schema version is an error.
func Load(path string) (*Store, error) {
	s := &Store{path: path, cur: Defaults()}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s, nil
		}
		return nil, fmt.Errorf("reading settings file %s: %w", path, err)
	}

	var disk diskFile
	if err := json.Unmarshal(data, &disk); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if disk.SchemaVersion != 1 && disk.SchemaVersion != schemaVersion {
		return nil, fmt.Errorf(
			"%s: unsupported schema version %d (expected 1 or %d); "+
				"a future HomeShell version may have written this file",
			path, disk.SchemaVersion, schemaVersion,
		)
	}
	// Unmarshal the stored payload onto a defaulted value, so keys absent from
	// the file keep their default.
	cur := Defaults()
	if len(disk.Settings) > 0 {
		if err := json.Unmarshal(disk.Settings, &cur); err != nil {
			return nil, fmt.Errorf("parsing settings in %s: %w", path, err)
		}
	}
	// Migrated in memory only: the next save writes v2, so a user who then
	// explicitly chooses 5 seconds keeps that choice.
	if disk.SchemaVersion == 1 && cur.Providers.RefreshIntervalSec == 5 {
		cur.Providers.RefreshIntervalSec = Defaults().Providers.RefreshIntervalSec
	}
	// Reads are normalized and validated too, so a hand-edited file cannot
	// bypass the constraints PUT enforces. An invalid one logs and falls back to
	// defaults rather than failing startup.
	if err := cur.normalize(); err != nil {
		return nil, err
	}
	// A settings file can come from another platform, naming an emulator this
	// one lacks. An invalid terminal choice falls back alone rather than taking
	// the rest of the document with it.
	if err := validateTerminal(cur.Terminal); err != nil {
		slog.Warn("stored terminal setting is invalid; using auto", "path", path, "error", err)
		cur.Terminal = Defaults().Terminal
	}
	if err := cur.Validate(); err != nil {
		slog.Warn("stored settings failed validation; falling back to defaults", "path", path, "error", err)
		cur = Defaults()
	}
	s.cur = cur
	s.logLevel = disk.LogLevel
	return s, nil
}

// LogLevel returns the persisted log level, or "" when none is stored (the
// daemon then follows the flag/env/default precedence at startup).
func (s *Store) LogLevel() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.logLevel
}

// SetLogLevel persists the log level so it survives a restart. This is only the
// stored startup default; the live level is owned by the logging package.
func (s *Store) SetLogLevel(level string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev := s.logLevel
	s.logLevel = level
	if err := s.saveLocked(); err != nil {
		s.logLevel = prev
		return err
	}
	return nil
}

// Get returns the current settings.
func (s *Store) Get() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur
}

// Set normalizes, validates and persists the settings, returning the document
// as stored (normalization may have assigned ids). A validation failure returns
// a *ValidationError; either failure leaves the in-memory value unchanged, so
// memory never claims a value the disk does not hold.
func (s *Store) Set(next Settings) (Settings, error) {
	if err := next.normalize(); err != nil {
		return Settings{}, err
	}
	if err := next.Validate(); err != nil {
		return Settings{}, &ValidationError{Err: err}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	prev := s.cur
	s.cur = next
	if err := s.saveLocked(); err != nil {
		s.cur = prev
		return Settings{}, err
	}
	return next, nil
}

func (s *Store) saveLocked() error {
	payload, err := json.Marshal(s.cur)
	if err != nil {
		return fmt.Errorf("marshaling settings: %w", err)
	}
	data, err := json.MarshalIndent(diskFile{
		SchemaVersion: schemaVersion,
		Settings:      payload,
		LogLevel:      s.logLevel,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling settings file: %w", err)
	}
	return persist.WriteFileAtomic(s.path, append(data, '\n'))
}
