package cli

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/eclipsesource/homeshell/daemon/internal/daemonstate"
)

// updateDaemon starts a stub that answers both GET /api/hello (for discovery)
// and POST /api/update (for the update verb), records a matching state file,
// and returns the server.
func updateDaemon(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/hello" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"message":"hi","version":"1.2.3"}`))
			return
		}
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	addr := strings.TrimPrefix(srv.URL, "http://")
	if err := daemonstate.Write(daemonstate.State{Address: addr, PID: os.Getpid(), Version: "1.2.3", Token: "update-token"}); err != nil {
		t.Fatal(err)
	}
	return srv
}

func TestUpdateSendsBearerToken(t *testing.T) {
	isolateStateFile(t)
	var gotAuth string
	updateDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/update" {
			gotAuth = r.Header.Get("Authorization")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"updated":false,"version":"1.2.3"}`))
	})

	code, _, _ := runVerb(runUpdate, nil)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if gotAuth != "Bearer update-token" {
		t.Errorf("Authorization on POST /api/update = %q, want %q", gotAuth, "Bearer update-token")
	}
}

func TestUpdateReportsRestartOnSuccess(t *testing.T) {
	isolateStateFile(t)
	updateDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/update" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"updated":true,"version":"2.0.0"}`))
	})

	code, stdout, _ := runVerb(runUpdate, nil)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(stdout, "2.0.0") || !strings.Contains(stdout, "restarting") {
		t.Errorf("stdout = %q, want it to mention the new version and restart", stdout)
	}
}

func TestUpdateReportsUpToDate(t *testing.T) {
	isolateStateFile(t)
	updateDaemon(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"updated":false,"version":"1.0.0"}`))
	})

	code, stdout, _ := runVerb(runUpdate, nil)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(stdout, "up to date") {
		t.Errorf("stdout = %q, want it to say up to date", stdout)
	}
}

func TestUpdateReportsDaemonError(t *testing.T) {
	isolateStateFile(t)
	updateDaemon(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"checksum mismatch for homeshell-linux-amd64.tar.gz"}`))
	})

	code, _, stderr := runVerb(runUpdate, nil)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	want := "homeshell: update failed: checksum mismatch for homeshell-linux-amd64.tar.gz\n"
	if stderr != want {
		t.Errorf("stderr = %q, want %q", stderr, want)
	}
}

func TestUpdateReportsUnavailable(t *testing.T) {
	isolateStateFile(t)
	updateDaemon(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"error":"self-update is not available"}`))
	})

	code, _, stderr := runVerb(runUpdate, nil)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr, "self-update is not available") {
		t.Errorf("stderr = %q, want it to surface the daemon's reason", stderr)
	}
}

func TestUpdateReportsNoDaemon(t *testing.T) {
	isolateStateFile(t)
	// No state file → discovery fails → update reports no daemon.
	code, _, stderr := runVerb(runUpdate, nil)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr, "cannot discover") {
		t.Errorf("stderr = %q, want a discovery error", stderr)
	}
}
