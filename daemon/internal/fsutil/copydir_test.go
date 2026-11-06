package fsutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCopyDirBasic(t *testing.T) {
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "a.txt"), "hello")
	writeFile(t, filepath.Join(src, "sub", "b.txt"), "world")

	dst := filepath.Join(t.TempDir(), "copy")
	if err := CopyDir(src, dst, nil); err != nil {
		t.Fatalf("CopyDir: %v", err)
	}

	assertFileContent(t, filepath.Join(dst, "a.txt"), "hello")
	assertFileContent(t, filepath.Join(dst, "sub", "b.txt"), "world")
}

func TestCopyDirExcludes(t *testing.T) {
	tests := []struct {
		name     string
		excludes []string
		wantGone []string
		wantKept []string
	}{
		{
			name:     "exclude .git",
			excludes: []string{".git"},
			wantGone: []string{".git/config"},
			wantKept: []string{"src/main.go"},
		},
		{
			name:     "exclude node_modules",
			excludes: []string{"node_modules"},
			wantGone: []string{"node_modules/pkg/index.js"},
			wantKept: []string{"src/main.go", ".git/config"},
		},
		{
			name:     "exclude multiple",
			excludes: []string{".git", "node_modules"},
			wantGone: []string{".git/config", "node_modules/pkg/index.js"},
			wantKept: []string{"src/main.go"},
		},
		{
			name:     "no excludes",
			excludes: nil,
			wantGone: nil,
			wantKept: []string{"src/main.go", ".git/config", "node_modules/pkg/index.js"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := t.TempDir()
			writeFile(t, filepath.Join(src, "src", "main.go"), "package main")
			writeFile(t, filepath.Join(src, ".git", "config"), "[core]")
			writeFile(t, filepath.Join(src, "node_modules", "pkg", "index.js"), "module.exports = {}")

			dst := filepath.Join(t.TempDir(), "copy")
			if err := CopyDir(src, dst, tt.excludes); err != nil {
				t.Fatalf("CopyDir: %v", err)
			}

			for _, rel := range tt.wantGone {
				if _, err := os.Stat(filepath.Join(dst, rel)); err == nil {
					t.Errorf("excluded path %q exists in copy", rel)
				}
			}
			for _, rel := range tt.wantKept {
				if _, err := os.Stat(filepath.Join(dst, rel)); err != nil {
					t.Errorf("expected path %q missing from copy: %v", rel, err)
				}
			}
		})
	}
}

func TestCopyDirPreservesPermissions(t *testing.T) {
	src := t.TempDir()
	path := filepath.Join(src, "script.sh")
	writeFile(t, path, "#!/bin/sh\necho hi")
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(t.TempDir(), "copy")
	if err := CopyDir(src, dst, nil); err != nil {
		t.Fatalf("CopyDir: %v", err)
	}

	info, err := os.Stat(filepath.Join(dst, "script.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("permission = %o, want 755", info.Mode().Perm())
	}
}

func TestCopyDirSymlinks(t *testing.T) {
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "target.txt"), "data")
	if err := os.Symlink("target.txt", filepath.Join(src, "link.txt")); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(t.TempDir(), "copy")
	if err := CopyDir(src, dst, nil); err != nil {
		t.Fatalf("CopyDir: %v", err)
	}

	link, err := os.Readlink(filepath.Join(dst, "link.txt"))
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if link != "target.txt" {
		t.Errorf("symlink target = %q, want %q", link, "target.txt")
	}
}

func TestCopyDirRejectsNonDirectory(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file.txt")
	writeFile(t, file, "content")

	dst := filepath.Join(t.TempDir(), "copy")
	err := CopyDir(file, dst, nil)
	if err == nil {
		t.Fatal("expected error for non-directory source")
	}
}

func TestCopyDirRejectsExistingDest(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir() // already exists

	err := CopyDir(src, dst, nil)
	if err == nil {
		t.Fatal("expected error for existing destination")
	}
}

func TestCopyDirRejectsMissingSource(t *testing.T) {
	err := CopyDir("/nonexistent/path/that/does/not/exist", filepath.Join(t.TempDir(), "copy"), nil)
	if err == nil {
		t.Fatal("expected error for missing source")
	}
}

func TestCopyDirDefaultExcludes(t *testing.T) {
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "main.go"), "package main")
	writeFile(t, filepath.Join(src, ".git", "HEAD"), "ref: refs/heads/main")
	writeFile(t, filepath.Join(src, "node_modules", "x", "y.js"), "")

	dst := filepath.Join(t.TempDir(), "copy")
	if err := CopyDir(src, dst, DefaultCopyExcludes); err != nil {
		t.Fatalf("CopyDir: %v", err)
	}

	assertFileContent(t, filepath.Join(dst, "main.go"), "package main")

	if _, err := os.Stat(filepath.Join(dst, ".git")); err == nil {
		t.Error(".git was not excluded by DefaultCopyExcludes")
	}
	if _, err := os.Stat(filepath.Join(dst, "node_modules")); err == nil {
		t.Error("node_modules was not excluded by DefaultCopyExcludes")
	}
}

func TestRemoveCopy(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "workstream")
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "sub", "f.txt"), "data")

	if err := RemoveCopy(dir); err != nil {
		t.Fatalf("RemoveCopy: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("directory still exists after RemoveCopy")
	}
}

func TestRemoveCopyRefusesRoot(t *testing.T) {
	if err := RemoveCopy("/"); err == nil {
		t.Error("RemoveCopy did not refuse /")
	}
	if err := RemoveCopy("."); err == nil {
		t.Error("RemoveCopy did not refuse .")
	}
}

func TestRemoveCopyRefusesHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot determine home directory")
	}
	if err := RemoveCopy(home); err == nil {
		t.Error("RemoveCopy did not refuse the home directory")
	}
}

func TestCopyDirSkipsDeeplyNestedDirectories(t *testing.T) {
	src := t.TempDir()
	// Create a directory nested deeper than maxCopyDepth.
	deepDir := src
	for i := 0; i < maxCopyDepth+5; i++ {
		deepDir = filepath.Join(deepDir, "d")
	}
	if err := os.MkdirAll(deepDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(deepDir, "deep.txt"), "too deep")
	// Also create a shallow file that should be copied.
	writeFile(t, filepath.Join(src, "shallow.txt"), "ok")

	dst := filepath.Join(t.TempDir(), "copy")
	if err := CopyDir(src, dst, nil); err != nil {
		t.Fatalf("CopyDir: %v", err)
	}

	// The shallow file is copied.
	assertFileContent(t, filepath.Join(dst, "shallow.txt"), "ok")

	// The deep file is not — the walk stopped before reaching it.
	deepDst := dst
	for i := 0; i < maxCopyDepth+5; i++ {
		deepDst = filepath.Join(deepDst, "d")
	}
	if _, err := os.Stat(filepath.Join(deepDst, "deep.txt")); err == nil {
		t.Error("deeply nested file should have been skipped")
	}
}

// --- test helpers ---

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %q: %v", path, err)
	}
	if string(data) != want {
		t.Errorf("content of %q = %q, want %q", path, data, want)
	}
}
