// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

// Package updater implements the daemon self-update flow described in
// docs/ARCHITECTURE.md, "Self-update": check the release channel the binary
// was published on, verify the download by checksum, atomically swap the
// running binary, and restart so connected SSE clients reconnect.
package updater

import (
	"archive/tar"
	"bufio"
	"cmp"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode"
)

const (
	// DefaultBaseURL is the project's directory on the Eclipse download
	// server; each channel publishes below it (see docs/DEV.md, Releases).
	DefaultBaseURL = "https://download.eclipse.org/enclave/homeshell"

	ChannelRolling = "rolling"
	ChannelStable  = "stable"

	checksumsFilename     = "checksums.txt"
	latestFilename        = "latest"
	binaryName            = "homeshell"
	maxChecksumsBytes     = 1 << 20
	maxLatestBytes        = 4 << 10
	maxDownloadBytes      = 256 << 20
	responseHeaderTimeout = 30 * time.Second
	responseIdleTimeout   = 30 * time.Second
	updateTempPattern     = ".homeshell-update-*"
	unknownBuildValue     = "unknown"
	// staleUpdateTempAge exceeds any install's timeout, so a temporary file
	// this old belongs to an install that was killed before cleaning up.
	staleUpdateTempAge = time.Hour
)

var (
	// ErrUnsupported reports a binary that must not replace itself: a
	// package, app bundle or source build, or an unsupported platform.
	ErrUnsupported = errors.New("self-update is not supported")
	// ErrNotWritable reports an executable this user cannot replace; the
	// updater does not elevate privileges.
	ErrNotWritable = errors.New("not writable")
)

// Updater checks one release channel and installs its builds. Service depends
// on the interface so tests can inject a fake.
type Updater interface {
	// Supported returns an error wrapping ErrUnsupported when this binary
	// must not replace itself, or nil.
	Supported() error
	// Check compares the running build with the channel's newest one.
	Check(ctx context.Context) (Status, error)
	// Install replaces the executable at exePath with the build that status,
	// as returned by Check, names.
	Install(ctx context.Context, status Status, exePath string) error
}

// Config identifies the running build, as stamped at build time.
type Config struct {
	Version string
	Commit  string
	// Channel is the release channel the binary was published on; empty for
	// packages, the macOS app and source builds, which never update themselves.
	Channel string
	// BaseURL overrides DefaultBaseURL (dev and e2e runs).
	BaseURL string
}

// build is what the running binary knows about itself.
type build struct {
	Version string
	Commit  string
}

type channelUpdater struct {
	fetcher          fetcher
	sources          map[string]source
	channel          string
	goos             string
	goarch           string
	current          build
	maxDownloadBytes int64
}

// New returns the Updater for the channel the running binary was published on.
func New(cfg Config) Updater {
	// No Client.Timeout: it would also cap reading the archive, failing every
	// download on a slow link. Callers bound each operation with a context.
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment}
	if defaultTransport, ok := http.DefaultTransport.(*http.Transport); ok {
		transport = defaultTransport.Clone()
	}
	transport.ResponseHeaderTimeout = responseHeaderTimeout
	fetcher := fetcher{client: &http.Client{Transport: transport}, idleTimeout: responseIdleTimeout}
	base := strings.TrimSuffix(cmp.Or(cfg.BaseURL, DefaultBaseURL), "/")
	return &channelUpdater{
		fetcher: fetcher,
		sources: map[string]source{
			ChannelRolling: newRollingSource(fetcher, base),
			ChannelStable:  newStableSource(fetcher, base),
		},
		channel:          strings.TrimSpace(cfg.Channel),
		goos:             runtime.GOOS,
		goarch:           runtime.GOARCH,
		current:          build{Version: cfg.Version, Commit: cfg.Commit},
		maxDownloadBytes: maxDownloadBytes,
	}
}

func (u *channelUpdater) Supported() error {
	if _, err := artifactName(u.goos, u.goarch); err != nil {
		return err
	}
	if u.channel == "" {
		return fmt.Errorf("%w: only the standalone release archives replace themselves, but this is a package, app or source build (version %q); update it the way it was installed", ErrUnsupported, u.current.Version)
	}
	if _, ok := u.sources[u.channel]; !ok {
		return fmt.Errorf("%w: unknown release channel %q", ErrUnsupported, u.channel)
	}
	return nil
}

