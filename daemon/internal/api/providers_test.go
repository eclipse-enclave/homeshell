package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/notify"
	"github.com/eclipsesource/homeshell/daemon/internal/provider"
	"github.com/eclipsesource/homeshell/daemon/internal/store"
)

func TestGetProviders(t *testing.T) {
	h, _ := newProviderHandler(t)
	rec := do(t, h, http.MethodGet, "/api/providers", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var descs []provider.ProviderDescriptor
	if err := json.Unmarshal(rec.Body.Bytes(), &descs); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(descs) != 1 {
		t.Fatalf("providers = %d, want 1", len(descs))
	}
	d := descs[0]
	if d.ID != "fake" || d.Status.State != provider.StatusAvailable {
		t.Errorf("descriptor = %+v; want id=fake status=available", d)
	}
	// The fake session provider implements session start/stop and one-off
	// prompting (it echoes the prompt/key into the session preview for the demo).
	if !d.Capabilities.SessionStartStop {
		t.Errorf("capabilities = %+v; want sessionStartStop true", d.Capabilities)
	}
	if !d.Capabilities.OneOffPrompt {
		t.Errorf("oneOffPrompt = false; fake implements Prompter")
	}
}

func TestGetProvidersEmptyWhenUnwired(t *testing.T) {
	st := store.NewTestStore(t, []domain.Project{})
	h := handlerForTest(Deps{Store: st, Heartbeat: time.Minute, Notifications: notify.NewHub()})
	rec := do(t, h, http.MethodGet, "/api/providers", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var descs []provider.ProviderDescriptor
	if err := json.Unmarshal(rec.Body.Bytes(), &descs); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(descs) != 0 {
		t.Errorf("providers = %d, want 0 when unwired", len(descs))
	}
}

func TestWorkstreamProvidersForProject(t *testing.T) {
	h, _ := newProviderHandler(t)
	rec := do(t, h, http.MethodGet, "/api/projects/proj-test/workstream-providers", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var providers []provider.ProviderDescriptor
	if err := json.Unmarshal(rec.Body.Bytes(), &providers); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(providers) != 2 {
		t.Fatalf("providers = %+v; want 2 entries (core + fake)", providers)
	}
	byID := map[string]provider.ProviderDescriptor{}
	for _, p := range providers {
		byID[p.ID] = p
	}
	if core, ok := byID[provider.CoreProviderID]; !ok || core.Label != "Default" || len(core.WorkstreamSchema) == 0 {
		t.Errorf("core default provider missing or malformed: %+v", core)
	}
	if fk, ok := byID["fake"]; !ok || len(fk.WorkstreamSchema) == 0 {
		t.Errorf("fake workstream provider missing or malformed: %+v", fk)
	}
}

func TestWorkstreamProvidersUnknownProject(t *testing.T) {
	h, _ := newProviderHandler(t)
	rec := do(t, h, http.MethodGet, "/api/projects/nope/workstream-providers", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}
