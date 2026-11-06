package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBrowseDefaultsToHomeDir(t *testing.T) {
	handler, _ := testHandler(t, time.Minute)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/browse", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	var response browseResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	homeDir, _ := os.UserHomeDir()
	if response.Path != homeDir {
		t.Errorf("path = %q, want %q", response.Path, homeDir)
	}
}

func TestBrowseListsSubdirectories(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "alpha"), 0o755)
	os.MkdirAll(filepath.Join(dir, "beta"), 0o755)
	os.WriteFile(filepath.Join(dir, "file.txt"), []byte("x"), 0o600)

	handler, _ := testHandler(t, time.Minute)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/browse?path="+dir, nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}

	var response browseResponse
	json.Unmarshal(recorder.Body.Bytes(), &response)

	if response.Path != dir {
		t.Errorf("path = %q, want %q", response.Path, dir)
	}
	if len(response.Entries) != 2 {
		t.Fatalf("got %d entries, want 2 (files should be excluded)", len(response.Entries))
	}
	if response.Entries[0].Name != "alpha" {
		t.Errorf("first entry = %q, want %q", response.Entries[0].Name, "alpha")
	}
	if response.Entries[1].Name != "beta" {
		t.Errorf("second entry = %q, want %q", response.Entries[1].Name, "beta")
	}
}

func TestBrowseExcludesHiddenDirs(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".hidden"), 0o755)
	os.MkdirAll(filepath.Join(dir, "visible"), 0o755)

	handler, _ := testHandler(t, time.Minute)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/browse?path="+dir, nil))

	var response browseResponse
	json.Unmarshal(recorder.Body.Bytes(), &response)

	if len(response.Entries) != 1 {
		t.Fatalf("got %d entries, want 1 (hidden dirs excluded)", len(response.Entries))
	}
	if response.Entries[0].Name != "visible" {
		t.Errorf("entry = %q, want %q", response.Entries[0].Name, "visible")
	}
}

func TestBrowseShowHiddenIncludesHiddenDirs(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".hidden"), 0o755)
	os.MkdirAll(filepath.Join(dir, "visible"), 0o755)

	handler, _ := testHandler(t, time.Minute)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/browse?path="+dir+"&showHidden=true", nil))

	var response browseResponse
	json.Unmarshal(recorder.Body.Bytes(), &response)

	if len(response.Entries) != 2 {
		t.Fatalf("got %d entries, want 2 (hidden dirs included with showHidden=true)", len(response.Entries))
	}
	names := make(map[string]bool)
	for _, entry := range response.Entries {
		names[entry.Name] = true
	}
	if !names[".hidden"] {
		t.Error("missing .hidden dir when showHidden=true")
	}
	if !names["visible"] {
		t.Error("missing visible dir when showHidden=true")
	}
}

func TestBrowseReturnsParent(t *testing.T) {
	dir := t.TempDir()
	child := filepath.Join(dir, "child")
	os.MkdirAll(child, 0o755)

	handler, _ := testHandler(t, time.Minute)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/browse?path="+child, nil))

	var response browseResponse
	json.Unmarshal(recorder.Body.Bytes(), &response)

	if response.Parent != dir {
		t.Errorf("parent = %q, want %q", response.Parent, dir)
	}
}

func TestBrowseRejectsNonExistentPath(t *testing.T) {
	handler, _ := testHandler(t, time.Minute)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/browse?path=/no/such/path/zzz", nil))

	if recorder.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
}

func TestBrowseRejectsFilePath(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "afile")
	os.WriteFile(tmpFile, []byte("x"), 0o600)

	handler, _ := testHandler(t, time.Minute)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/browse?path="+tmpFile, nil))

	if recorder.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
}

func TestBrowseFollowsSymlinksToDirectories(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "real-dir"), 0o755)
	os.Symlink(filepath.Join(dir, "real-dir"), filepath.Join(dir, "linked-dir"))
	// Symlink to a file should still be excluded.
	os.WriteFile(filepath.Join(dir, "real-file"), []byte("x"), 0o600)
	os.Symlink(filepath.Join(dir, "real-file"), filepath.Join(dir, "linked-file"))

	handler, _ := testHandler(t, time.Minute)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/browse?path="+dir, nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}

	var response browseResponse
	json.Unmarshal(recorder.Body.Bytes(), &response)

	// Should include both the real dir and the symlinked dir, but not the symlinked file.
	if len(response.Entries) != 2 {
		t.Fatalf("got %d entries, want 2 (real-dir + linked-dir); entries: %v", len(response.Entries), response.Entries)
	}
	names := make(map[string]bool)
	for _, entry := range response.Entries {
		names[entry.Name] = true
	}
	if !names["real-dir"] {
		t.Error("missing real-dir")
	}
	if !names["linked-dir"] {
		t.Error("missing linked-dir (symlink to directory)")
	}
}

func TestBrowseEmptyDirectory(t *testing.T) {
	dir := t.TempDir()

	handler, _ := testHandler(t, time.Minute)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/browse?path="+dir, nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}

	var response browseResponse
	json.Unmarshal(recorder.Body.Bytes(), &response)

	if response.Entries == nil {
		t.Error("entries should not be nil for empty directory")
	}
	if len(response.Entries) != 0 {
		t.Errorf("got %d entries, want 0", len(response.Entries))
	}
}