// Check compares the running build with the newest one on its channel. An
// unsupported build reports its own version as latest, not an error, so a
// source build does not look like a failed release check.
func (u *channelUpdater) Check(ctx context.Context) (Status, error) {
	if err := u.Supported(); err != nil {
		return Status{Current: u.current.Version, Latest: u.current.Version}, nil
	}
	src := u.sources[u.channel]
	latest, err := src.latest(ctx)
	if err != nil {
		return Status{}, err
	}
	return Status{
		Channel:         u.channel,
		Current:         src.label(u.current),
		Latest:          ShortBuild(latest),
		UpdateAvailable: !src.upToDate(u.current, latest),
		build:           latest,
	}, nil
}

func (u *channelUpdater) Install(ctx context.Context, status Status, target string) error {
	if err := u.Supported(); err != nil {
		return err
	}
	latest := status.build
	if latest == "" {
		return errors.New("install: no build to install; check for updates first")
	}
	artifact, err := artifactName(u.goos, u.goarch)
	if err != nil {
		return err
	}
	info, err := os.Stat(target)
	if err != nil {
		return fmt.Errorf("inspect current executable %s: %w", target, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("current executable is not a regular file: %s", target)
	}
	removeStaleUpdates(filepath.Dir(target), time.Now())

	// Created before any request, so an unwritable directory fails offline.
	tmp, err := os.CreateTemp(filepath.Dir(target), updateTempPattern)
	if errors.Is(err, fs.ErrPermission) {
		return u.notWritable(target, latest)
	}
	if err != nil {
		return fmt.Errorf("create update beside %s: %w", target, err)
	}
	tmpPath := tmp.Name()
	installed := false
	defer func() {
		_ = tmp.Close()
		if !installed {
			_ = os.Remove(tmpPath)
		}
	}()

	src := u.sources[u.channel]
	if err := confirmLatest(ctx, src, u.channel, latest); err != nil {
		return err
	}
	expected, err := u.expectedChecksum(ctx, src, latest, artifact)
	if err != nil {
		return err
	}
	actual, err := u.extractBinary(ctx, src.assetURL(latest, artifact), artifact, tmp)
	if err != nil {
		return err
	}
	// A publish swaps in a build's whole directory before naming it latest
	// and only moves forward, so a channel naming latest before and after the
	// download means the checksums and the archive both came from that build.
	if err := confirmLatest(ctx, src, u.channel, latest); err != nil {
		return err
	}
	if actual != expected {
		return fmt.Errorf("checksum mismatch for %s: got %s, want %s", artifact, actual, expected)
	}

	if err := tmp.Chmod(info.Mode().Perm()); err != nil {
		return fmt.Errorf("set update permissions: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync update: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close update: %w", err)
	}
	err = os.Rename(tmpPath, target)
	if errors.Is(err, fs.ErrPermission) {
		// A sticky directory such as /tmp lets anyone create the temp file
		// but refuses to replace another user's executable.
		return u.notWritable(target, latest)
	}
	if err != nil {
		return fmt.Errorf("replace %s: %w", target, err)
	}
	installed = true
	if err := syncDir(filepath.Dir(target)); err != nil {
		return fmt.Errorf("sync executable directory: %w", err)
	}
	return nil
}

// extractBinary streams the archive at url, writes its homeshell entry to dst
// and returns the sha256 of the whole compressed archive. The caller must
// compare it before trusting dst.
func (u *channelUpdater) extractBinary(ctx context.Context, url string, artifact string, dst io.Writer) (string, error) {
	response, err := u.fetcher.get(ctx, url)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", artifact, err)
	}
	defer func() { _ = response.Body.Close() }()

	hasher := sha256.New()
	limited := &io.LimitedReader{R: response.Body, N: u.maxDownloadBytes + 1}
	stream := io.TeeReader(limited, hasher)
	// A read cut off by the limit fails inside gzip or tar; name the cause.
	readErr := func(err error) error {
		if limited.N <= 0 {
			return fmt.Errorf("download %s exceeds %d bytes", artifact, u.maxDownloadBytes)
		}
		return fmt.Errorf("read %s: %w", artifact, err)
	}

	gz, err := gzip.NewReader(stream)
	if err != nil {
		return "", readErr(err)
	}
	archive := tar.NewReader(gz)
	found := false
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", readErr(err)
		}
		if path.Clean(header.Name) != binaryName {
			continue
		}
		if found || header.Typeflag != tar.TypeReg {
			return "", fmt.Errorf("%s does not hold exactly one regular %s file", artifact, binaryName)
		}
		found = true
		// The download limit bounds the compressed bytes; this bounds what
		// they expand to.
		written, err := io.Copy(dst, io.LimitReader(archive, u.maxDownloadBytes+1))
		if err != nil {
			return "", readErr(err)
		}
		if written > u.maxDownloadBytes {
			return "", fmt.Errorf("%s in %s exceeds %d bytes", binaryName, artifact, u.maxDownloadBytes)
		}
	}
	if !found {
		return "", fmt.Errorf("%s has no %s binary", artifact, binaryName)
	}
	// Reading to the end checks gzip's CRC, and the drain hashes any trailing
	// bytes, so the checksum covers the whole file as published.
	if _, err := io.Copy(io.Discard, gz); err != nil {
		return "", readErr(err)
	}
	if _, err := io.Copy(io.Discard, stream); err != nil {
		return "", readErr(err)
	}
	if limited.N <= 0 {
		return "", readErr(nil)
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// notWritable spells out the manual reinstall, verification included, so it
// is no less safe than the update it replaces.
func (u *channelUpdater) notWritable(target string, latest string) error {
	artifact, err := artifactName(u.goos, u.goarch)
	if err != nil {
		return err
	}
	src := u.sources[u.channel]
	return fmt.Errorf("homeshell cannot replace %s: its location is %w for this user and the updater does not elevate privileges; download %s and %s into one directory and reinstall from there with: %s",
		target, ErrNotWritable, src.assetURL(latest, artifact), src.assetURL(latest, checksumsFilename), u.reinstallCommand(artifact, target))
}

func (u *channelUpdater) reinstallCommand(artifact string, target string) string {
	verify := "sha256sum --check --ignore-missing " + checksumsFilename
	if u.goos == "darwin" {
		verify = "shasum -a 256 --check --ignore-missing " + checksumsFilename
	}
	command := verify + " && tar -xzf " + artifact + " " + binaryName
	if u.goos == "darwin" {
		// xattr -c drops the quarantine attribute of a browser download and,
		// unlike xattr -d, succeeds when there is none.
		command += " && xattr -c " + binaryName
	}
	return command + " && sudo install " + binaryName + " " + shellArg(target)
}

// shellArg quotes path for a command line unless it is a plain word.
func shellArg(path string) string {
	plain := path != "" && !strings.ContainsFunc(path, func(r rune) bool {
		return !strings.ContainsRune("/._-+", r) && (r > unicode.MaxASCII || !unicode.IsLetter(r) && !unicode.IsDigit(r))
	})
	if plain {
		return path
	}
	return "'" + strings.ReplaceAll(path, "'", `'"'"'`) + "'"
}

func confirmLatest(ctx context.Context, src source, channel string, latest string) error {
	now, err := src.latest(ctx)
	if err != nil {
		return err
	}
	if now != latest {
		return fmt.Errorf("%s release moved from %s to %s; run the update again", channel, ShortBuild(latest), ShortBuild(now))
	}
	return nil
}

// removeStaleUpdates deletes temporary downloads that an interrupted install
// left beside the executable; a signal or crash skips Install's own cleanup.
func removeStaleUpdates(dir string, now time.Time) {
	matches, _ := filepath.Glob(filepath.Join(dir, updateTempPattern))
	for _, path := range matches {
		info, err := os.Lstat(path)
		if err == nil && info.Mode().IsRegular() && now.Sub(info.ModTime()) > staleUpdateTempAge {
			_ = os.Remove(path)
		}
	}
}

func (u *channelUpdater) expectedChecksum(ctx context.Context, src source, latest string, artifact string) (string, error) {
	response, err := u.fetcher.get(ctx, src.assetURL(latest, checksumsFilename))
	if err != nil {
		return "", fmt.Errorf("download %s: %w", checksumsFilename, err)
	}
	defer func() { _ = response.Body.Close() }()

	scanner := bufio.NewScanner(io.LimitReader(response.Body, maxChecksumsBytes))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != artifact {
			continue
		}
		digest := strings.ToLower(fields[0])
		if len(digest) != sha256.Size*2 || !validHex(digest) {
			return "", fmt.Errorf("invalid checksum for %s", artifact)
		}
		return digest, nil
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read %s: %w", checksumsFilename, err)
	}
	return "", fmt.Errorf("%s has no checksum for %s", checksumsFilename, artifact)
}

