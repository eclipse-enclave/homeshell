// Package fsutil provides filesystem utilities for workstream creation
// strategies: directory copying and git-worktree management. Provider
// implementations compose these when materializing a workstream on disk.
package fsutil

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// DefaultCopyExcludes lists directory names excluded from copies by default.
// VCS metadata and heavyweight dependency directories are never useful in a
// copy and would waste time and disk space.
var DefaultCopyExcludes = []string{
	".git",
	"node_modules",
}

// maxCopyDepth is the deepest directory nesting CopyDir will descend into.
// Any real project is well within this; the limit exists to prevent runaway
// copies of pathological trees (e.g. a Downloads folder with a deeply nested
// cycle-like structure that would hit the OS path-length limit).
const maxCopyDepth = 64

// CopyDir recursively copies src into dst, creating dst if it does not
// exist. Directories listed in excludes are skipped entirely (matched by
// base name, case-sensitive). Symlinks are not followed: a symlink entry is
// recreated as a symlink pointing at the same target.
//
// dst must not already exist — the caller is responsible for choosing a
// fresh location. File permissions are preserved; directory permissions use
// the source's mode.
func CopyDir(src, dst string, excludes []string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return fmt.Errorf("stat source: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("source %q is not a directory", src)
	}
	if _, err := os.Lstat(dst); err == nil {
		return fmt.Errorf("destination %q already exists", dst)
	}

	excludeSet := make(map[string]bool, len(excludes))
	for _, e := range excludes {
		excludeSet[e] = true
	}

	return filepath.WalkDir(src, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		rel, err := filepath.Rel(src, path)
		if err != nil {
			return fmt.Errorf("computing relative path: %w", err)
		}
		target := filepath.Join(dst, rel)

		if d.IsDir() && excludeSet[d.Name()] && path != src {
			return fs.SkipDir
		}

		// Guard against pathologically deep trees that would exceed the OS
		// path-length limit or run for an unreasonable time.
		if d.IsDir() && path != src {
			depth := strings.Count(rel, string(filepath.Separator)) + 1
			if depth > maxCopyDepth {
				return fs.SkipDir
			}
		}

		fi, err := d.Info()
		if err != nil {
			return fmt.Errorf("stat %q: %w", path, err)
		}

		switch {
		case fi.Mode()&os.ModeSymlink != 0:
			return copySymlink(path, target)
		case d.IsDir():
			return os.MkdirAll(target, fi.Mode().Perm())
		default:
			return copyFile(path, target, fi.Mode().Perm())
		}
	})
}

func copyFile(src, dst string, perm fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %q: %w", src, err)
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_EXCL, perm)
	if err != nil {
		return fmt.Errorf("create %q: %w", dst, err)
	}
	defer func() {
		if cerr := out.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close %q: %w", dst, cerr)
		}
	}()

	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copy %q → %q: %w", src, dst, err)
	}
	return nil
}

func copySymlink(src, dst string) error {
	link, err := os.Readlink(src)
	if err != nil {
		return fmt.Errorf("readlink %q: %w", src, err)
	}
	return os.Symlink(link, dst)
}

// RemoveCopy removes a previously copied workstream directory. It is a
// thin wrapper around os.RemoveAll with a safety check: it refuses to
// remove a path that looks like a filesystem root or home directory.
func RemoveCopy(dir string) error {
	clean := filepath.Clean(dir)
	if clean == "/" || clean == "." {
		return fmt.Errorf("refusing to remove %q: looks like a root path", dir)
	}

	home, err := os.UserHomeDir()
	if err == nil && strings.TrimRight(clean, string(filepath.Separator)) == strings.TrimRight(home, string(filepath.Separator)) {
		return fmt.Errorf("refusing to remove %q: looks like the home directory", dir)
	}

	return os.RemoveAll(dir)
}
