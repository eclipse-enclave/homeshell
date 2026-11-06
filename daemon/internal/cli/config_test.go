package cli

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestConfigGet(t *testing.T) {
	t.Run("prints the live runtime config as key=value", func(t *testing.T) {
		newStubDaemon(t, map[string]stubResponse{
			"GET /api/config": {http.StatusOK, `{"logLevel":"info"}`},
		})

		code, stdout, stderr := runClient(configGet())
		if code != exitOK {
			t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
		}
		if !strings.Contains(stdout, "logLevel=info") {
			t.Errorf("stdout = %q, want it to report the live log level", stdout)
		}
	})

	t.Run("one key prints the bare value", func(t *testing.T) {
		newStubDaemon(t, map[string]stubResponse{
			"GET /api/config": {http.StatusOK, `{"logLevel":"debug"}`},
		})

		code, stdout, stderr := runClient(configGet(), "logLevel")
		if code != exitOK {
			t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
		}
		if strings.TrimSpace(stdout) != "debug" {
			t.Errorf("stdout = %q, want the bare value", stdout)
		}
	})

	t.Run("an unknown key is a usage error listing the known ones", func(t *testing.T) {
		newStubDaemon(t, map[string]stubResponse{
			"GET /api/config": {http.StatusOK, `{"logLevel":"info"}`},
		})

		code, _, stderr := runClient(configGet(), "colour")
		if code != exitUsage {
			t.Errorf("exit code = %d, want %d", code, exitUsage)
		}
		if !strings.Contains(stderr, "logLevel") {
			t.Errorf("stderr = %q, want it to list the keys that do exist", stderr)
		}
	})
}

func TestConfigSet(t *testing.T) {
	t.Run("patches only the key being changed", func(t *testing.T) {
		stub := newStubDaemon(t, map[string]stubResponse{
			"PATCH /api/config": {http.StatusOK, `{"logLevel":"debug"}`},
		})

		code, stdout, stderr := runClient(configSet(), "logLevel", "debug")
		if code != exitOK {
			t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
		}
		var sent map[string]string
		if err := json.Unmarshal([]byte(stub.call(t, http.MethodPatch, "/api/config").Body), &sent); err != nil {
			t.Fatal(err)
		}
		if len(sent) != 1 || sent["logLevel"] != "debug" {
			t.Errorf("patched %v, want only logLevel=debug", sent)
		}
		if !strings.Contains(stdout, "logLevel=debug") {
			t.Errorf("stdout = %q, want it to report the applied value", stdout)
		}
	})

	// The daemon answers with the config it now holds, so a value it normalized
	// is reported as applied rather than as typed.
	t.Run("reports what the daemon applied, not what was asked for", func(t *testing.T) {
		newStubDaemon(t, map[string]stubResponse{
			"PATCH /api/config": {http.StatusOK, `{"logLevel":"warn"}`},
		})

		_, stdout, _ := runClient(configSet(), "logLevel", "WARN")
		if !strings.Contains(stdout, "logLevel=warn") {
			t.Errorf("stdout = %q, want the daemon's normalized value", stdout)
		}
	})

	t.Run("an invalid level is the daemon's 400 to give", func(t *testing.T) {
		newStubDaemon(t, map[string]stubResponse{
			"PATCH /api/config": {http.StatusBadRequest,
				`{"error":"invalid log level \"shouty\" (want verbose, debug, info, warn, or error)"}`},
		})

		code, _, stderr := runClient(configSet(), "logLevel", "shouty")
		if code != exitFailed {
			t.Errorf("exit code = %d, want %d", code, exitFailed)
		}
		if !strings.Contains(stderr, "invalid log level") {
			t.Errorf("stderr = %q, want the daemon's own message", stderr)
		}
	})

	t.Run("a missing value is a usage error naming it", func(t *testing.T) {
		newStubDaemon(t, map[string]stubResponse{
			"PATCH /api/config": {http.StatusOK, `{"logLevel":"info"}`},
		})

		code, _, stderr := runClient(configSet(), "logLevel")
		if code != exitUsage {
			t.Errorf("exit code = %d, want %d", code, exitUsage)
		}
		if !strings.Contains(stderr, "a value is required") {
			t.Errorf("stderr = %q, want it to name the missing argument", stderr)
		}
	})
}