func artifactName(goos string, goarch string) (string, error) {
	if (goos != "linux" && goos != "darwin") || (goarch != "amd64" && goarch != "arm64") {
		return "", fmt.Errorf("%w on %s/%s", ErrUnsupported, goos, goarch)
	}
	return binaryName + "-" + goos + "-" + goarch + ".tar.gz", nil
}

func commitsEqual(current string, latest string) bool {
	current = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(current), "-dirty"))
	latest = strings.ToLower(strings.TrimSpace(latest))
	if current == "" || current == unknownBuildValue {
		return false
	}
	return strings.HasPrefix(current, latest) || strings.HasPrefix(latest, current)
}

// compareVersions orders release versions as semantic versions (1.2.0 <
// 1.10.0, 1.3.0-rc.1 < 1.3.0). A current version that is not a release
// version, such as dev or git describe output, sorts below every release so
// that it is offered the update.
func compareVersions(current string, latest string) int {
	current = strings.TrimPrefix(strings.TrimSpace(current), "v")
	if !releaseVersionPattern.MatchString(current) {
		return -1
	}
	currentCore, currentPre, _ := strings.Cut(current, "-")
	latestCore, latestPre, _ := strings.Cut(strings.TrimPrefix(strings.TrimSpace(latest), "v"), "-")
	if c := compareIdentifiers(strings.Split(currentCore, "."), strings.Split(latestCore, ".")); c != 0 {
		return c
	}
	switch {
	case currentPre == "" && latestPre == "":
		return 0
	case currentPre == "":
		return 1
	case latestPre == "":
		return -1
	}
	return compareIdentifiers(strings.Split(currentPre, "."), strings.Split(latestPre, "."))
}

