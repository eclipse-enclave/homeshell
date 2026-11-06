// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package updater

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	testArtifact = "homeshell-linux-amd64.tar.gz"
	testCommit   = "1234567890abcdef1234567890abcdef12345678"
	otherCommit  = "abcdef0123456789abcdef0123456789abcdef01"
)

type archiveEntry struct {
	name     string
	body     []byte
	typeflag byte
}

// archiveOf packs entries as a release archive does.
func archiveOf(t *testing.T, entries ...archiveEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		typeflag := e.typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		header := &tar.Header{Name: e.name, Mode: 0o755, Size: int64(len(e.body)), Typeflag: typeflag}
		if typeflag != tar.TypeReg {
			header.Size = 0
		}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if typeflag == tar.TypeReg {
			if _, err := tw.Write(e.body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// releaseArchive is a release archive holding binary beside its notices.
func releaseArchive(t *testing.T, binary []byte) []byte {
	return archiveOf(t,
		archiveEntry{name: "README.md", body: []byte("readme")},
		archiveEntry{name: "homeshell", body: binary},
		archiveEntry{name: "licenses/herdr/LICENSE", body: []byte("license")},
	)
}

// testRelease serves a rolling channel directory. Its handlers can be swapped
// per test to simulate a broken or moving release.
type testRelease struct {
	latest    http.HandlerFunc
	checksums http.HandlerFunc
	archive   http.HandlerFunc
}

func newTestRelease(archive []byte) *testRelease {
	digest := sha256.Sum256(archive)
	return &testRelease{
		latest: func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprintln(w, testCommit)
		},
		checksums: func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprintf(w, "%x  homeshell-darwin-arm64.tar.gz\n%x  %s\n", sha256.Sum256(nil), digest, testArtifact)
		},
		archive: func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(archive)
		},
	}
}

func (r *testRelease) serve(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/rolling/latest":
			r.latest(w, req)
		case "/rolling/checksums.txt":
			r.checksums(w, req)
		case "/rolling/" + testArtifact:
			r.archive(w, req)
		default:
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// newTestUpdater returns a linux/amd64 rolling build updating from server.
func newTestUpdater(server *httptest.Server) *channelUpdater {
	u := New(Config{Version: "0.0.0-gabcdef0", Commit: "abcdef0", Channel: ChannelRolling, BaseURL: server.URL}).(*channelUpdater)
	u.goos = "linux"
	u.goarch = "amd64"
	return u
}

// checkThenInstall installs what Check reports, as Service.Apply does.
func checkThenInstall(t *testing.T, u *channelUpdater, target string) error {
	t.Helper()
	status, err := u.Check(context.Background())
	if err != nil {
		return err
	}
	return u.Install(context.Background(), status, target)
}

func writeExecutable(t *testing.T, mode os.FileMode) string {
	t.Helper()
	target := filepath.Join(t.TempDir(), "homeshell")
	if err := os.WriteFile(target, []byte("old binary"), mode); err != nil {
		t.Fatal(err)
	}
	return target
}

func assertUnchanged(t *testing.T, target string) {
	t.Helper()
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old binary" {
		t.Fatalf("executable changed after failed update: %q", got)
	}
	entries, err := os.ReadDir(filepath.Dir(target))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("failed update left files behind: %v", entries)
	}
}

func TestCheckComparesShortAndFullCommits(t *testing.T) {
	u := newTestUpdater(newTestRelease(nil).serve(t))
	for _, tc := range []struct {
		name      string
		current   string
		available bool
	}{
		{name: "same short commit", current: "1234567", available: false},
		{name: "same full commit", current: testCommit, available: false},
		{name: "dirty same commit", current: "1234567-dirty", available: false},
		{name: "different commit", current: "abcdef0", available: true},
		{name: "unknown build", current: "unknown", available: true},
		{name: "no commit stamp", current: "", available: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u.current.Commit = tc.current
			status, err := u.Check(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if status.UpdateAvailable != tc.available {
				t.Fatalf("UpdateAvailable = %v, want %v", status.UpdateAvailable, tc.available)
			}
			if status.Channel != ChannelRolling || status.Latest != "1234567" {
				t.Fatalf("status = %+v, want rolling channel at 1234567", status)
			}
		})
	}
}

func TestCheckRejectsBadReferences(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"not hex": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprintln(w, "not-a-commit")
		},
		"empty": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprintln(w, "")
		},
		"error page": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprintln(w, "<html><body>Moved</body></html>")
		},
		"missing release": func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		},
	} {
		t.Run(name, func(t *testing.T) {
			release := newTestRelease(nil)
			release.latest = handler
			if _, err := newTestUpdater(release.serve(t)).Check(context.Background()); err == nil {
				t.Fatal("Check accepted a bad rolling reference")
			}
		})
	}
}

