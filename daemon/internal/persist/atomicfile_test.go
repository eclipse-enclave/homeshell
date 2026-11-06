package persist

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// A directory that cannot be synced (some FUSE and network mounts) does not turn
// a write that landed into a reported failure.
func TestWriteFileAtomicSucceedsWhenTheDirectorySyncFails(t *testing.T) {
	saved := syncDir
	t.Cleanup(func() { syncDir = saved })
	syncDir = func(string) error { return errors.New("invalid argument") }

	path := filepath.Join(t.TempDir(), "file.json")
	if err := WriteFileAtomic(path, []byte("new")); err != nil {
		t.Fatalf("WriteFileAtomic = %v; want success, the file is in place", err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "new" {
		t.Errorf("file holds %q (%v), want the new content", got, err)
	}
}
