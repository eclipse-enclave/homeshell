package cli

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/eclipsesource/homeshell/daemon/internal/daemonstate"
	"github.com/eclipsesource/homeshell/daemon/internal/persist"
)

// isolateStateFile points daemonstate at a throwaway directory for the
// duration of a test so nothing touches the real per-user state directory.
func isolateStateFile(t *testing.T) {
	t.Helper()
	old := persist.HomeDir()
	persist.SetHomeDir(t.TempDir())
	t.Cleanup(func() { persist.SetHomeDir(old) })
}

// liveDaemon starts a stub that answers GET /api/hello and records a matching
// state file, so discovery classifies it as running.
func liveDaemon(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/hello" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"message":"hi","version":"1.2.3"}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	addr := strings.TrimPrefix(srv.URL, "http://")
	if err := daemonstate.Write(daemonstate.State{Address: addr, PID: os.Getpid(), Version: "1.2.3"}); err != nil {
		t.Fatal(err)
	}
	return srv
}

func runVerb(run func(Env, []string) int, args []string) (code int, stdout, stderr string) {
	var out, err bytes.Buffer
	code = run(Env{Version: testVersion, Stdout: &out, Stderr: &err}, args)
	return code, out.String(), err.String()
}

func TestDiscover(t *testing.T) {
	t.Run("none when no state file", func(t *testing.T) {
		isolateStateFile(t)
		st, phase, err := discover(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if phase != daemonNone {
			t.Errorf("phase = %v, want daemonNone", phase)
		}
		if st.PID != 0 {
			t.Errorf("expected a zero State, got %+v", st)
		}
	})

	t.Run("running when the state file points at a live daemon", func(t *testing.T) {
		isolateStateFile(t)
		liveDaemon(t)
		_, phase, err := discover(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if phase != daemonRunning {
			t.Errorf("phase = %v, want daemonRunning", phase)
		}
	})

	t.Run("stale when the recorded daemon does not answer", func(t *testing.T) {
		isolateStateFile(t)
		// 127.0.0.1:1 refuses connections; the PID is a dead one.
		if err := daemonstate.Write(daemonstate.State{Address: "127.0.0.1:1", PID: 999999999}); err != nil {
			t.Fatal(err)
		}
		st, phase, err := discover(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if phase != daemonStale {
			t.Errorf("phase = %v, want daemonStale", phase)
		}
		if st.Address != "127.0.0.1:1" {
			t.Errorf("state not returned for a stale daemon: %+v", st)
		}
	})
}
