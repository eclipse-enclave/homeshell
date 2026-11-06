package provider_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/provider"
	"github.com/eclipsesource/homeshell/daemon/internal/store"
)

// recordingLauncher records launches instead of spawning a terminal window.
type recordingLauncher struct {
	mu    sync.Mutex
	calls int
	cmd   []string
	title string
	fail  error
}

func (l *recordingLauncher) Launch(_ context.Context, cmd []string, title string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls++
	l.cmd = cmd
	l.title = title
	return l.fail
}

// newOpenService wires a service over one project holding a single workstream.
func newOpenService(t *testing.T, ws domain.Workstream, providers ...provider.SessionProvider) (*provider.Service, *recordingLauncher) {
	t.Helper()
	st := store.NewTestStore(t, []domain.Project{{
		ID: "p1", Name: "p1", Path: t.TempDir(), Workstreams: []domain.Workstream{ws},
	}})
	reg := provider.NewRegistry()
	for _, p := range providers {
		if err := reg.RegisterSessionProvider(p); err != nil {
			t.Fatalf("register session provider %q: %v", p.ID(), err)
		}
	}
	launcher := &recordingLauncher{}
	svc := provider.NewService(st, reg, 0, provider.WithTerminal(launcher))
	return svc, launcher
}

func workstreamAt(dir string, sessions ...domain.Session) domain.Workstream {
	if sessions == nil {
		sessions = []domain.Session{}
	}
	return domain.Workstream{
		ID: "ws-1", Name: "test", Provider: provider.CoreProviderID,
		StrategyID: "worktree", WorkingDir: dir, Sessions: sessions,
	}
}

// containerProvider's sessions run in an environment of their own: it
// implements provider.ShellOpener, and openErr is what OpenShell reports.
type containerProvider struct {
	id       string
	openErr  error
	opened   []string // session IDs OpenShell was called with
	sessions map[string]domain.Session
}

var _ provider.ShellOpener = (*containerProvider)(nil)

func (c *containerProvider) ID() string                              { return c.id }
func (c *containerProvider) Label() string                           { return "Container " + c.id }
func (c *containerProvider) Available(context.Context) (bool, error) { return true, nil }
func (c *containerProvider) Capabilities() provider.Capabilities     { return provider.Capabilities{} }

func (c *containerProvider) SessionSchemas(context.Context, provider.WorkstreamRef) ([]provider.SessionSchemaOption, error) {
	return nil, nil
}

func (c *containerProvider) CreateSession(context.Context, provider.CreateSessionRequest) (domain.Session, error) {
	return domain.Session{}, nil
}
func (c *containerProvider) RemoveSession(context.Context, provider.SessionRef) error { return nil }

func (c *containerProvider) SessionState(context.Context, provider.SessionRef) (domain.Session, error) {
	return domain.Session{}, nil
}

func (c *containerProvider) Open(context.Context, provider.SessionRef, string) (provider.OpenResult, error) {
	return provider.OpenResult{}, nil
}

func (c *containerProvider) DiscoverSessions(context.Context, provider.ProjectRef) ([]provider.DiscoveredSession, error) {
	return nil, nil
}

func (c *containerProvider) OpenShell(_ context.Context, ref provider.SessionRef) (provider.OpenResult, error) {
	c.opened = append(c.opened, ref.SessionID)
	if c.openErr != nil {
		return provider.OpenResult{}, c.openErr
	}
	return provider.OpenResult{Mode: "terminal", Data: json.RawMessage(`{"target":"container"}`)}, nil
}

// hostProvider's sessions run straight on the host: no ShellOpener.
type hostProvider struct{ containerProvider }

func (h *hostProvider) OpenShell() {} // shadows the embedded method's signature, so the type assertion fails

func session(id, providerID string, state domain.SessionState) domain.Session {
	return domain.Session{ID: id, Title: id, Agent: "claude", Provider: providerID, State: state}
}

