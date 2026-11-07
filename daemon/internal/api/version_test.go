package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/notify"
	"github.com/eclipsesource/homeshell/daemon/internal/store"
	"github.com/eclipsesource/homeshell/daemon/internal/updater"
)

// stubUpdater is a fake updater.Updater for exercising the version/update HTTP
// handlers without a real release source.
type stubUpdater struct {
	status      updater.Status
	checkErr    error
	unsupported error
	installErr  error
}

func (s stubUpdater) Supported() error { return s.unsupported }

func (s stubUpdater) Check(context.Context) (updater.Status, error) {
	return s.status, s.checkErr
}

func (s stubUpdater) Install(context.Context, updater.Status, string) error { return s.installErr }

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

func TestVersionEndpointReportsTheUpdatersStatus(t *testing.T) {
	for _, tc := range []struct {
		name string
		stub stubUpdater
		want string
	}{
		{
			name: "update available",
			stub: stubUpdater{status: updater.Status{Channel: "stable", Current: "1.2.3", Latest: "2.0.0", UpdateAvailable: true}},
			want: `{"channel":"stable","current":"1.2.3","latest":"2.0.0","updateAvailable":true}`,
		},
		{
			name: "rolling build up to date",
			stub: stubUpdater{status: updater.Status{Channel: "rolling", Current: "abc1234", Latest: "abc1234"}},
			want: `{"channel":"rolling","current":"abc1234","latest":"abc1234","updateAvailable":false}`,
		},
		{
			name: "build that does not update itself",
			stub: stubUpdater{status: updater.Status{Current: "1.2.3", Latest: "1.2.3"}},
			want: `{"channel":"","current":"1.2.3","latest":"1.2.3","updateAvailable":false}`,
		},
		{
			name: "release check fails",
			stub: stubUpdater{checkErr: errors.New("download server unreachable")},
			want: `{"channel":"","current":"1.2.3","latest":"1.2.3","updateAvailable":false,"error":"release check failed"}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := handlerWithUpdater(t, updater.NewService(tc.stub))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/version", nil))

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
			}
			if got := strings.TrimSpace(rec.Body.String()); got != tc.want {
				t.Errorf("body = %s, want %s", got, tc.want)
			}
		})
	}
}
