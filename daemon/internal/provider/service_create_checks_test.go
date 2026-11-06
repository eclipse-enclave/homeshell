package provider

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/store"
)

// serviceWith wires a service over one empty project and the workstream
// provider under test.
func serviceWith(t *testing.T, workstreamProvider WorkstreamProvider) *Service {
	t.Helper()
	projectStore := store.NewTestStore(t, []domain.Project{{ID: "p1", Name: "p1", Path: t.TempDir(), Workstreams: []domain.Workstream{}}})
	registry := NewRegistry()
	if err := registry.RegisterWorkstreamProvider(workstreamProvider); err != nil {
		t.Fatalf("registering the workstream provider: %v", err)
	}
	return NewService(projectStore, registry, 0)
}

// validationSchemaStub offers a form that accepts anything and validates
// against a schema that admits only the in-place strategy.
type validationSchemaStub struct {
	stubWorkstreamProvider
	formAsked bool
}

func (s *validationSchemaStub) Schema(context.Context, ProjectRef) json.RawMessage {
	s.formAsked = true
	return json.RawMessage(`{"type":"object"}`)
}

func (s *validationSchemaStub) ValidationSchema(context.Context, ProjectRef) json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"strategy":{"enum":["in-place"]}}}`)
}

// A create is validated against the provider's ValidationSchema, so it never
// pays for the form's suggestions.
func TestCreateValidatesAgainstTheValidationSchema(t *testing.T) {
	stub := &validationSchemaStub{stubWorkstreamProvider: stubWorkstreamProvider{
		id: "stub", label: "Stub", available: true, canCreate: true,
	}}
	service := serviceWith(t, stub)

	_, err := service.Create(context.Background(), CreateInput{
		ProjectID: "p1", ProviderID: "stub", Name: "ws", Config: []byte(`{"strategy":"copy"}`),
	})
	var validationErr *ValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("Create err = %v; want the validation schema's refusal", err)
	}
	if stub.formAsked {
		t.Error("Create asked for the form's Schema; want only the validation schema")
	}
}

// hangingChecksStub names and validates a create only once its context is
// done, the way a provider waiting on a git that never answers does.
type hangingChecksStub struct {
	stubWorkstreamProvider
	cancelledOnEntry bool
}

func (s *hangingChecksStub) ResolveDraft(ctx context.Context, _ CreateRequest) Draft {
	s.cancelledOnEntry = ctx.Err() != nil
	s.wait(ctx)
	return Draft{Name: "named"}
}

func (s *hangingChecksStub) ValidateCreate(ctx context.Context, _ CreateRequest) error {
	s.wait(ctx)
	return nil
}

func (s *hangingChecksStub) wait(ctx context.Context) {
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
	}
}

// The checks a create makes under createMu share one bound, so a provider that
// hangs holds every other create up for no longer than that; and they outlive
// the request, which the create itself does too.
func TestCreateChecksShareOneBoundAndOutliveTheRequest(t *testing.T) {
	defer func(previous time.Duration) { createCheckTimeout = previous }(createCheckTimeout)
	createCheckTimeout = 100 * time.Millisecond
	stub := &hangingChecksStub{stubWorkstreamProvider: stubWorkstreamProvider{
		id: "stub", label: "Stub", available: true, canCreate: true,
		createErr: errors.New("not materialized"),
	}}
	service := serviceWith(t, stub)
	request, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	if _, err := service.Create(request, CreateInput{ProjectID: "p1", ProviderID: "stub"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Create took %v; want the checks bounded by %v together", elapsed, createCheckTimeout)
	}
	if stub.cancelledOnEntry {
		t.Error("the checks began cancelled; want them to outlive the request")
	}
}

// materializingStub creates its workstream once released, and records the
// directories it is asked to discard.
type materializingStub struct {
	stubWorkstreamProvider
	entered, release chan struct{}

	mu      sync.Mutex
	removed []string
}

func (s *materializingStub) Create(context.Context, CreateRequest) (domain.Workstream, error) {
	close(s.entered)
	<-s.release
	return s.created, nil
}

func (s *materializingStub) Remove(_ context.Context, _ ProjectRef, workstream domain.Workstream, _ RemoveOptions) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removed = append(s.removed, workstream.WorkingDir)
	return nil
}

// A workstream whose project is removed while it is being created has nowhere to
// land, so the directory the provider materialized for it is discarded.
func TestCreateDiscardsAWorkstreamWhoseProjectWasRemoved(t *testing.T) {
	stub := &materializingStub{
		stubWorkstreamProvider: stubWorkstreamProvider{
			id: "stub", label: "Stub", available: true, canCreate: true,
			created: domain.Workstream{Name: "ws", WorkingDir: "/materialized"},
		},
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	service := serviceWith(t, stub)
	if _, err := service.Create(context.Background(), CreateInput{ProjectID: "p1", ProviderID: "stub", Name: "ws"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	<-stub.entered

	if err := service.RemoveProject(context.Background(), "p1"); err != nil {
		t.Fatalf("RemoveProject: %v", err)
	}
	close(stub.release)

	waitFor(t, "the materialized directory to be discarded", func() bool {
		stub.mu.Lock()
		defer stub.mu.Unlock()
		return slices.Equal(stub.removed, []string{"/materialized"})
	})
}
