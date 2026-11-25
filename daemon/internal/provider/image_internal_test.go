package provider

import (
	"context"
	"errors"
	"testing"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/store"
)

// attachStub is an ImageAttacher whose attach fails with err.
type attachStub struct {
	stubSessionProvider
	err error
}

func (s *attachStub) AttachImage(context.Context, SessionRef, Image) (string, error) {
	return "", s.err
}

// serviceWithSession reconciles one session, reported by sessionProvider, into
// a workstream of project p1, and returns the service and that session's ref.
func serviceWithSession(t *testing.T, sessionProvider SessionProvider) (*Service, SessionRef) {
	t.Helper()
	projectStore := store.NewTestStore(t, []domain.Project{{ID: "p1", Name: "p1", Path: "/p1", Workstreams: []domain.Workstream{
		{ID: "ws1", Name: "ws", Provider: "core", WorkingDir: "/p1/work", Sessions: []domain.Session{}},
	}}})
	service := NewService(projectStore, registryWith(t, sessionProvider), 0)
	service.Reconcile(context.Background())
	return service, SessionRef{ProjectID: "p1", WorkstreamID: "ws1", SessionID: "s1"}
}

func attachableSession() map[string][]DiscoveredSession {
	return map[string][]DiscoveredSession{"p1": {{WorkingDir: "/p1/work", Session: domain.Session{
		ID: "s1", State: domain.SessionIdle, Capabilities: &domain.SessionCapabilities{Live: true, AttachImage: true},
	}}}}
}

func TestServiceAttachImageFailures(t *testing.T) {
	providerErr := errors.New("error: the image inbox is not enabled")
	tests := []struct {
		name            string
		sessionProvider SessionProvider
		wantErr         error
	}{
		{
			name:            "the provider's failure comes back as it is",
			sessionProvider: &attachStub{stubSessionProvider: stubSessionProvider{id: "prov", available: true, discovered: attachableSession()}, err: providerErr},
			wantErr:         providerErr,
		},
		{
			// A session claiming the capability on a provider without the
			// interface is still refused, not a panic.
			name:            "a provider that is no ImageAttacher",
			sessionProvider: &stubSessionProvider{id: "prov", available: true, discovered: attachableSession()},
			wantErr:         ErrCapabilityUnsupported,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service, ref := serviceWithSession(t, tt.sessionProvider)
			_, err := service.AttachImage(context.Background(), ref, Image{MediaType: "image/png", Data: []byte("\x89PNG\r\n\x1a\n")})
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("AttachImage = %v, want %v", err, tt.wantErr)
			}
		})
	}
}
