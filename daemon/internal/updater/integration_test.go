package updater

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// These tests exercise the real go-selfupdate HttpSource against a local fake
// release server, per issue #3 ("point the updater at a local/fake release
// source rather than real GitHub releases"). They cover the happy path
// (detect → verify → atomic swap) and the safety property that a download
// failing checksum verification never replaces the running binary.

const (
	testOwner   = "eclipsesource"
	testRepo    = "homeshell"
	testVersion = "9.9.9"
)

// fakeReleaseServer serves a go-selfupdate HTTP manifest, one platform asset,
// and a checksums.txt. When corruptChecksum is set the checksum entry does not
// match the asset, simulating a broken/tampered download.
func fakeReleaseServer(t *testing.T, newBinary []byte, corruptChecksum bool) *httptest.Server {
	t.Helper()

	assetName := fmt.Sprintf("homeshell_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	archive := tarGz(t, "homeshell", newBinary)

	sum := sha256.Sum256(archive)
	checksumHex := hex.EncodeToString(sum[:])
	if corruptChecksum {
		checksumHex = hex.EncodeToString(bytes.Repeat([]byte{0}, sha256.Size))
	}
	checksums := fmt.Sprintf("%s  %s\n", checksumHex, assetName)

	mux := http.NewServeMux()
	base := "/" + testOwner + "/" + testRepo
	mux.HandleFunc(base+"/manifest.yaml", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `last_release_id: 1
last_asset_id: 2
releases:
  - id: 1
    name: v%[1]s
    tag_name: v%[1]s
    assets:
      - id: 1
        name: %[2]s
        url: %[2]s
      - id: 2
        name: checksums.txt
        url: checksums.txt
`, testVersion, assetName)
	})
	mux.HandleFunc(base+"/"+assetName, func(w http.ResponseWriter, _ *http.Request) {
		w.Write(archive)
	})
	mux.HandleFunc(base+"/checksums.txt", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, checksums)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// tarGz packs a single named file into a gzip-compressed tar archive.
func tarGz(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	hdr := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(content))}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func newHTTPUpdater(t *testing.T, server *httptest.Server) Updater {
	t.Helper()
	up, err := New(context.Background(), Config{
		Owner:   testOwner,
		Repo:    testRepo,
		BaseURL: server.URL,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return up
}

func TestHttpSourceDetectsLatest(t *testing.T) {
	server := fakeReleaseServer(t, []byte("new-binary"), false)
	up := newHTTPUpdater(t, server)

	rel, err := up.Latest(context.Background())
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if rel.Version != testVersion {
		t.Errorf("version = %q, want %q", rel.Version, testVersion)
	}
}

func TestHttpSourceApplySwapsBinary(t *testing.T) {
	newBinary := []byte("the new daemon binary")
	server := fakeReleaseServer(t, newBinary, false)
	up := newHTTPUpdater(t, server)

	exe := filepath.Join(t.TempDir(), "homeshell")
	if err := os.WriteFile(exe, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	rel, err := up.Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := up.Apply(context.Background(), rel, exe); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	got, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, newBinary) {
		t.Errorf("binary after update = %q, want the new binary", got)
	}
}

func TestHttpSourceBrokenDownloadDoesNotReplaceBinary(t *testing.T) {
	server := fakeReleaseServer(t, []byte("new-binary"), true) // corrupt checksum
	up := newHTTPUpdater(t, server)

	exe := filepath.Join(t.TempDir(), "homeshell")
	const original = "original binary"
	if err := os.WriteFile(exe, []byte(original), 0o755); err != nil {
		t.Fatal(err)
	}

	// With a corrupt checksum the release detection itself fails validation in
	// go-selfupdate; whether the error surfaces at Latest or Apply, the binary
	// on disk must be untouched.
	rel, err := up.Latest(context.Background())
	if err == nil {
		if err = up.Apply(context.Background(), rel, exe); err == nil {
			t.Fatal("expected checksum verification to fail")
		}
	}

	got, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Errorf("binary was modified on failed update: got %q, want %q", got, original)
	}
}
