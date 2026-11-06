package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/eclipsesource/homeshell/daemon/internal/agentmodel"
	"github.com/eclipsesource/homeshell/daemon/internal/agentspec"
	"github.com/eclipsesource/homeshell/daemon/internal/settings"
	"github.com/eclipsesource/homeshell/daemon/internal/store"
)

// Registry holds the providers the daemon knows about, kept in two lists that
// mirror the workstream/session divide. It is safe for concurrent use.
type Registry struct {
	mu sync.RWMutex

	workstreamProviders []WorkstreamProvider
	workstreamByID      map[string]WorkstreamProvider

	sessionProviders []SessionProvider
	sessionByID      map[string]SessionProvider
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		workstreamByID: map[string]WorkstreamProvider{},
		sessionByID:    map[string]SessionProvider{},
	}
}

// RegisterWorkstreamProvider adds a workstream provider, rejecting a duplicate
// ID. A provider that also implements SessionProvider is *not* added to the
// session list by this call; register it separately if it plays both roles.
func (r *Registry) RegisterWorkstreamProvider(p WorkstreamProvider) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	id := p.ID()
	if id == "" {
		return fmt.Errorf("workstream provider has an empty ID")
	}
	if _, ok := r.workstreamByID[id]; ok {
		return fmt.Errorf("a workstream provider with ID %q is already registered", id)
	}
	r.workstreamByID[id] = p
	r.workstreamProviders = append(r.workstreamProviders, p)
	return nil
}

// RegisterSessionProvider adds a session provider, rejecting a duplicate ID.
func (r *Registry) RegisterSessionProvider(p SessionProvider) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	id := p.ID()
	if id == "" {
		return fmt.Errorf("session provider has an empty ID")
	}
	if _, ok := r.sessionByID[id]; ok {
		return fmt.Errorf("a session provider with ID %q is already registered", id)
	}
	r.sessionByID[id] = p
	r.sessionProviders = append(r.sessionProviders, p)
	return nil
}

// WorkstreamProviders returns the registered workstream providers in
// registration order.
func (r *Registry) WorkstreamProviders() []WorkstreamProvider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]WorkstreamProvider, len(r.workstreamProviders))
	copy(out, r.workstreamProviders)
	return out
}

// SessionProviders returns the registered session providers in registration
// order.
func (r *Registry) SessionProviders() []SessionProvider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]SessionProvider, len(r.sessionProviders))
	copy(out, r.sessionProviders)
	return out
}

// WorkstreamProvider returns the workstream provider with the given ID.
func (r *Registry) WorkstreamProvider(id string) (WorkstreamProvider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.workstreamByID[id]
	return p, ok
}

// SessionProvider returns the session provider with the given ID.
func (r *Registry) SessionProvider(id string) (SessionProvider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.sessionByID[id]
	return p, ok
}

// Provider availability states. The set is open to further values (e.g. a
// future "busy") without changing the wire shape.
const (
	StatusAvailable   = "available"
	StatusUnavailable = "unavailable"
)

// ProviderStatus is a provider's availability: a state plus an optional reason
// (given when the provider is unavailable).
type ProviderStatus struct {
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
}

// ProviderDescriptor is the client-facing description of a provider served by
// GET /api/providers: identity, status, capabilities, and (for workstream
// providers) the JSON Schema for the workstream type it creates.
type ProviderDescriptor struct {
	ID               string          `json:"id"`
	Label            string          `json:"label"`
	Status           ProviderStatus  `json:"status"`
	Capabilities     DescribedCaps   `json:"capabilities"`
	WorkstreamSchema json.RawMessage `json:"workstreamSchema,omitempty"`
}

// DescribedCaps is the data-only Capabilities plus behavioural flags derived by
// type-asserting the optional interfaces.
type DescribedCaps struct {
	Capabilities
	StartStop        bool `json:"startStop"`
	OneOffPrompt     bool `json:"oneOffPrompt"`
	SessionStartStop bool `json:"sessionStartStop"`
	SessionRestart   bool `json:"sessionRestart"`
}

