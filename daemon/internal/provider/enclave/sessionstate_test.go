package enclave

import (
	"context"
	"testing"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/provider"
	"github.com/eclipsesource/homeshell/daemon/internal/store"
)

func TestSessionStateReadsContainer(t *testing.T) {
	cli := &fakeCLI{
		available: true,
		containers: map[string][]Container{
			"/tmp/project": {
				{Name: "enclave-claude-abc", Tool: "claude", Directory: "/tmp/project", Status: "running"},
			},
		},
	}
	p := newForTest(t, cli)
	p.SetStore(store.NewTestStore(t, []domain.Project{{ID: "p1", Name: "p", Path: "/tmp/project"}}))

	sess, err := p.SessionState(context.Background(), provider.SessionRef{
		ProjectID: "p1", WorkstreamID: "ws1", SessionID: "enclave-claude-abc",
	})
	if err != nil {
		t.Fatalf("SessionState: %v", err)
	}
	if sess.ID != "enclave-claude-abc" {
		t.Errorf("id = %q, want the container name", sess.ID)
	}
	if sess.State != domain.SessionRunning {
		t.Errorf("state = %q, want running", sess.State)
	}
	if sess.Agent != "claude" {
		t.Errorf("agent = %q, want claude", sess.Agent)
	}
}

func TestSessionStateReadsOwnedSessionWithoutInventory(t *testing.T) {
	// The CLI has no listing; an owned session comes from its bridge.
	cli := &fakeCLI{available: true, runResult: "enclave-claude-new"}
	p := newForTest(t, cli)
	p.SetStore(store.NewTestStore(t, []domain.Project{{ID: "p1", Name: "p", Path: "/tmp/project"}}))

	if _, err := p.CreateSession(context.Background(), provider.CreateSessionRequest{
		Workstream: provider.WorkstreamRef{ProjectID: "p1", WorkstreamID: "ws1"},
		WorkingDir: "/tmp/project",
		OptionID:   "claude",
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	sess, err := p.SessionState(context.Background(), provider.SessionRef{
		ProjectID: "p1", WorkstreamID: "ws1", SessionID: "enclave-claude-new",
	})
	if err != nil {
		t.Fatalf("SessionState (owned): %v", err)
	}
	if sess.ID != "enclave-claude-new" || sess.State != domain.SessionRunning {
		t.Errorf("owned session = %+v; want the created running session", sess)
	}
}

func TestSessionStateNotFound(t *testing.T) {
	p := newForTest(t, &fakeCLI{available: true})
	p.SetStore(store.NewTestStore(t, []domain.Project{{ID: "p1", Name: "p", Path: "/tmp/project"}}))
	if _, err := p.SessionState(context.Background(), provider.SessionRef{
		ProjectID: "p1", WorkstreamID: "ws1", SessionID: "ghost",
	}); err == nil {
		t.Fatal("SessionState for an unknown session should error")
	}
}
