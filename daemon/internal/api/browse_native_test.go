package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/filedialog"
	"github.com/eclipsesource/homeshell/daemon/internal/notify"
	"github.com/eclipsesource/homeshell/daemon/internal/store"
)

// fakeChooser stands in for the host's folder dialog: it records the start
// directory it was asked to open at and returns a canned outcome.
type fakeChooser struct {
	path     string
	err      error
	startDir string
	called   int
}

func (f *fakeChooser) ChooseDirectory(_ context.Context, startDir string) (string, error) {
	f.called++
	f.startDir = startDir
	return f.path, f.err
}

func nativeHandler(t *testing.T, chooser filedialog.Chooser, publicBind bool) http.Handler {
	t.Helper()
	return handlerForTest(Deps{
		Version:       "1.2.3",
		Store:         store.NewTestStore(t, store.Seed()),
		Heartbeat:     time.Minute,
		Notifications: notify.NewHub(),
		FileDialog:    chooser,
		PublicBind:    publicBind,
	})
}

func postNative(t *testing.T, handler http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/browse/native", strings.NewReader(body)))
	return recorder
}

func TestBrowseNativeReturnsTheChosenDirectory(t *testing.T) {
	chooser := &fakeChooser{path: "/home/user/projects/thing"}
	recorder := postNative(t, nativeHandler(t, chooser, false), `{}`)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var response browseNativeResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if response.Path != "/home/user/projects/thing" {
		t.Errorf("path = %q, want %q", response.Path, "/home/user/projects/thing")
	}
	if response.Cancelled {
		t.Error("cancelled = true, want false")
	}
}

// Dismissing the dialog is a normal outcome, so it must not look like a failure
// the client should report as an error.
func TestBrowseNativeReportsACancelAsSuccess(t *testing.T) {
	chooser := &fakeChooser{err: filedialog.ErrCancelled}
	recorder := postNative(t, nativeHandler(t, chooser, false), `{}`)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	var response browseNativeResponse
	json.Unmarshal(recorder.Body.Bytes(), &response)
	if !response.Cancelled {
		t.Error("cancelled = false, want true")
	}
	if response.Path != "" {
		t.Errorf("path = %q, want empty", response.Path)
	}
}

// 501 is the signal the client falls back to the in-app picker on, so a host
// without a dialog tool must produce exactly that.
func TestBrowseNativeReportsAnUnavailableChooser(t *testing.T) {
	tests := []struct {
		name    string
		chooser filedialog.Chooser
	}{
		{"host has no dialog tool", &fakeChooser{err: filedialog.ErrUnavailable}},
		{"daemon wired without one", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := postNative(t, nativeHandler(t, tt.chooser, false), `{}`)
			if recorder.Code != http.StatusNotImplemented {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNotImplemented)
			}
		})
	}
}

// On a public bind the dialog would open on a screen the caller cannot see, so
// the endpoint refuses rather than hanging on a window nobody will answer.
func TestBrowseNativeRefusesOnAPublicBind(t *testing.T) {
	chooser := &fakeChooser{path: "/tmp"}
	recorder := postNative(t, nativeHandler(t, chooser, true), `{}`)

	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusConflict)
	}
	if chooser.called != 0 {
		t.Errorf("chooser was called %d times, want 0 — no dialog may open", chooser.called)
	}
}

func TestBrowseNativeReportsARealFailure(t *testing.T) {
	chooser := &fakeChooser{err: errors.New("display connection refused")}
	recorder := postNative(t, nativeHandler(t, chooser, false), `{}`)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}
}

// The start directory is a hint. A usable one is honored; anything else is
// dropped so the dialog still opens rather than refusing the request.
func TestBrowseNativeStartDirectory(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a-file")
	os.WriteFile(file, []byte("x"), 0o600)

	tests := []struct {
		name string
		body string
		want string
	}{
		{"an existing directory is honored", `{"path":` + jsonString(dir) + `}`, dir},
		{"no path asks for the platform default", `{}`, ""},
		{"a missing path is dropped", `{"path":"/no/such/directory/here"}`, ""},
		{"a file is dropped", `{"path":` + jsonString(file) + `}`, ""},
		{"an empty body is dropped", ``, ""},
		{"a malformed body is dropped", `not json`, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chooser := &fakeChooser{path: "/chosen"}
			recorder := postNative(t, nativeHandler(t, chooser, false), tt.body)

			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d; body: %s", recorder.Code, http.StatusOK, recorder.Body.String())
			}
			if chooser.startDir != tt.want {
				t.Errorf("start directory = %q, want %q", chooser.startDir, tt.want)
			}
		})
	}
}

func jsonString(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
