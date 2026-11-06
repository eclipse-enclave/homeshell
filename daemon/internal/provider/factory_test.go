package provider

import "testing"

func TestRegisterSessionFactoriesMaterializesAndDeclines(t *testing.T) {
	registered := sessionFactories
	t.Cleanup(func() { sessionFactories = registered })
	// A factory that builds a provider and one that declines (returns nil).
	RegisterSessionFactory(func() (SessionProvider, error) {
		return &stubSessionProvider{id: "factory-stub", label: "Factory Stub"}, nil
	})
	RegisterSessionFactory(func() (SessionProvider, error) { return nil, nil })

	reg := NewRegistry()
	if err := RegisterSessionFactories(reg); err != nil {
		t.Fatalf("RegisterSessionFactories: %v", err)
	}
	if _, ok := reg.SessionProvider("factory-stub"); !ok {
		t.Error("materialized provider not registered")
	}
	// The declining factory must not add a nil provider.
	for _, p := range reg.SessionProviders() {
		if p == nil {
			t.Error("registry holds a nil provider from a declining factory")
		}
	}
}
