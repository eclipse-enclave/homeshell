package cli

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestVersionVerb(t *testing.T) {
	tests := []struct {
		name       string
		route      map[string]stubResponse
		wantStdout []string
	}{
		{
			name: "reports the client and the daemon when they agree",
			route: map[string]stubResponse{
				"GET /api/version": {http.StatusOK, `{"current":"1.2.3","latest":"1.2.3","updateAvailable":false}`},
			},
			wantStdout: []string{"client  " + testVersion, "daemon  1.2.3", "latest  1.2.3 (up to date)"},
		},
		{
			name: "points at the update verb when a newer release exists",
			route: map[string]stubResponse{
				"GET /api/version": {http.StatusOK, `{"current":"1.2.3","latest":"1.3.0","updateAvailable":true}`},
			},
			wantStdout: []string{"daemon  1.2.3", "latest  1.3.0", `"homeshell update"`},
		},
		{
			name: "a failed release check leaves the latest version unknown",
			route: map[string]stubResponse{
				"GET /api/version": {http.StatusOK,
					`{"current":"1.2.3","latest":"1.2.3","updateAvailable":false,"error":"release check failed"}`},
			},
			// Printing "latest 1.2.3" here would claim it is up to date on
			// evidence the daemon explicitly said it does not have.
			wantStdout: []string{"latest  unknown", "release check failed"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			newStubDaemon(t, tt.route)

			code, stdout, stderr := runVerb(runVersion, nil)
			if code != exitOK {
				t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
			}
			for _, want := range tt.wantStdout {
				if !strings.Contains(stdout, want) {
					t.Errorf("stdout = %q, want it to contain %q", stdout, want)
				}
			}
		})
	}
}

// The client version is a fact about this binary, so `version` must still answer
// with no daemon around — that is when it is most often asked.
func TestVersionWithoutADaemon(t *testing.T) {
	isolateStateFile(t)

	code, stdout, _ := runVerb(runVersion, nil)
	if code != exitOK {
		t.Errorf("exit code = %d, want 0 even with no daemon", code)
	}
	if !strings.Contains(stdout, "client  "+testVersion) {
		t.Errorf("stdout = %q, want the client version", stdout)
	}
	if !strings.Contains(stdout, "daemon  not running") {
		t.Errorf("stdout = %q, want it to report the daemon as not running", stdout)
	}
}

func TestVersionJSON(t *testing.T) {
	t.Run("nests the daemon's payload beside the client version", func(t *testing.T) {
		newStubDaemon(t, map[string]stubResponse{
			"GET /api/version": {http.StatusOK,
				`{"current":"1.2.3","latest":"1.3.0","updateAvailable":true}`},
		})

		code, stdout, stderr := runVerb(runVersion, []string{"--json"})
		if code != exitOK {
			t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
		}
		var reported struct {
			Client string `json:"client"`
			Daemon *struct {
				Current         string `json:"current"`
				UpdateAvailable bool   `json:"updateAvailable"`
			} `json:"daemon"`
		}
		if err := json.Unmarshal([]byte(stdout), &reported); err != nil {
			t.Fatalf("stdout is not JSON: %v (%q)", err, stdout)
		}
		if reported.Client != testVersion {
			t.Errorf("client = %q, want %q", reported.Client, testVersion)
		}
		if reported.Daemon == nil || reported.Daemon.Current != "1.2.3" || !reported.Daemon.UpdateAvailable {
			t.Errorf("daemon = %+v, want the daemon's payload passed through", reported.Daemon)
		}
	})

	t.Run("a missing daemon is a null, not a missing key", func(t *testing.T) {
		isolateStateFile(t)

		code, stdout, _ := runVerb(runVersion, []string{"--json"})
		if code != exitOK {
			t.Fatalf("exit code = %d, want 0", code)
		}
		var reported map[string]any
		if err := json.Unmarshal([]byte(stdout), &reported); err != nil {
			t.Fatalf("stdout is not JSON: %v (%q)", err, stdout)
		}
		daemon, present := reported["daemon"]
		if !present {
			t.Fatalf("payload = %v, want a daemon key so a script can test it", reported)
		}
		if daemon != nil {
			t.Errorf("daemon = %v, want null", daemon)
		}
	})
}

// A daemon that answers badly is a real failure, unlike one that is absent.
func TestVersionReportsARefusal(t *testing.T) {
	newStubDaemon(t, map[string]stubResponse{
		"GET /api/version": {http.StatusInternalServerError, `{"error":"broken"}`},
	})

	code, _, stderr := runVerb(runVersion, nil)
	if code != exitFailed {
		t.Errorf("exit code = %d, want %d", code, exitFailed)
	}
	if !strings.Contains(stderr, "broken") {
		t.Errorf("stderr = %q, want the daemon's message", stderr)
	}
}
