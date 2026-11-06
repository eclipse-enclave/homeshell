// Package updater implements the daemon self-update flow described in
// docs/ARCHITECTURE.md, "Self-update": check a release channel, verify the download by
// checksum, atomically swap the running binary, and restart so connected SSE
// clients reconnect automatically.
//
// The concrete implementation wraps github.com/creativeprojects/go-selfupdate,
// but callers depend only on the Updater interface so tests can inject a fake
// release source (see updater_test.go) rather than reaching GitHub.
package updater

import (
	"context"
	"errors"
	"fmt"

	"github.com/creativeprojects/go-selfupdate"
)

// checksumFilename is the single checksum file GoReleaser produces for all
// assets; go-selfupdate's ChecksumValidator verifies each download against it.
const checksumFilename = "checksums.txt"

// ErrNoRelease is returned when the release channel has no asset matching the
// running platform (OS/arch), as opposed to a network or auth failure.
var ErrNoRelease = errors.New("no matching release found")

// Release describes a version available on the release channel. It is a small
// projection of the underlying library's release type so the rest of the
// daemon does not depend on go-selfupdate.
type Release struct {
	Version string
	URL     string
	// rel is the underlying release, carried so Apply can download it without
	// a second lookup. It is nil for releases constructed in tests.
	rel *selfupdate.Release
}

// Updater checks for and applies daemon updates. It is an interface so the
// HTTP/CLI layers can be exercised against a fake in tests.
type Updater interface {
	// Latest reports the newest release on the channel. It returns
	// ErrNoRelease when nothing matches the running platform.
	Latest(ctx context.Context) (Release, error)
	// Apply downloads, verifies, and atomically swaps the binary at exePath
	// with the given release. On checksum failure the running binary is left
	// untouched.
	Apply(ctx context.Context, rel Release, exePath string) error
}

// Config configures a GitHub-backed Updater.
type Config struct {
	// Owner and Repo identify the GitHub repository holding the releases.
	Owner string
	Repo  string
	// BaseURL, when set, points the updater at an HTTP release server
	// (go-selfupdate's HttpSource) instead of GitHub. It exists so tests — and
	// self-hosted deployments — can serve releases from a local/private
	// endpoint. Ignored when Source is set.
	BaseURL string
	// Source overrides the release source entirely. When nil, a source is
	// built from BaseURL (HTTP) or GitHub. Tests inject a fake here.
	Source selfupdate.Source
}

// githubUpdater is the production Updater backed by go-selfupdate.
type githubUpdater struct {
	up   *selfupdate.Updater
	repo selfupdate.Repository
}

// New builds a GitHub-backed Updater. When cfg.Source is nil it constructs an
// authenticated GitHub source, resolving a token from `gh` or the environment
// so private release assets can be downloaded.
func New(ctx context.Context, cfg Config) (Updater, error) {
	source := cfg.Source
	switch {
	case source != nil:
		// Explicit source (tests).
	case cfg.BaseURL != "":
		http, err := selfupdate.NewHttpSource(selfupdate.HttpConfig{BaseURL: cfg.BaseURL})
		if err != nil {
			return nil, fmt.Errorf("creating http source: %w", err)
		}
		source = http
	default:
		gh, err := selfupdate.NewGitHubSource(selfupdate.GitHubConfig{
			APIToken: resolveToken(ctx),
		})
		if err != nil {
			return nil, fmt.Errorf("creating github source: %w", err)
		}
		source = gh
	}

	up, err := selfupdate.NewUpdater(selfupdate.Config{
		Source:    source,
		Validator: &selfupdate.ChecksumValidator{UniqueFilename: checksumFilename},
	})
	if err != nil {
		return nil, fmt.Errorf("creating updater: %w", err)
	}

	return &githubUpdater{
		up:   up,
		repo: selfupdate.NewRepositorySlug(cfg.Owner, cfg.Repo),
	}, nil
}

func (g *githubUpdater) Latest(ctx context.Context) (Release, error) {
	rel, found, err := g.up.DetectLatest(ctx, g.repo)
	if err != nil {
		return Release{}, fmt.Errorf("detecting latest release: %w", err)
	}
	if !found || rel == nil {
		return Release{}, ErrNoRelease
	}
	return Release{Version: rel.Version(), URL: rel.URL, rel: rel}, nil
}

func (g *githubUpdater) Apply(ctx context.Context, rel Release, exePath string) error {
	if rel.rel == nil {
		return fmt.Errorf("apply: release %q has no downloadable asset", rel.Version)
	}
	if err := g.up.UpdateTo(ctx, rel.rel, exePath); err != nil {
		return fmt.Errorf("applying update to %s: %w", rel.Version, err)
	}
	return nil
}
