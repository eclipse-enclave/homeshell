package provider_test

import (
	"context"
	"errors"
	"testing"

	"github.com/eclipsesource/homeshell/daemon/internal/provider"
	"github.com/eclipsesource/homeshell/daemon/internal/store"
)

// renamableSession creates a workstream with one session in it and returns a ref
// to that session.
func renamableSession(t *testing.T, svc *provider.Service, st *store.Store) provider.SessionRef {
	t.Helper()
	wref := createWS(t, svc, st)
	if _, err := svc.CreateSession(context.Background(), provider.CreateSessionInput{
		ProjectID: wref.ProjectID, WorkstreamID: wref.WorkstreamID, ProviderID: "fake", OptionID: "claude",
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	sess := awaitSession(t, st, "p1", wref.WorkstreamID, "Claude Code")
	return provider.SessionRef{
		ProjectID: wref.ProjectID, WorkstreamID: wref.WorkstreamID, SessionID: sess.ID,
	}
}

// sessionByID reads a session out of the store's projection.
func sessionByID(t *testing.T, st *store.Store, ref provider.SessionRef) (title string, found bool) {
	t.Helper()
	ws, err := st.Workstream(ref.ProjectID, ref.WorkstreamID)
	if err != nil {
		t.Fatalf("Workstream: %v", err)
	}
	for _, sess := range ws.Sessions {
		if sess.ID == ref.SessionID {
			return sess.Title, true
		}
	}
	return "", false
}

func TestServiceRenameSession(t *testing.T) {
	svc, st := newService(t)
	ref := renamableSession(t, svc, st)

	if err := svc.RenameSession(context.Background(), ref, "Port the CLI"); err != nil {
		t.Fatalf("RenameSession: %v", err)
	}
	title, found := sessionByID(t, st, ref)
	if !found {
		t.Fatal("the session is gone from the store")
	}
	if title != "Port the CLI" {
		t.Errorf("title = %q; want %q", title, "Port the CLI")
	}
}

// The daemon does not hand a session a name with padding on it.
func TestServiceRenameSessionTrimsTheTitle(t *testing.T) {
	svc, st := newService(t)
	ref := renamableSession(t, svc, st)

	if err := svc.RenameSession(context.Background(), ref, "  spaced  "); err != nil {
		t.Fatalf("RenameSession: %v", err)
	}
	if title, _ := sessionByID(t, st, ref); title != "spaced" {
		t.Errorf("title = %q; want it trimmed to %q", title, "spaced")
	}
}

func TestServiceRenameSessionRejectsBlankTitles(t *testing.T) {
	svc, st := newService(t)
	ref := renamableSession(t, svc, st)

	for _, title := range []string{"", "   ", "\t\n"} {
		if err := svc.RenameSession(context.Background(), ref, title); !errors.Is(err, provider.ErrEmptySessionTitle) {
			t.Errorf("RenameSession(%q) err = %v; want ErrEmptySessionTitle", title, err)
		}
	}
}

func TestServiceRenameSessionUnknownTargets(t *testing.T) {
	svc, st := newService(t)
	ref := renamableSession(t, svc, st)

	tests := []struct {
		name string
		ref  provider.SessionRef
		want error
	}{
		{
			"unknown workstream",
			provider.SessionRef{ProjectID: "p1", WorkstreamID: "ghost", SessionID: ref.SessionID},
			provider.ErrWorkstreamNotFound,
		},
		{
			"unknown project",
			provider.SessionRef{ProjectID: "ghost", WorkstreamID: ref.WorkstreamID, SessionID: ref.SessionID},
			provider.ErrWorkstreamNotFound,
		},
		{
			"unknown session",
			provider.SessionRef{ProjectID: "p1", WorkstreamID: ref.WorkstreamID, SessionID: "ghost"},
			provider.ErrSessionNotFound,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := svc.RenameSession(context.Background(), test.ref, "x"); !errors.Is(err, test.want) {
				t.Errorf("RenameSession err = %v; want %v", err, test.want)
			}
			if err := svc.ResetSessionTitle(context.Background(), test.ref); !errors.Is(err, test.want) {
				t.Errorf("ResetSessionTitle err = %v; want %v", err, test.want)
			}
		})
	}
}

// The fake provider re-titles its running sessions as they work ("Demo run #1 ·
// editing files"), so a reconcile after a rename is the real test that the name
// the user chose wins over the one the provider keeps reporting.
func TestServiceRenameSessionSurvivesReconcile(t *testing.T) {
	svc, st := newService(t)
	ref := renamableSession(t, svc, st)

	if err := svc.RenameSession(context.Background(), ref, "mine"); err != nil {
		t.Fatalf("RenameSession: %v", err)
	}
	for range 3 {
		svc.Reconcile(context.Background())
	}
	if title, _ := sessionByID(t, st, ref); title != "mine" {
		t.Errorf("title after reconciling = %q; want the rename to hold", title)
	}
}

func TestServiceResetSessionTitle(t *testing.T) {
	svc, st := newService(t)
	ref := renamableSession(t, svc, st)

	if err := svc.RenameSession(context.Background(), ref, "mine"); err != nil {
		t.Fatalf("RenameSession: %v", err)
	}
	// The reset re-projects itself, so the provider's own title is back by the time
	// it returns — a client that cleared a name does not wait for the next poll.
	if err := svc.ResetSessionTitle(context.Background(), ref); err != nil {
		t.Fatalf("ResetSessionTitle: %v", err)
	}
	title, _ := sessionByID(t, st, ref)
	if title == "mine" {
		t.Error("title is still the custom one after a reset + reconcile")
	}
	if title == "" {
		t.Error("title is empty after a reset; want the provider's own")
	}
}

// A placeholder's ID is discarded the moment the real session takes its place,
// and titles are keyed on the ID — so a rename accepted here would be pruned on
// the next cycle and vanish with no error anywhere. Refused in the daemon rather
// than only in the UI, because the API and the CLI reach this the same way.
func TestRenameSessionRefusesAStartingPlaceholder(t *testing.T) {
	svc, st := newService(t)
	wref := createWS(t, svc, st)
	ref := provider.SessionRef{
		ProjectID:    wref.ProjectID,
		WorkstreamID: wref.WorkstreamID,
		SessionID:    provider.StartingIDPrefix + "abc",
	}

	err := svc.RenameSession(context.Background(), ref, "My task")
	if !errors.Is(err, provider.ErrSessionStarting) {
		t.Errorf("renaming a placeholder err = %v; want ErrSessionStarting", err)
	}
}
