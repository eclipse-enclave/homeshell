package provider

import (
	"context"
	"encoding/json"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
)

// stubSessionProvider implements only the mandatory SessionProvider contract
// (no optional capability interfaces), so it exercises the "capability absent"
// paths. It reports the sessions in `discovered` keyed by project ID.
type stubSessionProvider struct {
	id             string
	label          string
	available      bool
	availErr       error
	discovered     map[string][]DiscoveredSession
	discoverErr    error
	caps           Capabilities
	sessionSchemas []SessionSchemaOption
}

func (s *stubSessionProvider) ID() string    { return s.id }
func (s *stubSessionProvider) Label() string { return s.label }

func (s *stubSessionProvider) Available(context.Context) (bool, error) {
	return s.available, s.availErr
}

func (s *stubSessionProvider) Capabilities() Capabilities { return s.caps }

func (s *stubSessionProvider) SessionSchemas(_ context.Context, _ WorkstreamRef) ([]SessionSchemaOption, error) {
	return s.sessionSchemas, nil
}

func (s *stubSessionProvider) CreateSession(_ context.Context, _ CreateSessionRequest) (domain.Session, error) {
	return domain.Session{}, nil
}

func (s *stubSessionProvider) RemoveSession(_ context.Context, _ SessionRef) error { return nil }

func (s *stubSessionProvider) SessionState(context.Context, SessionRef) (domain.Session, error) {
	return domain.Session{}, nil
}

func (s *stubSessionProvider) Open(context.Context, SessionRef, string) (OpenResult, error) {
	return OpenResult{}, nil
}

func (s *stubSessionProvider) DiscoverSessions(_ context.Context, p ProjectRef) ([]DiscoveredSession, error) {
	if s.discoverErr != nil {
		return nil, s.discoverErr
	}
	return s.discovered[p.ID], nil
}

// fullSessionStub also implements the optional session capability interfaces.
type fullSessionStub struct{ stubSessionProvider }

func (fullSessionStub) Prompt(context.Context, SessionRef, string) error  { return nil }
func (fullSessionStub) SendKey(context.Context, SessionRef, string) error { return nil }
func (fullSessionStub) StartSession(context.Context, SessionRef) error    { return nil }
func (fullSessionStub) StopSession(context.Context, SessionRef) error     { return nil }
func (fullSessionStub) CheckRestart(SessionRef, string) error             { return nil }
func (fullSessionStub) PrepareRestart(context.Context, RestartSessionRequest) (RestartFunc, error) {
	return func(context.Context) error { return nil }, nil
}

// stubWorkstreamProvider implements the WorkstreamProvider contract for tests.
type stubWorkstreamProvider struct {
	id        string
	label     string
	available bool
	canCreate bool
	created   domain.Workstream
	createErr error
}

func (s *stubWorkstreamProvider) ID() string    { return s.id }
func (s *stubWorkstreamProvider) Label() string { return s.label }
func (s *stubWorkstreamProvider) Available(context.Context) (bool, error) {
	return s.available, nil
}
func (s *stubWorkstreamProvider) CanCreate(context.Context, ProjectRef) bool { return s.canCreate }
func (s *stubWorkstreamProvider) Schema(context.Context, ProjectRef) json.RawMessage {
	return json.RawMessage(`{"type":"object"}`)
}
func (s *stubWorkstreamProvider) Create(context.Context, CreateRequest) (domain.Workstream, error) {
	if s.createErr != nil {
		return domain.Workstream{}, s.createErr
	}
	return s.created, nil
}
func (s *stubWorkstreamProvider) Remove(context.Context, ProjectRef, domain.Workstream, RemoveOptions) error {
	return nil
}