// The plain Open is the host folder even beside a container session: the
// container's shell is a mode of its own.
func TestOpenWorkstreamDefaultOpensTheHostDirectoryBesideAContainerSession(t *testing.T) {
	container := &containerProvider{id: "enclave"}
	svc, launcher := newOpenService(t,
		workstreamAt(t.TempDir(), session("sess-1", "enclave", domain.SessionRunning)), container)

	for _, mode := range []string{"", domain.OpenModeTerminal} {
		res, err := svc.OpenWorkstream(context.Background(), provider.WorkstreamRef{ProjectID: "p1", WorkstreamID: "ws-1"}, mode)
		if err != nil {
			t.Fatalf("OpenWorkstream(%q): %v", mode, err)
		}
		if !strings.Contains(string(res.Data), `"target":"host"`) {
			t.Errorf("OpenWorkstream(%q) data = %s; want the host directory", mode, res.Data)
		}
	}
	if len(container.opened) != 0 {
		t.Errorf("OpenShell calls = %v; want none", container.opened)
	}
	if launcher.calls != 2 {
		t.Errorf("host terminal launched %d times; want 2", launcher.calls)
	}
}

// Each container session is its own place, so the mode names the session.
func TestOpenWorkstreamSessionShellOpensThatSessionsContainer(t *testing.T) {
	container := &containerProvider{id: "enclave"}
	svc, launcher := newOpenService(t, workstreamAt(t.TempDir(),
		session("sess-1", "enclave", domain.SessionRunning),
		session("sess-2", "enclave", domain.SessionIdle)), container)

	res, err := svc.OpenWorkstream(context.Background(), provider.WorkstreamRef{ProjectID: "p1", WorkstreamID: "ws-1"},
		domain.SessionShellModeFor("sess-2"))
	if err != nil {
		t.Fatalf("OpenWorkstream: %v", err)
	}
	if string(res.Data) != `{"target":"container"}` {
		t.Errorf("result data = %s; want the provider's container result", res.Data)
	}
	if len(container.opened) != 1 || container.opened[0] != "sess-2" {
		t.Errorf("OpenShell calls = %v; want [sess-2]", container.opened)
	}
	if launcher.calls != 0 {
		t.Errorf("host terminal launched %d times; the provider owns this open", launcher.calls)
	}
}

// The user asked for that container, so there is no silent fallback to the
// host: a session without one is a conflict.
func TestOpenWorkstreamSessionShellWithoutAnEnvironment(t *testing.T) {
	gone := fmt.Errorf("%w: container is gone", provider.ErrNoShellEnvironment)
	tests := []struct {
		name    string
		state   domain.SessionState
		openErr error
	}{
		{"stopped", domain.SessionStopped, nil},
		{"still starting", domain.SessionStarting, nil},
		{"failed to start", domain.SessionError, nil},
		// The store's session list can be a poll behind the container being gone.
		{"container gone", domain.SessionRunning, gone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			container := &containerProvider{id: "enclave", openErr: tt.openErr}
			svc, launcher := newOpenService(t,
				workstreamAt(t.TempDir(), session("sess-1", "enclave", tt.state)), container)

			_, err := svc.OpenWorkstream(context.Background(), provider.WorkstreamRef{ProjectID: "p1", WorkstreamID: "ws-1"},
				domain.SessionShellModeFor("sess-1"))
			if !errors.Is(err, provider.ErrNoShellEnvironment) {
				t.Fatalf("error = %v; want ErrNoShellEnvironment", err)
			}
			if launcher.calls != 0 {
				t.Errorf("host terminal launched %d times; want no fallback", launcher.calls)
			}
		})
	}
	// Removed since the menu was drawn: stale, not a bad request.
	t.Run("session gone", func(t *testing.T) {
		svc, _ := newOpenService(t, workstreamAt(t.TempDir()), &containerProvider{id: "enclave"})
		_, err := svc.OpenWorkstream(context.Background(), provider.WorkstreamRef{ProjectID: "p1", WorkstreamID: "ws-1"},
			domain.SessionShellModeFor("sess-1"))
		if !errors.Is(err, provider.ErrNoShellEnvironment) {
			t.Fatalf("error = %v; want ErrNoShellEnvironment", err)
		}
	})
}

