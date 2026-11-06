package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/eclipsesource/homeshell/daemon/internal/logging"
	"github.com/eclipsesource/homeshell/daemon/internal/notify"
	"github.com/eclipsesource/homeshell/daemon/internal/store"
)

func configTestHandler(t *testing.T, level *logging.Level) http.Handler {
	return handlerForTest(Deps{LogLevel: level, Store: store.NewTestStore(t, store.Seed()), Notifications: notify.NewHub()})
}

func TestConfigEndpointReportsLogLevel(t *testing.T) {
	handler := configTestHandler(t, logging.NewLevel(slog.LevelDebug))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/config", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var body struct {
		LogLevel string `json:"logLevel"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if body.LogLevel != "debug" {
		t.Errorf("logLevel = %q, want %q", body.LogLevel, "debug")
	}
}

func TestConfigEndpointChangesLogLevelAtRuntime(t *testing.T) {
	level := logging.NewLevel(slog.LevelInfo)
	handler := configTestHandler(t, level)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPatch, "/api/config", strings.NewReader(`{"logLevel":"debug"}`)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if level.Level() != slog.LevelDebug {
		t.Errorf("live level = %v, want debug", level.Level())
	}
	var body struct {
		LogLevel string `json:"logLevel"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if body.LogLevel != "debug" {
		t.Errorf("logLevel = %q, want debug", body.LogLevel)
	}
}

func TestConfigEndpointRejectsInvalidLevel(t *testing.T) {
	level := logging.NewLevel(slog.LevelInfo)
	handler := configTestHandler(t, level)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPatch, "/api/config", strings.NewReader(`{"logLevel":"nonsense"}`)))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if level.Level() != slog.LevelInfo {
		t.Errorf("live level changed to %v on a rejected request", level.Level())
	}
}

func TestConfigEndpointAllowsOnlyGetAndPatch(t *testing.T) {
	handler := configTestHandler(t, logging.NewLevel(slog.LevelInfo))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/config", nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}
