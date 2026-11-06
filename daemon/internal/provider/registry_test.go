package provider

import (
	"context"
	"errors"
	"testing"
)

func TestRegisterAndGetSessionProvider(t *testing.T) {
	reg := NewRegistry()
	p := &stubSessionProvider{id: "a", label: "A", available: true}
	if err := reg.RegisterSessionProvider(p); err != nil {
		t.Fatalf("RegisterSessionProvider: %v", err)
	}
	got, ok := reg.SessionProvider("a")
	if !ok || got != p {
		t.Fatalf("SessionProvider(a) = %v, %v; want the registered provider", got, ok)
	}
	if _, ok := reg.SessionProvider("missing"); ok {
		t.Errorf("SessionProvider(missing) = ok; want not found")
	}
	if len(reg.SessionProviders()) != 1 {
		t.Errorf("SessionProviders() len = %d, want 1", len(reg.SessionProviders()))
	}
}

func TestRegisterAndGetWorkstreamProvider(t *testing.T) {
	reg := NewRegistry()
	p := &stubWorkstreamProvider{id: "w", label: "W", available: true, canCreate: true}
	if err := reg.RegisterWorkstreamProvider(p); err != nil {
		t.Fatalf("RegisterWorkstreamProvider: %v", err)
	}
	got, ok := reg.WorkstreamProvider("w")
	if !ok || got != p {
		t.Fatalf("WorkstreamProvider(w) = %v, %v; want the registered provider", got, ok)
	}
	if len(reg.WorkstreamProviders()) != 1 {
		t.Errorf("WorkstreamProviders() len = %d, want 1", len(reg.WorkstreamProviders()))
	}
}

func TestRegisterDuplicateRejected(t *testing.T) {
	reg := NewRegistry()
	if err := reg.RegisterSessionProvider(&stubSessionProvider{id: "dup"}); err != nil {
		t.Fatalf("first RegisterSessionProvider: %v", err)
	}
	if err := reg.RegisterSessionProvider(&stubSessionProvider{id: "dup"}); err == nil {
		t.Errorf("second RegisterSessionProvider(dup) = nil; want duplicate error")
	}
	if err := reg.RegisterSessionProvider(&stubSessionProvider{id: ""}); err == nil {
		t.Errorf("RegisterSessionProvider(empty id) = nil; want error")
	}
	if err := reg.RegisterWorkstreamProvider(&stubWorkstreamProvider{id: "wdup"}); err != nil {
		t.Fatalf("first RegisterWorkstreamProvider: %v", err)
	}
	if err := reg.RegisterWorkstreamProvider(&stubWorkstreamProvider{id: "wdup"}); err == nil {
		t.Errorf("second RegisterWorkstreamProvider(wdup) = nil; want duplicate error")
	}
}

func TestDescribeSessionDerivesCapabilities(t *testing.T) {
	reg := NewRegistry()

	minimal := &stubSessionProvider{id: "min", label: "Minimal", available: true,
		caps: Capabilities{OpenModes: []string{"terminal"}}}
	full := &fullSessionStub{stubSessionProvider{id: "full", label: "Full", available: true}}

	ctx := context.Background()
	dm := reg.DescribeSession(ctx, minimal)
	if dm.Capabilities.OneOffPrompt || dm.Capabilities.SessionStartStop || dm.Capabilities.SessionRestart {
		t.Errorf("minimal derived caps = %+v; want prompt/startStop/restart false", dm.Capabilities)
	}
	if len(dm.Capabilities.OpenModes) != 1 || dm.Capabilities.OpenModes[0] != "terminal" {
		t.Errorf("minimal open modes = %v, want [terminal]", dm.Capabilities.OpenModes)
	}

	df := reg.DescribeSession(ctx, full)
	if !df.Capabilities.OneOffPrompt || !df.Capabilities.SessionStartStop || !df.Capabilities.SessionRestart {
		t.Errorf("full derived caps = %+v; want prompt, sessionStartStop and sessionRestart true", df.Capabilities)
	}
}

func TestDescribeSessionUnavailableSurfacesError(t *testing.T) {
	p := &stubSessionProvider{id: "down", label: "Down", available: false, availErr: errors.New("not installed")}
	reg := NewRegistry()
	d := reg.DescribeSession(context.Background(), p)
	if d.Status.State != StatusUnavailable {
		t.Errorf("status state = %q, want unavailable", d.Status.State)
	}
	if d.Status.Reason != "not installed" {
		t.Errorf("status reason = %q, want %q", d.Status.Reason, "not installed")
	}
}