// A shell that was never offered is an unknown mode, not a conflict.
func TestOpenWorkstreamSessionShellThatWasNeverOffered(t *testing.T) {
	svc, _ := newOpenService(t,
		workstreamAt(t.TempDir(), session("sess-1", "local", domain.SessionRunning)),
		&hostProvider{containerProvider{id: "local"}})

	_, err := svc.OpenWorkstream(context.Background(), provider.WorkstreamRef{ProjectID: "p1", WorkstreamID: "ws-1"},
		domain.SessionShellModeFor("sess-1"))
	if !errors.Is(err, provider.ErrUnknownOpenMode) {
		t.Errorf("error = %v; want ErrUnknownOpenMode", err)
	}
}

func TestOpenWorkstreamWithHostSessionsOpensTheDirectory(t *testing.T) {
	svc, launcher := newOpenService(t,
		workstreamAt(t.TempDir(), session("sess-1", "local", domain.SessionRunning)),
		&hostProvider{containerProvider{id: "local"}})

	if _, err := svc.OpenWorkstream(context.Background(), provider.WorkstreamRef{ProjectID: "p1", WorkstreamID: "ws-1"}, ""); err != nil {
		t.Fatalf("OpenWorkstream: %v", err)
	}
	if launcher.calls != 1 {
		t.Errorf("host terminal launched %d times; want 1", launcher.calls)
	}
}

// Falling back here would silently drop the user into the wrong environment.
func TestOpenWorkstreamReportsAProviderFailure(t *testing.T) {
	container := &containerProvider{id: "enclave", openErr: errors.New("enclave exec refused")}
	svc, launcher := newOpenService(t,
		workstreamAt(t.TempDir(), session("sess-1", "enclave", domain.SessionRunning)), container)

	_, err := svc.OpenWorkstream(context.Background(), provider.WorkstreamRef{ProjectID: "p1", WorkstreamID: "ws-1"},
		domain.SessionShellModeFor("sess-1"))
	if err == nil || !strings.Contains(err.Error(), "enclave exec refused") {
		t.Fatalf("error = %v; want the provider's failure", err)
	}
	if launcher.calls != 0 {
		t.Errorf("host terminal launched %d times; a provider failure must not fall back", launcher.calls)
	}
}

func TestOpenWorkstreamLaunchesAShellInTheWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	svc, launcher := newOpenService(t, workstreamAt(dir))

	res, err := svc.OpenWorkstream(context.Background(), provider.WorkstreamRef{ProjectID: "p1", WorkstreamID: "ws-1"}, "")
	if err != nil {
		t.Fatalf("OpenWorkstream: %v", err)
	}
	if res.Mode != "terminal" {
		t.Errorf("mode = %q; want terminal", res.Mode)
	}
	var data struct {
		WorkingDir string `json:"workingDir"`
	}
	if err := json.Unmarshal(res.Data, &data); err != nil {
		t.Fatalf("unmarshal result data: %v", err)
	}
	if data.WorkingDir != dir {
		t.Errorf("result workingDir = %q; want %q", data.WorkingDir, dir)
	}
	if launcher.calls != 1 {
		t.Fatalf("launcher called %d times; want 1", launcher.calls)
	}
	if !strings.Contains(strings.Join(launcher.cmd, " "), dir) {
		t.Errorf("launched command %q does not mention %q", launcher.cmd, dir)
	}
	if launcher.title != "test" {
		t.Errorf("window title = %q; want the workstream name", launcher.title)
	}
}

func TestOpenWorkstreamUnknownWorkstream(t *testing.T) {
	svc, launcher := newOpenService(t, workstreamAt(t.TempDir()))

	_, err := svc.OpenWorkstream(context.Background(), provider.WorkstreamRef{ProjectID: "p1", WorkstreamID: "nope"}, "")
	if !errors.Is(err, provider.ErrWorkstreamNotFound) {
		t.Errorf("error = %v; want ErrWorkstreamNotFound", err)
	}
	if launcher.calls != 0 {
		t.Errorf("launcher called %d times; want none", launcher.calls)
	}
}

