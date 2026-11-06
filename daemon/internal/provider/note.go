package provider

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
)

// NoteFileName is the note a workstream keeps in its working directory. It sits
// where the workstream's sessions run — the directory Enclave mounts into its
// container — so a note written here is visible to the agent working in it, not
// just to the person who wrote it.
const NoteFileName = "NOTES.md"

// NoteResult reports where the note lives and whether this call created it.
type NoteResult struct {
	Path    string `json:"path"`
	Created bool   `json:"created"`
}

// OpenNote opens the workstream's note in the desktop's default application for
// the file type, creating it first if it does not exist yet.
//
// Creation is the only write: an existing note is opened untouched, because the
// point of the file is what the user (or the agent) has put in it. A fresh one
// gets a single heading naming the workstream, so a note opened weeks later says
// what it is about.
func (s *Service) OpenNote(ctx context.Context, ref WorkstreamRef) (NoteResult, error) {
	ws, err := s.store.Workstream(ref.ProjectID, ref.WorkstreamID)
	if err != nil {
		return NoteResult{}, ErrWorkstreamNotFound
	}
	dir, err := s.materializedDir(ws)
	if err != nil {
		return NoteResult{}, err
	}
	path := filepath.Join(dir, NoteFileName)
	created, err := createNoteIfMissing(path, ws)
	if err != nil {
		return NoteResult{}, err
	}
	if err := s.opener.Open(ctx, path); err != nil {
		return NoteResult{}, fmt.Errorf("%w: %s: %v", ErrHandoffFailed, NoteFileName, err)
	}
	return NoteResult{Path: path, Created: created}, nil
}

// createNoteIfMissing writes the initial note, reporting whether it did. The
// O_EXCL create is what makes "only if missing" safe: checking for the file and
// then writing it would race two clicks against each other and could truncate a
// note written in between.
func createNoteIfMissing(path string, ws domain.Workstream) (bool, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, os.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("creating %s: %w", NoteFileName, err)
	}
	defer file.Close()
	name := ws.Name
	if name == "" {
		name = "Workstream"
	}
	if _, err := fmt.Fprintf(file, "# %s\n\n", name); err != nil {
		return false, fmt.Errorf("writing %s: %w", NoteFileName, err)
	}
	return true, nil
}
