package cli

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// testSettingsJSON is a settings document in the daemon's shape: nested groups
// of scalars, including the two types `set` has to read a typed string as.
const testSettingsJSON = `{"projects":{"confirmRemove":true,"sidebarSort":"insertion"},` +
	`"sessions":{"previewLines":20},"providers":{"ideCommand":"code"}}`

func settingsRoutes(put stubResponse) map[string]stubResponse {
	return map[string]stubResponse{
		"GET /api/settings": {http.StatusOK, testSettingsJSON},
		"PUT /api/settings": put,
	}
}

func TestSettingsGet(t *testing.T) {
	t.Run("prints every setting as a dotted key", func(t *testing.T) {
		newStubDaemon(t, settingsRoutes(stubResponse{http.StatusOK, testSettingsJSON}))

		code, stdout, stderr := runClient(settingsGet())
		if code != exitOK {
			t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
		}
		for _, want := range []string{
			"projects.confirmRemove=true", "projects.sidebarSort=insertion",
			"sessions.previewLines=20", "providers.ideCommand=code"} {
			if !strings.Contains(stdout, want) {
				t.Errorf("stdout = %q, want it to contain %q", stdout, want)
			}
		}
	})

	t.Run("a number prints without a float tail", func(t *testing.T) {
		newStubDaemon(t, settingsRoutes(stubResponse{http.StatusOK, testSettingsJSON}))

		_, stdout, _ := runClient(settingsGet())
		if strings.Contains(stdout, "20.0") {
			t.Errorf("stdout = %q, want 20 rather than a float rendering", stdout)
		}
	})

	t.Run("one key prints the bare value, for $(...) in a script", func(t *testing.T) {
		newStubDaemon(t, settingsRoutes(stubResponse{http.StatusOK, testSettingsJSON}))

		code, stdout, stderr := runClient(settingsGet(), "sessions.previewLines")
		if code != exitOK {
			t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
		}
		if strings.TrimSpace(stdout) != "20" {
			t.Errorf("stdout = %q, want the bare value", stdout)
		}
	})

	t.Run("an unknown key is a usage error listing the known ones", func(t *testing.T) {
		newStubDaemon(t, settingsRoutes(stubResponse{http.StatusOK, testSettingsJSON}))

		code, _, stderr := runClient(settingsGet(), "projects.nope")
		if code != exitUsage {
			t.Errorf("exit code = %d, want %d", code, exitUsage)
		}
		if !strings.Contains(stderr, `no setting is named "projects.nope"`) {
			t.Errorf("stderr = %q, want it to name the key that missed", stderr)
		}
		if !strings.Contains(stderr, "sessions.previewLines") {
			t.Errorf("stderr = %q, want it to list the settings that do exist", stderr)
		}
	})

	t.Run("--json passes the document through untouched", func(t *testing.T) {
		newStubDaemon(t, settingsRoutes(stubResponse{http.StatusOK, testSettingsJSON}))

		code, stdout, stderr := runClient(settingsGet(), "--json")
		if code != exitOK {
			t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
		}
		if strings.TrimSpace(stdout) != testSettingsJSON {
			t.Errorf("stdout = %q, want the exact payload the daemon sent", stdout)
		}
	})
}

