package local

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/eclipsesource/homeshell/daemon/internal/persist"
)

// fileStore persists the provider's state as a JSON file. Writes are atomic
// (write-temp-then-rename) so a crash mid-write cannot corrupt the file.
type fileStore struct {
	path string
}

// NewFileStore returns a Store backed by the JSON file at path.
func NewFileStore(path string) Store { return &fileStore{path: path} }

func (s *fileStore) Load() ([]PersistedWorkspace, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil // no state yet
	}
	if err != nil {
		return nil, fmt.Errorf("reading %q: %w", s.path, err)
	}
	if len(data) == 0 {
		return nil, nil
	}
	var out []PersistedWorkspace
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("parsing %q: %w", s.path, err)
	}
	return out, nil
}

func (s *fileStore) Save(workspaces []PersistedWorkspace) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("creating state directory: %w", err)
	}
	data, err := json.MarshalIndent(workspaces, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling state: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("writing %q: %w", tmp, err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("renaming %q: %w", tmp, err)
	}
	return nil
}

// defaultStatePaths returns the managed-directory root and the persistence-file
// path under the data directory (persist.DefaultDir), falling back to the OS
// temp dir when neither is resolvable.
func defaultStatePaths() (root, storePath string) {
	base, err := persist.DefaultDir()
	if err != nil {
		base = filepath.Join(os.TempDir(), "homeshell")
	}
	return filepath.Join(base, "workstreams"), filepath.Join(base, "local-provider.json")
}
