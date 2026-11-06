package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/procutil"
	"github.com/eclipsesource/homeshell/daemon/internal/settings"
	"github.com/eclipsesource/homeshell/daemon/internal/terminal"
)

// The user's "open with" rows live in the settings document
// (settings.OpenWithSettings); this file turns them into open modes and runs
// the command behind one. Nothing is discovered: the row states both what to
// run and how (its command and its InTerminal flag).

// CommandRunner starts a detached command in a directory, so opening an "open
// with" entry does not block on the program it starts.
type CommandRunner interface {
	Start(ctx context.Context, dir, command string) error
}

// OSCommandRunner is the production CommandRunner: the command is a shell line
// run via /bin/sh with the directory as its working directory, started detached
// and reaped.
type OSCommandRunner struct{}

// NewOSCommandRunner returns the production command runner.
func NewOSCommandRunner() CommandRunner { return OSCommandRunner{} }

// Start launches the shell line detached in dir.
func (OSCommandRunner) Start(_ context.Context, dir, command string) error {
	proc := exec.Command("/bin/sh", "-c", command)
	proc.Dir = dir
	return procutil.StartAndReap(proc)
}

// SetSettings wires the settings store the service reads the user's "open
// with" entries and terminal emulator from.
func (s *Service) SetSettings(store *settings.Store) { s.settings.SetSettings(store) }

// openWithEntries reads the user's list for one derivation pass.
func (s *Service) openWithEntries() []settings.OpenWithEntry {
	return s.settings.OpenWithEntries()
}

// openWithEntry finds the entry a mode names. An id not in the list is an
// unknown mode.
func (s *Service) openWithEntry(id string) (settings.OpenWithEntry, error) {
	for _, entry := range s.openWithEntries() {
		if entry.ID == id {
			return entry, nil
		}
	}
	return settings.OpenWithEntry{}, fmt.Errorf("%w: no open-with entry %q", ErrUnknownOpenMode, id)
}

// openWithCommand runs one of the user's own entries on the workstream. The
// launch is fire-and-forget: the daemon reports that the program started, not
// what it did.
func (s *Service) openWithCommand(ctx context.Context, ws domain.Workstream, entryID string) (OpenResult, error) {
	entry, err := s.openWithEntry(entryID)
	if err != nil {
		return OpenResult{}, err
	}
	return s.runEntry(ctx, ws, entry)
}

// openDefault is the plain "Open": it runs the always-present terminal row
// (settings.DefaultTerminalEntryID), falling back to a shell in the working
// directory when the list has no such row.
func (s *Service) openDefault(ctx context.Context, ws domain.Workstream) (OpenResult, error) {
	entry, err := s.openWithEntry(settings.DefaultTerminalEntryID)
	if err != nil {
		return s.openWorkingDir(ctx, ws)
	}
	return s.runEntry(ctx, ws, entry)
}

// runEntry performs one entry's hand-off on the workstream.
func (s *Service) runEntry(ctx context.Context, ws domain.Workstream, entry settings.OpenWithEntry) (OpenResult, error) {
	// An empty command is a shell in the working directory on this host, even
	// when a session runs in a container: its shell is a mode of its own.
	if entry.Command == "" {
		return s.openWorkingDir(ctx, ws)
	}
	dir, err := s.materializedDir(ws)
	if err != nil {
		return OpenResult{}, err
	}
	if err := s.launchEntry(ctx, entry, dir, ws.Name); err != nil {
		return OpenResult{}, fmt.Errorf("%w: %s: %v", ErrOpenWithFailed, entry.Label, err)
	}
	data, err := json.Marshal(map[string]string{
		"target": "host", "workingDir": dir, "command": entry.Command,
	})
	if err != nil {
		return OpenResult{}, err
	}
	return OpenResult{Mode: domain.OpenWithModeFor(entry.ID), Data: data}, nil
}

// launchEntry starts an entry's command in dir, in the way the row states.
func (s *Service) launchEntry(ctx context.Context, entry settings.OpenWithEntry, dir, title string) error {
	if entry.InTerminal {
		return s.term.Launch(ctx, terminal.ShellLineIn(dir, entry.Command), title)
	}
	return s.commands.Start(ctx, dir, entry.Command)
}