// A creation placeholder has no directory yet, so there is nothing to open.
func TestOpenWorkstreamWithoutWorkingDir(t *testing.T) {
	svc, launcher := newOpenService(t, workstreamAt(""))

	_, err := svc.OpenWorkstream(context.Background(), provider.WorkstreamRef{ProjectID: "p1", WorkstreamID: "ws-1"}, "")
	if !errors.Is(err, provider.ErrNoWorkingDir) {
		t.Errorf("error = %v; want ErrNoWorkingDir", err)
	}
	if launcher.calls != 0 {
		t.Errorf("launcher called %d times; want none", launcher.calls)
	}
}

// A WorkingDir is recorded but gone: launching a terminal would fail to cd.
func TestOpenWorkstreamMissingDirectory(t *testing.T) {
	svc, launcher := newOpenService(t, workstreamAt(filepath.Join(t.TempDir(), "gone")))

	_, err := svc.OpenWorkstream(context.Background(), provider.WorkstreamRef{ProjectID: "p1", WorkstreamID: "ws-1"}, "")
	if !errors.Is(err, provider.ErrNoWorkingDir) {
		t.Errorf("error = %v; want ErrNoWorkingDir", err)
	}
	if launcher.calls != 0 {
		t.Errorf("launcher called %d times; want none", launcher.calls)
	}
}