// compareIdentifiers follows semantic versioning: numeric identifiers compare
// as numbers and sort below alphanumeric ones, and a shorter list of
// otherwise equal identifiers sorts first.
func compareIdentifiers(a []string, b []string) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		aNumeric, bNumeric := isNumeric(a[i]), isNumeric(b[i])
		var c int
		switch {
		case aNumeric && bNumeric:
			c = compareNumbers(a[i], b[i])
		case aNumeric:
			c = -1
		case bNumeric:
			c = 1
		default:
			c = strings.Compare(a[i], b[i])
		}
		if c != 0 {
			return c
		}
	}
	return cmp.Compare(len(a), len(b))
}

func isNumeric(s string) bool {
	return s != "" && !strings.ContainsFunc(s, func(r rune) bool { return r < '0' || r > '9' })
}

// compareNumbers orders decimal strings of any length by magnitude.
func compareNumbers(a string, b string) int {
	a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
	if c := cmp.Compare(len(a), len(b)); c != 0 {
		return c
	}
	return strings.Compare(a, b)
}

// ShortBuild abbreviates a commit identifying a build to seven characters;
// versions are kept.
func ShortBuild(build string) string {
	if build == "" {
		return unknownBuildValue
	}
	base, dirty := strings.CutSuffix(build, "-dirty")
	if len(base) > 7 && validHex(base) {
		base = base[:7]
	}
	if dirty {
		return base + "-dirty"
	}
	return base
}

func validHex(value string) bool {
	return value != "" && !strings.ContainsFunc(value, func(r rune) bool {
		return (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F')
	})
}

// syncDir makes a rename in dir durable.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}

// resolvedExecutablePath is the running binary, symlinks resolved, so an
// update replaces the file rather than the link to it.
func resolvedExecutablePath() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved, nil
	}
	return path, nil
}
