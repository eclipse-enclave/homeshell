package provider_test

import (
	"context"
	"errors"
	"testing"

	"github.com/eclipsesource/homeshell/daemon/internal/provider"
)

// TestSessionKeyBytesCoverSessionKeys holds the shared byte spelling to the
// normalized key set. Both session providers write these into a PTY, so a key
// the API accepts but this map lacks is an error at send time on every backend
// that takes bytes. It lives here rather than in either provider because the
// two previously kept identical private copies that nothing compared, so a
// correction to one would have left the other behind.
func TestSessionKeyBytesCoverSessionKeys(t *testing.T) {
	for key := range provider.SessionKeys {
		if provider.SessionKeyBytes[key] == "" {
			t.Errorf("SessionKeyBytes missing a sequence for %q", key)
		}
	}
	for key := range provider.SessionKeyBytes {
		if !provider.SessionKeys[key] {
			t.Errorf("SessionKeyBytes has %q, which is not a normalized session key", key)
		}
	}
}

func TestValidSessionKey(t *testing.T) {
	for _, key := range []string{"up", "down", "left", "right", "enter", "escape", "tab"} {
		if !provider.ValidSessionKey(key) {
			t.Errorf("ValidSessionKey(%q) = false, want true", key)
		}
	}
	for _, key := range []string{"", "Up", "return", "ctrl-c", "2", "esc"} {
		if provider.ValidSessionKey(key) {
			t.Errorf("ValidSessionKey(%q) = true, want false", key)
		}
	}
}

// TestServicePromptRoutesToProvider verifies Prompt resolves the session's
// owning provider (the fake, a Prompter) and delegates without error.
func TestServicePromptRoutesToProvider(t *testing.T) {
	svc, st := newService(t)
	wref := createWS(t, svc, st)
	svc.CreateSession(context.Background(), provider.CreateSessionInput{
		ProjectID: wref.ProjectID, WorkstreamID: wref.WorkstreamID, ProviderID: "fake", OptionID: "claude",
	})
	sess := awaitSession(t, st, "p1", wref.WorkstreamID, "Claude Code")
	sref := provider.SessionRef{ProjectID: wref.ProjectID, WorkstreamID: wref.WorkstreamID, SessionID: sess.ID}

	if err := svc.Prompt(context.Background(), sref, "run tests"); err != nil {
		t.Fatalf("Prompt = %v, want nil", err)
	}
}

// TestServiceSendKeyRoutesAndValidates verifies SendKey validates the key and
// delegates to the owning Prompter.
func TestServiceSendKeyRoutesAndValidates(t *testing.T) {
	svc, st := newService(t)
	wref := createWS(t, svc, st)
	svc.CreateSession(context.Background(), provider.CreateSessionInput{
		ProjectID: wref.ProjectID, WorkstreamID: wref.WorkstreamID, ProviderID: "fake", OptionID: "claude",
	})
	sess := awaitSession(t, st, "p1", wref.WorkstreamID, "Claude Code")
	sref := provider.SessionRef{ProjectID: wref.ProjectID, WorkstreamID: wref.WorkstreamID, SessionID: sess.ID}

	if err := svc.SendKey(context.Background(), sref, "down"); err != nil {
		t.Fatalf("SendKey = %v, want nil", err)
	}
	// An unknown key is rejected before the provider is consulted.
	if err := svc.SendKey(context.Background(), sref, "bogus"); !errors.Is(err, provider.ErrInvalidKey) {
		t.Fatalf("SendKey(bogus) = %v, want ErrInvalidKey", err)
	}
}

// TestServiceSendKeyRejectsStopped verifies a stopped session is a 409-mapped
// conflict, mirroring Prompt.
func TestServiceSendKeyRejectsStopped(t *testing.T) {
	svc, st := newService(t)
	wref := createWS(t, svc, st)
	svc.CreateSession(context.Background(), provider.CreateSessionInput{
		ProjectID: wref.ProjectID, WorkstreamID: wref.WorkstreamID, ProviderID: "fake", OptionID: "claude",
	})
	sess := awaitSession(t, st, "p1", wref.WorkstreamID, "Claude Code")
	sref := provider.SessionRef{ProjectID: wref.ProjectID, WorkstreamID: wref.WorkstreamID, SessionID: sess.ID}

	if err := svc.StopSession(context.Background(), sref); err != nil {
		t.Fatalf("StopSession: %v", err)
	}
	if err := svc.SendKey(context.Background(), sref, "enter"); !errors.Is(err, provider.ErrSessionStopped) {
		t.Fatalf("SendKey on stopped = %v, want ErrSessionStopped", err)
	}
}