// DescribeSession builds the descriptor for one session provider.
func (r *Registry) DescribeSession(ctx context.Context, p SessionProvider) ProviderDescriptor {
	available, err := p.Available(ctx)
	status := ProviderStatus{State: StatusAvailable}
	switch {
	case err != nil:
		status = ProviderStatus{State: StatusUnavailable, Reason: err.Error()}
	case !available:
		status = ProviderStatus{State: StatusUnavailable}
	}
	d := ProviderDescriptor{
		ID:     p.ID(),
		Label:  p.Label(),
		Status: status,
		Capabilities: DescribedCaps{
			Capabilities: p.Capabilities(),
		},
	}
	if _, ok := p.(Prompter); ok {
		d.Capabilities.OneOffPrompt = true
	}
	if _, ok := p.(SessionRestarter); ok {
		d.Capabilities.SessionRestart = true
	}
	if _, ok := p.(SessionStartStopper); ok {
		d.Capabilities.SessionStartStop = true
	}
	return d
}

// DescribeAll returns descriptors for every registered session provider.
func (r *Registry) DescribeAll(ctx context.Context) []ProviderDescriptor {
	providers := r.SessionProviders()
	out := make([]ProviderDescriptor, 0, len(providers))
	for _, p := range providers {
		out = append(out, r.DescribeSession(ctx, p))
	}
	return out
}

// StoreAware is an optional interface a provider may implement when it needs a
// reference to the store (e.g. to resolve a workstream's working directory).
// Providers built via init() can't receive the store at construction time;
// InjectStore sets it after the store is created.
type StoreAware interface {
	SetStore(s *store.Store)
}

// InjectStore walks the registered providers and calls SetStore on any that
// implement StoreAware.
func (r *Registry) InjectStore(s *store.Store) {
	for _, p := range r.WorkstreamProviders() {
		if sa, ok := p.(StoreAware); ok {
			sa.SetStore(s)
		}
	}
	for _, p := range r.SessionProviders() {
		if sa, ok := p.(StoreAware); ok {
			sa.SetStore(s)
		}
	}
}

// ModelCatalogAware is an optional interface a session provider may implement
// when it offers a choice of model: the models on offer are the user's authored
// rows (internal/agentcatalog), not a list in the provider. Providers built
// via init() cannot receive the catalog at construction time (there is no catalog
// yet), so InjectModelCatalog sets it once there is one.
//
// The port is shared rather than per provider on purpose: `claude` must offer the
// same models whether the Local provider runs it on the host or Enclave runs it in a
// container, and one catalog behind one interface is what keeps the two from
// drifting.
type ModelCatalogAware interface {
	SetModelCatalog(c agentmodel.Catalog)
}

// InjectModelCatalog walks the registered providers and calls SetModelCatalog on
// any that implement ModelCatalogAware.
func (r *Registry) InjectModelCatalog(c agentmodel.Catalog) {
	for _, p := range r.WorkstreamProviders() {
		if ca, ok := p.(ModelCatalogAware); ok {
			ca.SetModelCatalog(c)
		}
	}
	for _, p := range r.SessionProviders() {
		if ca, ok := p.(ModelCatalogAware); ok {
			ca.SetModelCatalog(c)
		}
	}
}

// AgentSpecAware is an optional interface a provider may implement when it
// launches coding agents whose descriptions the user authors — which agents exist,
// what settings a new session offers, and how each value reaches the command line
// (internal/agentspec, internal/agentcatalog). Providers built via init() can't receive the
// source at construction time; InjectAgentSpecs sets it afterwards.
type AgentSpecAware interface {
	SetAgentSpecs(s agentspec.Source)
}

// InjectAgentSpecs walks the registered providers and calls SetAgentSpecs on any
// that implement AgentSpecAware, so the Local provider and Enclave read the same
// descriptions and cannot offer the same agent different settings.
func (r *Registry) InjectAgentSpecs(s agentspec.Source) {
	for _, p := range r.WorkstreamProviders() {
		if sa, ok := p.(AgentSpecAware); ok {
			sa.SetAgentSpecs(s)
		}
	}
	for _, p := range r.SessionProviders() {
		if sa, ok := p.(AgentSpecAware); ok {
			sa.SetAgentSpecs(s)
		}
	}
}

// SettingsAware is an optional interface a provider may implement when its
// behavior depends on user settings (e.g. the core provider honoring the
// configured workstreams.location). Providers built via init() can't receive
// the settings store at construction time; InjectSettings sets it afterwards.
type SettingsAware interface {
	SetSettings(s *settings.Store)
}

// InjectSettings walks the registered providers and calls SetSettings on any
// that implement SettingsAware.
func (r *Registry) InjectSettings(s *settings.Store) {
	for _, p := range r.WorkstreamProviders() {
		if sa, ok := p.(SettingsAware); ok {
			sa.SetSettings(s)
		}
	}
	for _, p := range r.SessionProviders() {
		if sa, ok := p.(SettingsAware); ok {
			sa.SetSettings(s)
		}
	}
}
