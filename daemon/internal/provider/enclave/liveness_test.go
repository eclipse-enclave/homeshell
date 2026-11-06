//go:build dev

package enclave

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/provider"
)

// Watch-driven projection reuses terminal observations, not a second CLI or
// socket poll. Container exit remains a durable ended session after cleanup.
func TestWatchProjectsBridgeExitWithoutPeriodicInventory(t *testing.T) {
	p, cli, bridge, project := bridgeProvider(t)
	created := createBridge(t, p, project)
	pastStartup(p, created.ID)
	registry := provider.NewRegistry()
	if err := registry.RegisterSessionProvider(p); err != nil {
		t.Fatal(err)
	}
	reconciler := provider.NewReconciler(p.store, registry, 0)
	reconcile := func() {
		t.Helper()
		if err := reconciler.ReconcileProject(context.Background(), project.ID); err != nil {
			t.Fatal(err)
		}
	}
	reconcile()
	poll := p.changeDetector()
	poll(context.Background())
	if poll(context.Background()) {
		t.Fatal("unchanged bridge reported a change")
	}
	if cli.listCalls != 0 || cli.allCalls != 0 {
		t.Fatal("live polling queried container inventory")
	}
	bridge.live = false
	cli.all = map[string][]Container{project.Path: {{Name: created.ID, Status: "exited"}}}
	if !poll(context.Background()) {
		t.Fatal("exit did not trigger projection")
	}
	p.waitForBridgeSamples()
	// Cleanup completes off the hot path; the next tick publishes its result.
	poll(context.Background())
	reconcile()
	if got := p.store.Projects()[0].Workstreams[0].Sessions; len(got) != 1 || got[0].State != domain.SessionDone {
		t.Fatalf("sessions=%+v", got)
	}
	if len(cli.stopped) != 1 || cli.stopped[0] != created.ID {
		t.Fatalf("exit cleanup=%v", cli.stopped)
	}
	if poll(context.Background()) {
		t.Fatal("completed cleanup reported another change")
	}
}

func TestBridgeRecoveryReportsUnknownWhenContainerCheckFails(t *testing.T) {
	p, cli, bridge, project := bridgeProvider(t)
	createBridge(t, p, project)
	before := discoverBridge(t, p, project)
	bridge.live = false
	cli.allErr = errors.New("Docker unavailable")
	if got := discoverBridge(t, p, project); got.State != domain.SessionUnknown || got.Preview != before.Preview || got.Error != "" || bridgeReadError(got) == "" {
		t.Fatalf("failed inventory did not report uncertain liveness: %+v", got)
	}
	if len(bridge.starts) != 1 || len(cli.stopped) != 0 {
		t.Fatal("uncertain liveness restarted or stopped container")
	}
}

// A container that dies right after attaching keeps its container for `docker
// logs` and fails the session; a later exit is an ordinary end and is cleaned up.
func TestBridgeExitRightAfterStartFailsSessionAndKeepsContainer(t *testing.T) {
	for _, tt := range []struct {
		name       string
		after      time.Duration
		recognized bool
		wantState  domain.SessionState
		wantStops  int
	}{
		{"right after start", time.Second, false, domain.SessionError, 0},
		{"right after a recognized screen", time.Second, true, domain.SessionDone, 1},
		{"later", time.Minute, false, domain.SessionDone, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p, cli, bridge, project := bridgeProvider(t)
			advance := recoveryClock(p)
			created := createBridge(t, p, project)
			if tt.recognized {
				discoverBridge(t, p, project)
			}
			advance(tt.after)
			bridge.live = false
			cli.all = map[string][]Container{project.Path: {{Name: created.ID, Status: "exited"}}}
			got := discoverBridge(t, p, project)
			if got.State != tt.wantState || len(cli.stopped) != tt.wantStops {
				t.Fatalf("state=%s stops=%v", got.State, cli.stopped)
			}
			if tt.wantState == domain.SessionError && !strings.Contains(got.Error, "docker logs "+created.ID) {
				t.Fatalf("error=%q", got.Error)
			}
			ref := provider.SessionRef{ProjectID: project.ID, SessionID: created.ID}
			if _, err := p.Open(context.Background(), ref, "terminal"); !errors.Is(err, provider.ErrSessionStopped) {
				t.Fatalf("open=%v", err)
			}
			if err := p.RemoveSession(context.Background(), ref); err != nil {
				t.Fatal(err)
			}
			if !slices.Contains(cli.stopped, created.ID) {
				t.Fatal("removing the session left its container")
			}
		})
	}
}