func TestCheckReferenceSHALengths(t *testing.T) {
	for _, length := range []int{6, 7, 8, 39, 40, 41, 42, 63, 64, 65} {
		t.Run(fmt.Sprint(length), func(t *testing.T) {
			release := newTestRelease(nil)
			sha := strings.Repeat("a", length)
			release.latest = func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprintln(w, sha)
			}
			status, err := newTestUpdater(release.serve(t)).Check(context.Background())
			if length != 40 && length != 64 {
				if err == nil {
					t.Fatal("Check accepted a non-full-length SHA")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if status.build != sha || status.Latest != sha[:7] {
				t.Fatalf("status = %+v, want build %q shown as %q", status, sha, sha[:7])
			}
		})
	}
}

func TestUnsupportedBuildsReportNoUpdate(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	t.Cleanup(server.Close)
	for _, tc := range []struct {
		name          string
		channel, goos string
	}{
		{name: "source or package build", channel: "", goos: "linux"},
		{name: "unknown channel", channel: "nightly", goos: "linux"},
		{name: "unsupported platform", channel: ChannelStable, goos: "windows"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := newTestUpdater(server)
			u.channel, u.goos = tc.channel, tc.goos
			if err := u.Supported(); !errors.Is(err, ErrUnsupported) {
				t.Fatalf("Supported = %v, want ErrUnsupported", err)
			}
			status, err := u.Check(context.Background())
			if err != nil {
				t.Fatalf("Check failed for an unsupported build: %v", err)
			}
			want := Status{Current: "0.0.0-gabcdef0", Latest: "0.0.0-gabcdef0"}
			if status != want {
				t.Fatalf("status = %+v, want %+v", status, want)
			}
			if err := u.Install(context.Background(), Status{build: testCommit}, "/nonexistent"); !errors.Is(err, ErrUnsupported) {
				t.Fatalf("Install = %v, want ErrUnsupported", err)
			}
		})
	}
	if requests.Load() != 0 {
		t.Fatalf("unsupported builds made %d requests", requests.Load())
	}
}

