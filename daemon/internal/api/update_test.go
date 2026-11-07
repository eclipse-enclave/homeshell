package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/updater"
)

func TestUpdateEndpointUnavailableWithoutUpdater(t *testing.T) {
	handler, _ := testHandler(t, time.Minute)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/update", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestUpdateEndpointNoNewerVersion(t *testing.T) {
	svc := updater.NewService(stubUpdater{status: updater.Status{Channel: "stable", Current: "1.2.3", Latest: "1.2.3"}})
	handler := handlerWithUpdater(t, svc)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/update", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var body struct {
		Updated bool   `json:"updated"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if body.Updated {
		t.Errorf("body = %+v, want updated=false", body)
	}
}

func TestUpdateEndpointRejectsGET(t *testing.T) {
	handler, _ := testHandler(t, time.Minute)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/update", nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestUpdateEndpointRefusals(t *testing.T) {
	for _, tc := range []struct {
		name       string
		stub       stubUpdater
		wantStatus int
		wantError  string
	}{
		{
			name:       "build that does not update itself",
			stub:       stubUpdater{unsupported: fmt.Errorf("%w: source build", updater.ErrUnsupported)},
			wantStatus: http.StatusServiceUnavailable,
			wantError:  "self-update is not supported: source build",
		},
		{
			name: "executable not writable",
			stub: stubUpdater{
				status:     updater.Status{Channel: "stable", Current: "1.0.0", Latest: "2.0.0", UpdateAvailable: true},
				installErr: fmt.Errorf("cannot replace /usr/bin/homeshell: %w; reinstall with: sudo install", updater.ErrNotWritable),
			},
			wantStatus: http.StatusConflict,
			wantError:  "cannot replace /usr/bin/homeshell: not writable; reinstall with: sudo install",
		},
		{
			name:       "download fails",
			stub:       stubUpdater{checkErr: errors.New("download server unreachable")},
			wantStatus: http.StatusInternalServerError,
			wantError:  "update failed: download server unreachable",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := handlerWithUpdater(t, updater.NewService(tc.stub))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/update", nil))

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			var body struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("invalid JSON %q: %v", rec.Body.String(), err)
			}
			if body.Error != tc.wantError {
				t.Errorf("error = %q, want %q", body.Error, tc.wantError)
			}
		})
	}
}
