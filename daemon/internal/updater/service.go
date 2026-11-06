package updater

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"
)

// executablePath is indirected so tests can supply a fake binary path.
var executablePath = os.Executable

// now is indirected so cache-expiry tests need not sleep.
var now = time.Now

// Cache TTLs for the release check. A successful result is cached long enough
// to keep routine SSE reconnects (each re-fetches /api/version) well under
// GitHub's 60 req/hr unauthenticated limit, while failures are cached briefly
// so a transient GitHub error is not hammered on every reconnect yet recovers
// quickly.
const (
	checkTTL      = 5 * time.Minute
	checkErrorTTL = 30 * time.Second
)

// Status reports the running version alongside the newest available one.
type Status struct {
	Current         string `json:"current"`
	Latest          string `json:"latest"`
	UpdateAvailable bool   `json:"updateAvailable"`
}

// Service orchestrates the self-update flow for a specific running version. It
// is shared by the HTTP API and the CLI so both behave identically.
type Service struct {
	up      Updater
	current string

	mu        sync.Mutex
	cached    Status
	cachedErr error
	cacheSet  bool
	expires   time.Time
}

// NewService wraps an Updater with the daemon's running version.
func NewService(up Updater, current string) *Service {
	return &Service{up: up, current: current}
}

// Current returns the running version.
func (s *Service) Current() string { return s.current }

// Check reports whether a newer release is available. A missing release
// (ErrNoRelease) is not an error: it yields a Status with the current version
// as latest and UpdateAvailable false.
//
// The result is cached behind a short TTL (checkTTL, or checkErrorTTL for
// failures) so the repeated calls the UI makes on every SSE reconnect do not
// each trigger an unauthenticated GitHub release check.
func (s *Service) Check(ctx context.Context) (Status, error) {
	s.mu.Lock()
	if s.cacheSet && now().Before(s.expires) {
		status, err := s.cached, s.cachedErr
		s.mu.Unlock()
		return status, err
	}
	s.mu.Unlock()

	status, err := s.check(ctx)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.cacheSet = true
	if err != nil {
		// Cache the failure briefly so a flaky source is not hammered.
		s.cached, s.cachedErr, s.expires = Status{}, err, now().Add(checkErrorTTL)
		return Status{}, err
	}
	s.cached, s.cachedErr, s.expires = status, nil, now().Add(checkTTL)
	return status, nil
}

// check performs the uncached release lookup.
func (s *Service) check(ctx context.Context) (Status, error) {
	latest, err := s.up.Latest(ctx)
	if err != nil {
		if err == ErrNoRelease {
			return Status{Current: s.current, Latest: s.current}, nil
		}
		return Status{}, err
	}
	return Status{
		Current:         s.current,
		Latest:          latest.Version,
		UpdateAvailable: isNewer(latest.Version, s.current),
	}, nil
}

// invalidate drops any cached release-check result so the next Check hits the
// source. Called after an update attempt, whose outcome changes availability.
func (s *Service) invalidate() {
	s.mu.Lock()
	s.cacheSet, s.expires = false, time.Time{}
	s.mu.Unlock()
}

// Apply checks for a newer release and, if one exists, downloads and verifies
// it, atomically swaps the running binary, and restarts the daemon.
//
// The onReady callback, if non-nil, is invoked exactly once after the binary
// has been verified and swapped but before the process restarts. It lets the
// caller flush an HTTP response or print a message while the swap is already
// durable, so clients see success right before the SSE stream drops and
// reconnects to the new version.
//
// When already up to date it returns (current, false, nil) without calling
// onReady or restarting. A download/verification failure leaves the running
// binary untouched and returns the error. On a successful swap the process is
// replaced by a fresh one, so Apply normally does not return; it only returns
// (version, true, nil) if restartFunc is stubbed in tests.
func (s *Service) Apply(ctx context.Context, onReady func(version string)) (version string, updated bool, err error) {
	latest, err := s.up.Latest(ctx)
	if err != nil {
		if err == ErrNoRelease {
			return s.current, false, nil
		}
		return "", false, err
	}
	if !isNewer(latest.Version, s.current) {
		return s.current, false, nil
	}

	exe, err := executablePath()
	if err != nil {
		return "", false, fmt.Errorf("locating executable: %w", err)
	}
	slog.Info("self-update: downloading and verifying", "version", latest.Version, "exe", exe)
	if err := s.up.Apply(ctx, latest, exe); err != nil {
		// A verification or download failure leaves the binary untouched.
		return "", false, err
	}
	slog.Info("self-update: binary swapped successfully", "version", latest.Version)
	// The swapped-in binary changes what a release check reports; drop the
	// cache so a stubbed restart (tests) or any post-swap Check is accurate.
	s.invalidate()

	if onReady != nil {
		onReady(latest.Version)
	}

	// syscall.Exec replaces the process image: the OS closes all file
	// descriptors (including the listening socket) and loads the new binary.
	// No explicit shutdown is needed — the new binary binds the port fresh.
	slog.Info("self-update: exec-ing into new binary", "exe", exe)
	if err := restartFunc(exe, os.Args[1:], os.Environ()); err != nil {
		return "", false, fmt.Errorf("restarting after update: %w", err)
	}
	// Unreachable when restartFunc replaces the process; reached only if a
	// test stubs restartFunc to return nil.
	return latest.Version, true, nil
}
