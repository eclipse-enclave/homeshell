package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/notify"
	"github.com/eclipsesource/homeshell/daemon/internal/store"
	"github.com/eclipsesource/homeshell/daemon/internal/updater"
)

// stubUpdater is a fake updater.Updater for exercising the version/update HTTP
// handlers without a real release source.
type stubUpdater struct {
	latest    updater.Release
	latestErr error
}

func (s stubUpdater) Latest(context.Context) (updater.Release, error) {
	return s.latest, s.latestErr
}

func (s stubUpdater) Apply(context.Context, updater.Release, string) error { return nil }

// handlerWithUpdater builds the daemon HTTP surface backed by seeded state and
// the given self-update service.
func handlerWithUpdater(t *testing.T, svc *updater.Service) http.Handler {
	t.Helper()
	return handlerForTest(Deps{
		Version:       "1.2.3",
		Store:         store.NewTestStore(t, store.Seed()),
		Heartbeat:     time.Minute,
		Updater:       svc,
		Notifications: notify.NewHub(),
	})
}

func TestVersionEndpointWithoutUpdater(t *testing.T) {
	handler, _ := testHandler(t, time.Minute)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/version", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var body struct {
		Current         string `json:"current"`
		Latest          string `json:"latest"`
		UpdateAvailable bool   `json:"updateAvailable"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if body.Current != "1.2.3" || body.Latest != "1.2.3" || body.UpdateAvailable {
		t.Errorf("body = %+v, want current=latest=1.2.3, no update", body)
	}
}

func TestVersionEndpointReportsAvailableUpdate(t *testing.T) {
	svc := updater.NewService(stubUpdater{latest: updater.Release{Version: "2.0.0"}}, "1.2.3")
	handler := handlerWithUpdater(t, svc)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/version", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var body struct {
		Current         string `json:"current"`
		Latest          string `json:"latest"`
		UpdateAvailable bool   `json:"updateAvailable"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if !body.UpdateAvailable || body.Latest != "2.0.0" {
		t.Errorf("body = %+v, want update to 2.0.0", body)
	}
}
