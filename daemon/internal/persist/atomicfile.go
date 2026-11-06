package persist

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
)

// syncDir is a variable so tests can make the directory sync fail.
var syncDir = syncDirectory

// WriteFileAtomic writes data to path through a temp file and a rename, so a
// reader never sees a partial file and a crash mid-write leaves the old one in
// place. The temp file is synced before the rename and the directory after it,
// so a power cut cannot leave an empty or stale file behind the new name. The
// parent directory is created if needed.
//
// A failed directory sync is logged, not returned: the new file is already in
// place, and reporting the write as failed would make callers that keep the old
// data in memory (agentcatalog) disagree with the disk.
func WriteFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating directory for %s: %w", path, err)
	}
	tempFile, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tempName := tempFile.Name()
	if err := writeAndSync(tempFile, data); err != nil {
		tempFile.Close()
		os.Remove(tempName)
		return err
	}
	if err := tempFile.Close(); err != nil {
		os.Remove(tempName)
		return fmt.Errorf("closing temp file: %w", err)
	}
	if err := os.Rename(tempName, path); err != nil {
		os.Remove(tempName)
		return fmt.Errorf("renaming temp file: %w", err)
	}
	// Some FUSE and network mounts reject fsync on a directory (EINVAL).
	if err := syncDir(dir); err != nil {
		slog.Warn("the write landed but its directory could not be synced", "path", path, "error", err)
	}
	return nil
}

func writeAndSync(file *os.File, data []byte) error {
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("writing temp file: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("syncing temp file: %w", err)
	}
	return nil
}

// syncDirectory flushes the directory entry so the rename itself is durable.
func syncDirectory(dir string) error {
	handle, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("opening directory %s: %w", dir, err)
	}
	defer handle.Close()
	if err := handle.Sync(); err != nil {
		return fmt.Errorf("syncing directory %s: %w", dir, err)
	}
	return nil
}