func TestInstallVerifiesAndAtomicallyReplacesExecutable(t *testing.T) {
	newBinary := []byte("new homeshell binary")
	server := newTestRelease(releaseArchive(t, newBinary)).serve(t)
	target := writeExecutable(t, 0o751)

	if err := checkThenInstall(t, newTestUpdater(server), target); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(newBinary) {
		t.Fatalf("installed contents = %q, want %q", got, newBinary)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o751 {
		t.Fatalf("installed mode = %o, want 751", info.Mode().Perm())
	}
	if entries, _ := os.ReadDir(filepath.Dir(target)); len(entries) != 1 {
		t.Fatalf("Install left files behind: %v", entries)
	}
}

func TestInstallFailuresPreserveExecutable(t *testing.T) {
	for _, tc := range []struct {
		name    string
		setup   func(*testing.T, *testRelease, *channelUpdater)
		wantErr string
	}{
		{
			name: "checksum mismatch",
			setup: func(t *testing.T, r *testRelease, _ *channelUpdater) {
				tampered := releaseArchive(t, []byte("tampered binary"))
				r.archive = func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(tampered) }
			},
			wantErr: "checksum mismatch",
		},
		{
			name: "no checksum entry",
			setup: func(_ *testing.T, r *testRelease, _ *channelUpdater) {
				r.checksums = func(w http.ResponseWriter, _ *http.Request) {
					_, _ = fmt.Fprintf(w, "%x  homeshell-darwin-arm64.tar.gz\n", sha256.Sum256(nil))
				}
			},
			wantErr: "has no checksum for " + testArtifact,
		},
		{
			name: "archive missing",
			setup: func(_ *testing.T, r *testRelease, _ *channelUpdater) {
				r.archive = func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) }
			},
			wantErr: "502",
		},
		{
			name: "not an archive",
			setup: func(_ *testing.T, r *testRelease, _ *channelUpdater) {
				r.archive = func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprint(w, "<html>") }
			},
			wantErr: "read " + testArtifact,
		},
		{
			name: "no binary in the archive",
			setup: func(t *testing.T, r *testRelease, _ *channelUpdater) {
				archive := archiveOf(t, archiveEntry{name: "README.md", body: []byte("readme")})
				*r = *newTestRelease(archive)
			},
			wantErr: "has no homeshell binary",
		},
		{
			name: "binary is a symlink",
			setup: func(t *testing.T, r *testRelease, _ *channelUpdater) {
				archive := archiveOf(t, archiveEntry{name: "homeshell", typeflag: tar.TypeSymlink})
				*r = *newTestRelease(archive)
			},
			wantErr: "exactly one regular homeshell",
		},
		{
			name:    "oversized download",
			setup:   func(_ *testing.T, _ *testRelease, u *channelUpdater) { u.maxDownloadBytes = 4 },
			wantErr: "exceeds 4 bytes",
		},
		{
			name: "oversized binary",
			setup: func(t *testing.T, r *testRelease, u *channelUpdater) {
				// Compresses far below the limit but expands above it.
				*r = *newTestRelease(releaseArchive(t, bytes.Repeat([]byte{0}, 1<<16)))
				u.maxDownloadBytes = 1 << 12
			},
			wantErr: "homeshell in " + testArtifact + " exceeds 4096 bytes",
		},
		{
			name: "release moved during download",
			setup: func(_ *testing.T, r *testRelease, _ *channelUpdater) {
				// The release moves once Install has confirmed it and started
				// downloading: lookups are Check, then Install's two.
				var lookups atomic.Int32
				r.latest = func(w http.ResponseWriter, _ *http.Request) {
					commit := testCommit
					if lookups.Add(1) > 2 {
						commit = otherCommit
					}
					_, _ = fmt.Fprintln(w, commit)
				}
			},
			wantErr: "moved from 1234567 to abcdef0",
		},
		{
			name: "stalled download",
			setup: func(_ *testing.T, r *testRelease, u *channelUpdater) {
				u.fetcher.idleTimeout = 50 * time.Millisecond
				r.archive = func(w http.ResponseWriter, req *http.Request) {
					w.Header().Set("Content-Length", "1000")
					_, _ = fmt.Fprint(w, "partial")
					w.(http.Flusher).Flush()
					<-req.Context().Done()
				}
			},
			wantErr: "no data received for 50ms",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			release := newTestRelease(releaseArchive(t, []byte("new homeshell binary")))
			target := writeExecutable(t, 0o755)
			u := newTestUpdater(release.serve(t))
			tc.setup(t, release, u)

			err := checkThenInstall(t, u, target)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Install error = %v, want it to contain %q", err, tc.wantErr)
			}
			assertUnchanged(t, target)
		})
	}
}

func TestInstallRemovesStaleUpdateDownloads(t *testing.T) {
	server := newTestRelease(releaseArchive(t, []byte("new homeshell binary"))).serve(t)
	target := writeExecutable(t, 0o755)
	dir := filepath.Dir(target)
	stale := filepath.Join(dir, ".homeshell-update-stale")
	recent := filepath.Join(dir, ".homeshell-update-recent")
	for _, path := range []string{stale, recent} {
		if err := os.WriteFile(path, []byte("partial"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-2 * staleUpdateTempAge)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}

	if err := checkThenInstall(t, newTestUpdater(server), target); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale download survived: %v", err)
	}
	if _, err := os.Stat(recent); err != nil {
		t.Fatalf("recent download, possibly another install's, was removed: %v", err)
	}
}

