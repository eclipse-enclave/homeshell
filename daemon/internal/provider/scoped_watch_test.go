package provider

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/store"
)

type scopedWatchProvider struct {
	stubSessionProvider
	availableCalls atomic.Int64
	discoveryCalls atomic.Int64
	watchedCalls   atomic.Int64
	sinks          chan Sink
	onDiscover     func()
}

func (p *scopedWatchProvider) Available(ctx context.Context) (bool, error) {
	p.availableCalls.Add(1)
	return p.stubSessionProvider.Available(ctx)
}

func (p *scopedWatchProvider) DiscoverSessions(ctx context.Context, project ProjectRef) ([]DiscoveredSession, error) {
	p.discoveryCalls.Add(1)
	if p.onDiscover != nil {
		p.onDiscover()
	}
	return p.stubSessionProvider.DiscoverSessions(ctx, project)
}

func (p *scopedWatchProvider) DiscoverWatchedSessions(ctx context.Context, project ProjectRef) ([]DiscoveredSession, error) {
	p.watchedCalls.Add(1)
	if p.onDiscover != nil {
		p.onDiscover()
	}
	return p.stubSessionProvider.DiscoverSessions(ctx, project)
}

func (p *scopedWatchProvider) Watch(ctx context.Context, sink Sink) {
	p.sinks <- sink
	<-ctx.Done()
}

func TestRunBindsWatcherToItsProvider(t *testing.T) {
	st := store.NewTestStore(t, []domain.Project{{ID: "p", Path: "/p"}})
	reg := NewRegistry()
	providers := []*scopedWatchProvider{}
	for _, id := range []string{"local", "other"} {
		p := &scopedWatchProvider{stubSessionProvider: stubSessionProvider{id: id, available: true, discovered: map[string][]DiscoveredSession{"p": {ds("/p/w", id, domain.SessionRunning)}}}, sinks: make(chan Sink, 1)}
		reg.RegisterSessionProvider(p)
		providers = append(providers, p)
	}
	r := NewReconciler(st, reg, 0)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); r.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	sink := <-providers[0].sinks
	<-providers[1].sinks
	waitUntil(t, func() bool { return len(st.Projects()[0].Workstreams) == 1 }, time.Second)
	sink.Changed()
	waitUntil(t, func() bool { return providers[0].watchedCalls.Load() == 1 }, time.Second)
	if providers[0].discoveryCalls.Load() != 1 || providers[1].discoveryCalls.Load() != 1 || providers[1].availableCalls.Load() != 1 || providers[1].watchedCalls.Load() != 0 {
		t.Fatal("watcher re-asked an unrelated provider, or did not reuse its observations")
	}
	r.Changed()
	waitUntil(t, func() bool { return providers[0].discoveryCalls.Load() == 2 && providers[1].discoveryCalls.Load() == 2 }, time.Second)
}

func TestScopedChangesCoalesceWithoutLosingInFlightChanges(t *testing.T) {
	r := NewReconciler(store.NewTestStore(t, nil), NewRegistry(), 0)
	a := watcherSink{r, "a"}
	b := watcherSink{r, "b"}
	a.Changed()
	a.Changed()
	b.Changed()
	<-r.trigger
	first := r.takeDirty()
	if first.all || len(first.ids) != 2 || !first.ids["a"] || !first.ids["b"] {
		t.Fatal(first)
	}
	// A new notification while the previous set is being reconciled must stay
	// pending, even if multiple changes had shared one wakeup earlier.
	b.Changed()
	<-r.trigger
	second := r.takeDirty()
	if second.all || len(second.ids) != 1 || !second.ids["b"] {
		t.Fatal(second)
	}
	a.Changed()
	r.Changed()
	b.Changed()
	<-r.trigger
	if !r.takeDirty().all {
		t.Fatal("scoped refresh masked a full refresh")
	}
	// A notification can enqueue another wakeup after Run receives the first
	// token but before it drains the dirty set. That leftover token is a no-op,
	// not an implicit request to query every provider.
	if only := r.takeDirty(); only.all || len(only.ids) != 0 {
		t.Fatalf("empty wakeup requested a full refresh: %v", only)
	}
}

func TestScopedReconcileRetainsOtherProvidersAndFailures(t *testing.T) {
	st := store.NewTestStore(t, []domain.Project{{ID: "p", Path: "/p"}})
	reg := NewRegistry()
	a := &scopedWatchProvider{stubSessionProvider: stubSessionProvider{id: "a", available: true, discovered: map[string][]DiscoveredSession{"p": {ds("/p/w", "a", domain.SessionRunning)}}}}
	b := &scopedWatchProvider{stubSessionProvider: stubSessionProvider{id: "b", available: true, discovered: map[string][]DiscoveredSession{"p": {ds("/p/w", "b", domain.SessionWaiting)}}}}
	reg.RegisterSessionProvider(a)
	reg.RegisterSessionProvider(b)
	r := NewReconciler(st, reg, 0)
	r.Reconcile(context.Background())
	a.discoverErr = errors.New("temporary failure")
	r.reconcileOnly(context.Background(), dirtySet{ids: map[string]bool{"a": true}})
	if got := st.Projects()[0].Workstreams[0].Sessions; len(got) != 2 {
		t.Fatalf("transient failure dropped sessions: %+v", got)
	}
	a.discoverErr = nil
	a.discovered["p"] = nil
	r.reconcileOnly(context.Background(), dirtySet{ids: map[string]bool{"a": true}})
	got := st.Projects()[0].Workstreams[0].Sessions
	if len(got) != 1 || got[0].ID != "b" || got[0].State != domain.SessionWaiting {
		t.Fatalf("unrelated provider state lost: %+v", got)
	}
	if b.discoveryCalls.Load() != 1 || b.availableCalls.Load() != 1 {
		t.Fatal("unrelated provider was queried")
	}
}