func TestSettingsSet(t *testing.T) {
	// The value written must land in the whole document PUT back, with every
	// other setting preserved — the endpoint replaces, so a dropped field would
	// silently reset it.
	t.Run("writes back the whole document with one leaf changed", func(t *testing.T) {
		stub := newStubDaemon(t, settingsRoutes(stubResponse{http.StatusOK, testSettingsJSON}))

		code, stdout, stderr := runClient(settingsSet(), "projects.sidebarSort", "alphabetical")
		if code != exitOK {
			t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
		}
		var sent map[string]map[string]any
		if err := json.Unmarshal([]byte(stub.call(t, http.MethodPut, "/api/settings").Body), &sent); err != nil {
			t.Fatal(err)
		}
		if sent["projects"]["sidebarSort"] != "alphabetical" {
			t.Errorf("wrote sidebarSort = %v, want alphabetical", sent["projects"]["sidebarSort"])
		}
		if sent["projects"]["confirmRemove"] != true {
			t.Errorf("confirmRemove = %v, want the untouched setting preserved", sent["projects"]["confirmRemove"])
		}
		if sent["sessions"]["previewLines"] != float64(20) {
			t.Errorf("previewLines = %v, want the untouched group preserved", sent["sessions"]["previewLines"])
		}
		if !strings.Contains(stdout, "projects.sidebarSort=alphabetical") {
			t.Errorf("stdout = %q, want it to echo the new value", stdout)
		}
	})

	// The document's existing type is what the typed string is read as, so the
	// CLI needs no copy of the schema.
	t.Run("reads the value as the type the setting already holds", func(t *testing.T) {
		tests := []struct {
			name, key, value string
			want             any
		}{
			{"boolean", "projects.confirmRemove", "false", false},
			{"number", "sessions.previewLines", "8", float64(8)},
			{"string", "providers.ideCommand", "vim", "vim"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				stub := newStubDaemon(t, settingsRoutes(stubResponse{http.StatusOK, testSettingsJSON}))

				if code, _, stderr := runClient(settingsSet(), tt.key, tt.value); code != exitOK {
					t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
				}
				var sent map[string]map[string]any
				if err := json.Unmarshal([]byte(stub.call(t, http.MethodPut, "/api/settings").Body), &sent); err != nil {
					t.Fatal(err)
				}
				group, leaf, _ := strings.Cut(tt.key, ".")
				if sent[group][leaf] != tt.want {
					t.Errorf("wrote %v (%T), want %v (%T)", sent[group][leaf], sent[group][leaf], tt.want, tt.want)
				}
			})
		}
	})

	t.Run("a value of the wrong type is caught here, before the daemon", func(t *testing.T) {
		stub := newStubDaemon(t, settingsRoutes(stubResponse{http.StatusOK, testSettingsJSON}))

		code, _, stderr := runClient(settingsSet(), "sessions.previewLines", "eight")
		if code != exitUsage {
			t.Errorf("exit code = %d, want %d", code, exitUsage)
		}
		if !strings.Contains(stderr, "is not a number") {
			t.Errorf("stderr = %q, want it to say what the value should be", stderr)
		}
		for _, recorded := range stub.Calls() {
			if recorded.Method == http.MethodPut {
				t.Errorf("a malformed value was still written to the daemon")
			}
		}
	})

	t.Run("a value the daemon rejects is reported in its own words", func(t *testing.T) {
		newStubDaemon(t, settingsRoutes(stubResponse{http.StatusBadRequest,
			`{"error":"sidebarSort must be \"insertion\" or \"alphabetical\""}`}))

		code, _, stderr := runClient(settingsSet(), "projects.sidebarSort", "sideways")
		if code != exitFailed {
			t.Errorf("exit code = %d, want %d", code, exitFailed)
		}
		if !strings.Contains(stderr, "sidebarSort must be") {
			t.Errorf("stderr = %q, want the daemon's own validation message", stderr)
		}
	})

	t.Run("an unknown key is refused without writing anything", func(t *testing.T) {
		stub := newStubDaemon(t, settingsRoutes(stubResponse{http.StatusOK, testSettingsJSON}))

		code, _, stderr := runClient(settingsSet(), "projects.nope", "x")
		if code != exitUsage {
			t.Errorf("exit code = %d, want %d", code, exitUsage)
		}
		if !strings.Contains(stderr, "no setting is named") {
			t.Errorf("stderr = %q, want it to reject the key", stderr)
		}
		for _, recorded := range stub.Calls() {
			if recorded.Method == http.MethodPut {
				t.Errorf("an unknown key was still written to the daemon")
			}
		}
	})

	t.Run("a missing value is a usage error naming it", func(t *testing.T) {
		newStubDaemon(t, settingsRoutes(stubResponse{http.StatusOK, testSettingsJSON}))

		code, _, stderr := runClient(settingsSet(), "projects.sidebarSort")
		if code != exitUsage {
			t.Errorf("exit code = %d, want %d", code, exitUsage)
		}
		if !strings.Contains(stderr, "a value is required") {
			t.Errorf("stderr = %q, want it to name the missing argument", stderr)
		}
	})
}