func TestOpenWorkstreamRejectsAFileWorkingDir(t *testing.T) {
	file := filepath.Join(t.TempDir(), "a-file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	svc, launcher := newOpenService(t, workstreamAt(file))

	_, err := svc.OpenWorkstream(context.Background(), provider.WorkstreamRef{ProjectID: "p1", WorkstreamID: "ws-1"}, "")
	if !errors.Is(err, provider.ErrNoWorkingDir) {
		t.Errorf("error = %v; want ErrNoWorkingDir", err)
	}
	if launcher.calls != 0 {
		t.Errorf("launcher called %d times; want none", launcher.calls)
	}
}

func TestOpenWorkstreamReportsLauncherFailure(t *testing.T) {
	dir := t.TempDir()
	svc, launcher := newOpenService(t, workstreamAt(dir))
	launcher.fail = errors.New("no terminal emulator found")

	_, err := svc.OpenWorkstream(context.Background(), provider.WorkstreamRef{ProjectID: "p1", WorkstreamID: "ws-1"}, "")
	if err == nil {
		t.Fatal("want an error when the launcher fails")
	}
	if err.Error() != "could not open a terminal: no terminal emulator found" {
		t.Errorf("error = %v; want it to carry the launcher's reason", err)
	}
	// A launcher failure is not a missing directory.
	if errors.Is(err, provider.ErrNoWorkingDir) {
		t.Error("launcher failure should not report as ErrNoWorkingDir")
	}
}

// linkProvider is a session provider declaring one browser-openable link.
type linkProvider struct {
	containerProvider
	links []provider.Link
	url   string
	err   error
}

func (l *linkProvider) Links(domain.Session) []provider.Link { return l.links }

func (l *linkProvider) LinkURL(context.Context, provider.SessionRef, string) (string, error) {
	return l.url, l.err
}

func TestOpenWorkstreamLinkReturnsTheURLForTheClient(t *testing.T) {
	web := &linkProvider{
		containerProvider: containerProvider{id: "enclave"},
		links:             []provider.Link{{ID: "web-ide", Label: "Web IDE"}},
		url:               "http://127.0.0.1:3000",
	}
	svc, launcher := newOpenService(t,
		workstreamAt(t.TempDir(), session("sess-1", "enclave", domain.SessionRunning)), web)

	res, err := svc.OpenWorkstream(context.Background(),
		provider.WorkstreamRef{ProjectID: "p1", WorkstreamID: "ws-1"}, "link:web-ide")
	if err != nil {
		t.Fatalf("OpenWorkstream: %v", err)
	}
	if res.Mode != "link:web-ide" {
		t.Errorf("mode = %q; want link:web-ide", res.Mode)
	}
	var data struct {
		Target string `json:"target"`
		URL    string `json:"url"`
	}
	if err := json.Unmarshal(res.Data, &data); err != nil {
		t.Fatalf("unmarshal result data: %v", err)
	}
	if data.URL != "http://127.0.0.1:3000" || data.Target != "browser" {
		t.Errorf("result data = %+v; want the URL for the browser to open", data)
	}
	// The daemon hands over a URL; it never opens what is behind it.
	if launcher.calls != 0 {
		t.Errorf("terminal launched %d times; want none", launcher.calls)
	}
}

func TestOpenWorkstreamLinkWhenNothingIsServing(t *testing.T) {
	web := &linkProvider{
		containerProvider: containerProvider{id: "enclave"},
		links:             []provider.Link{{ID: "web-ide", Label: "Web IDE"}},
		err:               fmt.Errorf("%w: nothing is serving", provider.ErrNoLink),
	}
	svc, _ := newOpenService(t,
		workstreamAt(t.TempDir(), session("sess-1", "enclave", domain.SessionRunning)), web)

	_, err := svc.OpenWorkstream(context.Background(),
		provider.WorkstreamRef{ProjectID: "p1", WorkstreamID: "ws-1"}, "link:web-ide")
	if !errors.Is(err, provider.ErrNoLink) {
		t.Errorf("error = %v; want ErrNoLink", err)
	}
}

// The provider is live but never declared this link id.
func TestOpenWorkstreamLinkWithoutAProviderDeclaringIt(t *testing.T) {
	web := &linkProvider{
		containerProvider: containerProvider{id: "enclave"},
		links:             []provider.Link{{ID: "web-ide", Label: "Web IDE"}},
		url:               "http://127.0.0.1:3000",
	}
	svc, _ := newOpenService(t,
		workstreamAt(t.TempDir(), session("sess-1", "enclave", domain.SessionRunning)), web)

	// A live session can declare links but not this one, so the id is the bad
	// part of the request rather than the workstream's state.
	_, err := svc.OpenWorkstream(context.Background(),
		provider.WorkstreamRef{ProjectID: "p1", WorkstreamID: "ws-1"}, "link:dashboard")
	if !errors.Is(err, provider.ErrUnknownOpenMode) {
		t.Errorf("error = %v; want ErrUnknownOpenMode", err)
	}
}

func TestOpenWorkstreamLinkSkipsStoppedSessions(t *testing.T) {
	web := &linkProvider{
		containerProvider: containerProvider{id: "enclave"},
		links:             []provider.Link{{ID: "web-ide", Label: "Web IDE"}},
		url:               "http://127.0.0.1:3000",
	}
	svc, _ := newOpenService(t,
		workstreamAt(t.TempDir(), session("sess-1", "enclave", domain.SessionStopped)), web)

	_, err := svc.OpenWorkstream(context.Background(),
		provider.WorkstreamRef{ProjectID: "p1", WorkstreamID: "ws-1"}, "link:web-ide")
	if !errors.Is(err, provider.ErrNoLink) {
		t.Errorf("error = %v; want ErrNoLink (a stopped session serves nothing)", err)
	}
}

func TestOpenWorkstreamRejectsAnUnknownMode(t *testing.T) {
	svc, launcher := newOpenService(t, workstreamAt(t.TempDir()))

	// A bare prefix names no session.
	for _, mode := range []string{"telepathy", domain.OpenModeSessionShellPrefix} {
		_, err := svc.OpenWorkstream(context.Background(),
			provider.WorkstreamRef{ProjectID: "p1", WorkstreamID: "ws-1"}, mode)
		if !errors.Is(err, provider.ErrUnknownOpenMode) {
			t.Errorf("mode %q: error = %v; want ErrUnknownOpenMode", mode, err)
		}
	}
	if launcher.calls != 0 {
		t.Errorf("terminal launched %d times; an unknown mode must not fall back", launcher.calls)
	}
}