// serveStableReleases publishes the stable layout: a latest file naming the
// newest version beside one directory per release.
func serveStableReleases(t *testing.T, latest string, archive []byte) *httptest.Server {
	t.Helper()
	digest := sha256.Sum256(archive)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/releases/latest":
			_, _ = fmt.Fprintln(w, latest)
		case "/releases/1.2.0/checksums.txt":
			_, _ = fmt.Fprintf(w, "%x  %s\n", digest, testArtifact)
		case "/releases/1.2.0/" + testArtifact:
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestStableChannelFollowsVersionedReleases(t *testing.T) {
	newBinary := []byte("stable homeshell binary")
	server := serveStableReleases(t, "1.2.0", releaseArchive(t, newBinary))
	target := writeExecutable(t, 0o755)
	u := newTestUpdater(server)
	u.channel = ChannelStable
	u.current = build{Version: "1.2.0", Commit: "abcdef0"}

	status, err := u.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.UpdateAvailable || status.Latest != "1.2.0" || status.Current != "1.2.0" || status.Channel != ChannelStable {
		t.Fatalf("status = %+v, want 1.2.0 current on stable", status)
	}

	// Newer builds stay: a release installed by hand before the pointer
	// moved, or a pre-release, which the pointer never names.
	for _, newer := range []string{"1.3.0", "1.10.0", "1.2.1", "1.3.0-rc.1", "2.0.0-beta.2"} {
		u.current.Version = newer
		status, err = u.Check(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if status.UpdateAvailable {
			t.Fatalf("status = %+v for current %s, want no downgrade to 1.2.0", status, newer)
		}
	}
	assertUnchanged(t, target)

	for _, older := range []string{"1.1.0", "1.2.0-rc.1", "1.2.0-rc.10", "1.1.10", "0.9.0", "dev", "1.1.0-3-gabc1234", "", "01.1.0", "1.1.00"} {
		u.current.Version = older
		status, err = u.Check(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !status.UpdateAvailable {
			t.Fatalf("status = %+v for current %q, want an available update", status, older)
		}
	}
	u.current.Version = "1.1.0"
	if err := checkThenInstall(t, u, target); err != nil {
		t.Fatalf("Install = %v; want the release installed", err)
	}
	if got, _ := os.ReadFile(target); string(got) != string(newBinary) {
		t.Fatalf("installed contents = %q, want the stable binary", got)
	}
}

func TestCompareVersions(t *testing.T) {
	for _, tc := range []struct {
		current, latest string
		want            int
	}{
		{"1.2.0", "1.2.0", 0},
		{"v1.2.0", "1.2.0", 0},
		{"1.2.0", "1.10.0", -1},
		{"1.10.0", "1.2.0", 1},
		{"1.2.0-rc.1", "1.2.0", -1},
		{"1.2.0", "1.2.0-rc.1", 1},
		{"1.2.0-rc.2", "1.2.0-rc.10", -1},
		{"1.2.0-alpha.1", "1.2.0-beta.1", -1},
		{"1.2.0-beta.3", "1.2.0-rc.1", -1},
		{"1.2.0-rc.1", "1.2.0-alpha.9", 1},
		{"1.2.0-rc.01", "1.2.0-rc.1", -1},
		{"100000000000000000000.0.0", "99999999999999999999.0.0", 1},
		{"1.2.0-rc.100000000000000000000", "1.2.0-rc.99999999999999999999", 1},
		{"dev", "1.2.0", -1},
		{"1.2.0-3-gabc1234", "1.2.0", -1},
		{"", "0.0.1", -1},
	} {
		if got := compareVersions(tc.current, tc.latest); got != tc.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", tc.current, tc.latest, got, tc.want)
		}
	}
}

func TestStableChannelRejectsBadVersions(t *testing.T) {
	for _, latest := range []string{"", "rolling", "1.2", "../rolling", "1.2.0/x", "<html>", "v1.2.0", "01.2.0", "1.02.0", "1.2.00", "1.2.0-rc1", "1.2.0-rc-1", "1.2.0-alpha", "1.2.0-rc.01", "1.2.0-rc.1.1"} {
		t.Run(latest, func(t *testing.T) {
			u := newTestUpdater(serveStableReleases(t, latest, nil))
			u.channel = ChannelStable
			if _, err := u.Check(context.Background()); err == nil {
				t.Fatalf("Check accepted latest %q", latest)
			}
		})
	}
}

func TestAssetURLsFollowTheDownloadLayout(t *testing.T) {
	stable := newStableSource(fetcher{}, DefaultBaseURL)
	if got, want := stable.assetURL("1.2.0", checksumsFilename), "https://download.eclipse.org/enclave/homeshell/releases/1.2.0/checksums.txt"; got != want {
		t.Fatalf("assetURL = %q, want %q", got, want)
	}
	rolling := newRollingSource(fetcher{}, DefaultBaseURL)
	if got, want := rolling.assetURL(testCommit, testArtifact), "https://download.eclipse.org/enclave/homeshell/rolling/"+testArtifact; got != want {
		t.Fatalf("assetURL = %q, want %q", got, want)
	}
}

func TestArtifactName(t *testing.T) {
	for _, tc := range []struct{ goos, goarch, want string }{
		{"linux", "amd64", "homeshell-linux-amd64.tar.gz"},
		{"linux", "arm64", "homeshell-linux-arm64.tar.gz"},
		{"darwin", "amd64", "homeshell-darwin-amd64.tar.gz"},
		{"darwin", "arm64", "homeshell-darwin-arm64.tar.gz"},
		{"windows", "amd64", ""},
		{"linux", "386", ""},
	} {
		got, err := artifactName(tc.goos, tc.goarch)
		if got != tc.want || (tc.want == "") != errors.Is(err, ErrUnsupported) {
			t.Errorf("artifactName(%s, %s) = %q, %v; want %q", tc.goos, tc.goarch, got, err, tc.want)
		}
	}
}

func TestInstallIntoUnwritableDirectoryFailsOffline(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write any directory")
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	target := writeExecutable(t, 0o755)
	dir := filepath.Dir(target)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	err := newTestUpdater(server).Install(context.Background(), Status{build: testCommit}, target)
	if !errors.Is(err, ErrNotWritable) {
		t.Fatalf("Install error = %v, want ErrNotWritable", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("made %d requests before finding the directory unwritable", requests.Load())
	}
	assertUnchanged(t, target)
}

func TestNotWritableNamesTheReinstallCommand(t *testing.T) {
	server := newTestRelease(nil).serve(t)
	u := newTestUpdater(server)
	err := u.notWritable("/opt/my tools/homeshell", testCommit)
	if !errors.Is(err, ErrNotWritable) {
		t.Fatalf("error = %v, want ErrNotWritable", err)
	}
	for _, want := range []string{
		"download " + server.URL + "/rolling/" + testArtifact + " and " + server.URL + "/rolling/checksums.txt",
		"with: sha256sum --check --ignore-missing checksums.txt && tar -xzf " + testArtifact + " homeshell && sudo install homeshell '/opt/my tools/homeshell'",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}

	u.goos = "darwin"
	want := "with: shasum -a 256 --check --ignore-missing checksums.txt && tar -xzf homeshell-darwin-amd64.tar.gz homeshell && xattr -c homeshell && sudo install homeshell '/opt/my tools/homeshell'"
	if err := u.notWritable("/opt/my tools/homeshell", testCommit); !strings.HasSuffix(err.Error(), want) {
		t.Errorf("error on macOS = %v, want it to end with %q", err, want)
	}
}

// A sticky directory such as /tmp lets anyone create files but refuses to
// replace another user's, so the final rename can be denied after CreateTemp
// succeeded. Revoking write access during the download produces the same
// denial without a second user.
func TestInstallReportsDeniedReplacementAsNotWritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can replace any file")
	}
	release := newTestRelease(releaseArchive(t, []byte("new homeshell binary")))
	target := writeExecutable(t, 0o755)
	dir := filepath.Dir(target)
	serveArchive := release.archive
	release.archive = func(w http.ResponseWriter, r *http.Request) {
		if err := os.Chmod(dir, 0o555); err != nil {
			t.Error(err)
		}
		serveArchive(w, r)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	err := checkThenInstall(t, newTestUpdater(release.serve(t)), target)
	if !errors.Is(err, ErrNotWritable) || !strings.Contains(err.Error(), "sudo install") {
		t.Fatalf("Install error = %v, want ErrNotWritable with the reinstall command", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "old binary" {
		t.Fatalf("executable changed after a denied replacement: %q", got)
	}
}

func TestShellArgQuotesOnlyWhenNeeded(t *testing.T) {
	for path, want := range map[string]string{
		"/usr/local/bin/homeshell": "/usr/local/bin/homeshell",
		"/home/me/.local/bin/x_1+": "/home/me/.local/bin/x_1+",
		"/opt/my tools/homeshell":  "'/opt/my tools/homeshell'",
		"/opt/$HOME/homeshell":     "'/opt/$HOME/homeshell'",
		"/opt/it's/homeshell":      `'/opt/it'"'"'s/homeshell'`,
		"/opt/é/homeshell":         "'/opt/é/homeshell'",
	} {
		if got := shellArg(path); got != want {
			t.Errorf("shellArg(%q) = %s, want %s", path, got, want)
		}
	}
}

func TestFailedRequestsExplainThemselves(t *testing.T) {
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		want    string
	}{
		{
			name: "not found",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNotFound)
				_, _ = fmt.Fprint(w, "<html><body>Not Found</body></html>")
			},
			want: "404 Not Found",
		},
		{
			name: "forbidden",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusForbidden)
			},
			want: "403 Forbidden",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(tc.handler)
			t.Cleanup(server.Close)
			_, err := newTestUpdater(server).fetcher.get(context.Background(), server.URL)
			if err == nil || !strings.HasSuffix(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to end with %q", err, tc.want)
			}
		})
	}
}

func TestShortBuild(t *testing.T) {
	for commit, want := range map[string]string{
		"":                    "unknown",
		"272b035":             "272b035",
		"272b035-dirty":       "272b035-dirty",
		"272b035ab":           "272b035",
		testCommit:            "1234567",
		testCommit + "-dirty": "1234567-dirty",
		"1.10.0-rc.1":         "1.10.0-rc.1",
	} {
		if got := ShortBuild(commit); got != want {
			t.Errorf("ShortBuild(%q) = %q, want %q", commit, got, want)
		}
	}
}
