package cli

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestWorkstreamList(t *testing.T) {
	t.Run("lists every project's workstreams with the owning project named", func(t *testing.T) {
		newStubDaemon(t, snapshotRoute())

		code, stdout, stderr := runClient(workstreamList())
		if code != exitOK {
			t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
		}
		for _, want := range []string{"ws-1", "ws-2", "ws-3", "alpha", "beta", "worktree", "in place", "core"} {
			if !strings.Contains(stdout, want) {
				t.Errorf("stdout = %q, want it to contain %q", stdout, want)
			}
		}
	})

	t.Run("-project narrows the list", func(t *testing.T) {
		newStubDaemon(t, snapshotRoute())

		code, stdout, stderr := runClient(workstreamList(), "-project", "alpha")
		if code != exitOK {
			t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
		}
		if !strings.Contains(stdout, "ws-1") {
			t.Errorf("stdout = %q, want alpha's workstream", stdout)
		}
		for _, unwanted := range []string{"ws-2", "ws-3"} {
			if strings.Contains(stdout, unwanted) {
				t.Errorf("stdout = %q, want it to leave out %q", stdout, unwanted)
			}
		}
	})

	t.Run("--json emits the daemon's workstream objects", func(t *testing.T) {
		newStubDaemon(t, snapshotRoute())

		code, stdout, stderr := runClient(workstreamList(), "-project", "alpha", "--json")
		if code != exitOK {
			t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
		}
		var listed []map[string]any
		if err := json.Unmarshal([]byte(stdout), &listed); err != nil {
			t.Fatalf("stdout is not a JSON array: %v (%q)", err, stdout)
		}
		if len(listed) != 1 || listed[0]["id"] != "ws-1" {
			t.Fatalf("listed = %v, want just ws-1", listed)
		}
		// The nested objects the CLI has no columns for must survive too.
		if _, ok := listed[0]["openModes"]; !ok {
			t.Errorf("workstream object = %v, want the daemon's openModes preserved", listed[0])
		}
	})

	t.Run("an empty list is a header and nothing else", func(t *testing.T) {
		newStubDaemon(t, map[string]stubResponse{
			"GET /api/projects": {http.StatusOK, `{"projects":[]}`},
		})

		code, stdout, stderr := runClient(workstreamList())
		if code != exitOK {
			t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
		}
		if lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n"); len(lines) != 1 {
			t.Errorf("stdout = %q, want only the header row", stdout)
		}
	})
}

func TestWorkstreamCreate(t *testing.T) {
	t.Run("posts the provider, name and config to the project", func(t *testing.T) {
		stub := newStubDaemon(t, map[string]stubResponse{
			"GET /api/projects": {http.StatusOK, testSnapshotJSON},
			"POST /api/projects/proj-1/workstreams": {http.StatusCreated,
				`{"id":"ws-new","name":"feature/x","provider":"core","strategyId":"worktree"}`},
		})

		code, stdout, stderr := runClient(workstreamCreate(),
			"-project", "alpha", "-provider", "core", "-name", "feature/x", "-config", `{"strategy":"worktree"}`)
		if code != exitOK {
			t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
		}
		var sent struct {
			ProviderID string          `json:"providerId"`
			Name       string          `json:"name"`
			Config     json.RawMessage `json:"config"`
		}
		if err := json.Unmarshal([]byte(stub.call(t, http.MethodPost, "/api/projects/proj-1/workstreams").Body), &sent); err != nil {
			t.Fatal(err)
		}
		if sent.ProviderID != "core" || sent.Name != "feature/x" {
			t.Errorf("posted %+v, want provider core and name feature/x", sent)
		}
		if string(sent.Config) != `{"strategy":"worktree"}` {
			t.Errorf("posted config = %s, want it forwarded verbatim", sent.Config)
		}
		// Creation is asynchronous, so the message must not claim it is ready.
		if !strings.Contains(stdout, "creating workstream feature/x") {
			t.Errorf("stdout = %q, want it to report the creation as in progress", stdout)
		}
	})

	t.Run("no config flag sends no config key at all", func(t *testing.T) {
		stub := newStubDaemon(t, map[string]stubResponse{
			"GET /api/projects":                     {http.StatusOK, testSnapshotJSON},
			"POST /api/projects/proj-1/workstreams": {http.StatusCreated, `{"id":"ws-new","name":"x"}`},
		})

		if code, _, stderr := runClient(workstreamCreate(), "-project", "alpha", "-provider", "core"); code != exitOK {
			t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
		}
		body := stub.call(t, http.MethodPost, "/api/projects/proj-1/workstreams").Body
		if strings.Contains(body, "config") {
			t.Errorf("posted %s, want no config key so the provider's own defaults apply", body)
		}
	})

	t.Run("the sole provider a project offers needs no -provider", func(t *testing.T) {
		stub := newStubDaemon(t, map[string]stubResponse{
			"GET /api/projects": {http.StatusOK, testSnapshotJSON},
			"GET /api/projects/proj-1/workstream-providers": {http.StatusOK,
				`[{"id":"core","label":"Default","status":{"state":"available"}}]`},
			"POST /api/projects/proj-1/workstreams": {http.StatusCreated, `{"id":"ws-new","name":"x"}`},
		})

		if code, _, stderr := runClient(workstreamCreate(), "-project", "alpha"); code != exitOK {
			t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
		}
		if !strings.Contains(stub.call(t, http.MethodPost, "/api/projects/proj-1/workstreams").Body, `"providerId":"core"`) {
			t.Error("want the single offered provider to be used")
		}
	})

	t.Run("several providers and no choice is a usage error listing them", func(t *testing.T) {
		newStubDaemon(t, map[string]stubResponse{
			"GET /api/projects": {http.StatusOK, testSnapshotJSON},
			"GET /api/projects/proj-1/workstream-providers": {http.StatusOK,
				`[{"id":"core","label":"Default","status":{"state":"available"}},` +
					`{"id":"enclave","label":"Eclipse Enclave","status":{"state":"available"}}]`},
		})

		code, _, stderr := runClient(workstreamCreate(), "-project", "alpha")
		if code != exitUsage {
			t.Errorf("exit code = %d, want %d", code, exitUsage)
		}
		for _, want := range []string{"-provider is required", "core", "enclave", "Eclipse Enclave"} {
			if !strings.Contains(stderr, want) {
				t.Errorf("stderr = %q, want it to contain %q", stderr, want)
			}
		}
	})

	t.Run("a missing -project is a usage error", func(t *testing.T) {
		newStubDaemon(t, snapshotRoute())

		code, _, stderr := runClient(workstreamCreate(), "-provider", "core")
		if code != exitUsage {
			t.Errorf("exit code = %d, want %d", code, exitUsage)
		}
		if !strings.Contains(stderr, "-project is required") {
			t.Errorf("stderr = %q, want it to name the missing flag", stderr)
		}
	})

	t.Run("a malformed -config is caught before the request", func(t *testing.T) {
		stub := newStubDaemon(t, snapshotRoute())

		code, _, stderr := runClient(workstreamCreate(), "-project", "alpha", "-provider", "core", "-config", "strategy=worktree")
		if code != exitUsage {
			t.Errorf("exit code = %d, want %d", code, exitUsage)
		}
		if !strings.Contains(stderr, "-config is not valid JSON") {
			t.Errorf("stderr = %q, want it to reject the config", stderr)
		}
		if len(stub.Calls()) != 0 {
			t.Errorf("the verb reached the daemon anyway: %+v", stub.Calls())
		}
	})

	t.Run("the daemon's per-field validation errors are shown", func(t *testing.T) {
		newStubDaemon(t, map[string]stubResponse{
			"GET /api/projects": {http.StatusOK, testSnapshotJSON},
			"POST /api/projects/proj-1/workstreams": {http.StatusBadRequest,
				`{"error":"configuration is invalid","fields":{"branch":"is required"}}`},
		})

		code, _, stderr := runClient(workstreamCreate(), "-project", "alpha", "-provider", "core")
		if code != exitFailed {
			t.Errorf("exit code = %d, want %d", code, exitFailed)
		}
		for _, want := range []string{"configuration is invalid", "branch: is required"} {
			if !strings.Contains(stderr, want) {
				t.Errorf("stderr = %q, want it to contain %q", stderr, want)
			}
		}
	})
}

func TestWorkstreamRemove(t *testing.T) {
	t.Run("deletes the resolved workstream and defaults both options off", func(t *testing.T) {
		stub := newStubDaemon(t, map[string]stubResponse{
			"GET /api/projects":                            {http.StatusOK, testSnapshotJSON},
			"DELETE /api/projects/proj-2/workstreams/ws-3": {http.StatusNoContent, ""},
		})

		code, stdout, stderr := runClient(workstreamRemove(), "extra")
		if code != exitOK {
			t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
		}
		var sent map[string]bool
		body := stub.call(t, http.MethodDelete, "/api/projects/proj-2/workstreams/ws-3").Body
		if err := json.Unmarshal([]byte(body), &sent); err != nil {
			t.Fatal(err)
		}
		// force must never be sent unasked: it is what the daemon's guard protects.
		if sent["force"] || sent["keepFiles"] {
			t.Errorf("sent %v, want both false by default", sent)
		}
		if !strings.Contains(stdout, "removed workstream extra") {
			t.Errorf("stdout = %q, want it to confirm the removal", stdout)
		}
	})

	t.Run("--force and --keep-files are forwarded", func(t *testing.T) {
		stub := newStubDaemon(t, map[string]stubResponse{
			"GET /api/projects":                            {http.StatusOK, testSnapshotJSON},
			"DELETE /api/projects/proj-2/workstreams/ws-3": {http.StatusNoContent, ""},
		})

		if code, _, stderr := runClient(workstreamRemove(), "extra", "--force", "--keep-files"); code != exitOK {
			t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
		}
		var sent map[string]bool
		body := stub.call(t, http.MethodDelete, "/api/projects/proj-2/workstreams/ws-3").Body
		if err := json.Unmarshal([]byte(body), &sent); err != nil {
			t.Fatal(err)
		}
		if !sent["force"] || !sent["keepFiles"] {
			t.Errorf("sent %v, want both true", sent)
		}
	})

	t.Run("the running-sessions guard is reported with the way out", func(t *testing.T) {
		newStubDaemon(t, map[string]stubResponse{
			"GET /api/projects": {http.StatusOK, testSnapshotJSON},
			"DELETE /api/projects/proj-1/workstreams/ws-1": {http.StatusConflict,
				`{"error":"cannot remove the workstream while a session is still running",` +
					`"reason":"sessionsRunning","sessions":[{"id":"sess-1","title":"Implement it","state":"running"}]}`},
		})

		code, _, stderr := runClient(workstreamRemove(), "ws-1")
		if code != exitFailed {
			t.Errorf("exit code = %d, want %d", code, exitFailed)
		}
		for _, want := range []string{"still running", "Implement it (running)", "--force"} {
			if !strings.Contains(stderr, want) {
				t.Errorf("stderr = %q, want it to contain %q", stderr, want)
			}
		}
	})

	t.Run("an ambiguous name is refused rather than guessed", func(t *testing.T) {
		stub := newStubDaemon(t, snapshotRoute())

		code, _, stderr := runClient(workstreamRemove(), "feature")
		if code != exitFailed {
			t.Errorf("exit code = %d, want %d", code, exitFailed)
		}
		if !strings.Contains(stderr, "2 workstreams are named") {
			t.Errorf("stderr = %q, want the ambiguity reported", stderr)
		}
		for _, recorded := range stub.Calls() {
			if recorded.Method == http.MethodDelete {
				t.Fatalf("the verb removed %s despite the ambiguity", recorded.Path)
			}
		}
	})
}

func TestWorkstreamOpen(t *testing.T) {
	const openPath = "/api/projects/proj-1/workstreams/ws-1/open"
	const openWithPath = "/api/projects/proj-1/workstreams/ws-1/open-with"

	// The open endpoint ignores any body and always runs the default, so a chosen
	// mode must go to open-with — the endpoint that reads and validates it.
	t.Run("posts a chosen mode to open-with and prints a resolved URL", func(t *testing.T) {
		stub := newStubDaemon(t, map[string]stubResponse{
			"GET /api/projects": {http.StatusOK, testSnapshotJSON},
			"POST " + openWithPath: {http.StatusOK,
				`{"mode":"web-ide","data":{"target":"browser","url":"http://127.0.0.1:3000/"}}`},
		})

		code, stdout, stderr := runClient(workstreamOpen(), "ws-1", "-mode", "web-ide")
		if code != exitOK {
			t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
		}
		if body := stub.call(t, http.MethodPost, openWithPath).Body; !strings.Contains(body, `"mode":"web-ide"`) {
			t.Errorf("posted %s, want the chosen mode", body)
		}
		for _, recorded := range stub.Calls() {
			if recorded.Path == openPath {
				t.Errorf("a chosen mode also hit %s, which would open the default", openPath)
			}
		}
		for _, want := range []string{"web-ide", "http://127.0.0.1:3000/"} {
			if !strings.Contains(stdout, want) {
				t.Errorf("stdout = %q, want it to contain %q", stdout, want)
			}
		}
	})

	t.Run("no mode posts the default open without a body", func(t *testing.T) {
		stub := newStubDaemon(t, map[string]stubResponse{
			"GET /api/projects": {http.StatusOK, testSnapshotJSON},
			"POST " + openPath:  {http.StatusOK, `{"mode":"terminal","data":{}}`},
		})

		code, stdout, stderr := runClient(workstreamOpen(), "ws-1")
		if code != exitOK {
			t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
		}
		if body := stub.call(t, http.MethodPost, openPath).Body; body != "" {
			t.Errorf("posted %q to the default open, want no body", body)
		}
		if !strings.Contains(stdout, "opened feature in mode terminal") {
			t.Errorf("stdout = %q, want it to name the mode the daemon used", stdout)
		}
	})

	t.Run("a refused mode is answered with the modes the daemon offers", func(t *testing.T) {
		newStubDaemon(t, map[string]stubResponse{
			"GET /api/projects": {http.StatusOK, testSnapshotJSON},
			"POST " + openWithPath: {http.StatusBadRequest,
				`{"error":"unknown open mode: \"nonsense\""}`},
		})

		code, _, stderr := runClient(workstreamOpen(), "ws-1", "-mode", "nonsense")
		if code != exitFailed {
			t.Errorf("exit code = %d, want %d", code, exitFailed)
		}
		for _, want := range []string{"unknown open mode", "terminal (Terminal)", "web-ide (Web IDE)"} {
			if !strings.Contains(stderr, want) {
				t.Errorf("stderr = %q, want it to contain %q", stderr, want)
			}
		}
	})

	t.Run("a workstream with no modes says so", func(t *testing.T) {
		newStubDaemon(t, map[string]stubResponse{
			"GET /api/projects": {http.StatusOK, testSnapshotJSON},
			"POST /api/projects/proj-2/workstreams/ws-2/open": {http.StatusConflict,
				`{"error":"nothing to open"}`},
		})

		code, _, stderr := runClient(workstreamOpen(), "ws-2")
		if code != exitFailed {
			t.Errorf("exit code = %d, want %d", code, exitFailed)
		}
		if !strings.Contains(stderr, "offers no open modes") {
			t.Errorf("stderr = %q, want it to report that no mode is on offer", stderr)
		}
	})
}

func TestParseConfigFlag(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{name: "empty yields nothing to send", raw: "   ", want: ""},
		{name: "an object is forwarded verbatim", raw: ` {"a":1} `, want: `{"a":1}`},
		{name: "not JSON is a usage error", raw: "a=1", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseConfigFlag(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %s", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("config = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWorkstreamProviders(t *testing.T) {
	t.Run("lists what can create in the project, with each status", func(t *testing.T) {
		newStubDaemon(t, map[string]stubResponse{
			"GET /api/projects": {http.StatusOK, testSnapshotJSON},
			"GET /api/projects/proj-1/workstream-providers": {http.StatusOK,
				`[{"id":"core","label":"Default","status":{"state":"available"}},` +
					`{"id":"enclave","label":"Eclipse Enclave","status":{"state":"unavailable","reason":"enclave not on PATH"}}]`},
		})

		code, stdout, stderr := runClient(workstreamProviders(), "-project", "alpha")
		if code != exitOK {
			t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
		}
		for _, want := range []string{"ID", "LABEL", "STATE", "REASON",
			"core", "Default", "available", "enclave", "unavailable", "enclave not on PATH"} {
			if !strings.Contains(stdout, want) {
				t.Errorf("stdout = %q, want it to contain %q", stdout, want)
			}
		}
	})

	t.Run("-project is required", func(t *testing.T) {
		newStubDaemon(t, snapshotRoute())

		code, _, stderr := runClient(workstreamProviders())
		if code != exitUsage {
			t.Errorf("exit code = %d, want %d", code, exitUsage)
		}
		if !strings.Contains(stderr, "-project is required") {
			t.Errorf("stderr = %q, want it to name the missing flag", stderr)
		}
	})
}

func TestWorkstreamRename(t *testing.T) {
	t.Run("patches the resolved workstream with the new name", func(t *testing.T) {
		stub := newStubDaemon(t, map[string]stubResponse{
			"GET /api/projects":                           {http.StatusOK, testSnapshotJSON},
			"PATCH /api/projects/proj-2/workstreams/ws-3": {http.StatusNoContent, ""},
		})

		code, stdout, stderr := runClient(workstreamRename(), "extra", "renamed")
		if code != exitOK {
			t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
		}
		var sent map[string]string
		body := stub.call(t, http.MethodPatch, "/api/projects/proj-2/workstreams/ws-3").Body
		if err := json.Unmarshal([]byte(body), &sent); err != nil {
			t.Fatal(err)
		}
		if sent["name"] != "renamed" {
			t.Errorf("patched name = %q, want %q", sent["name"], "renamed")
		}
		if !strings.Contains(stdout, "renamed workstream extra to renamed") {
			t.Errorf("stdout = %q, want it to confirm the rename", stdout)
		}
	})

	t.Run("a duplicate name is the daemon's 409 to report", func(t *testing.T) {
		newStubDaemon(t, map[string]stubResponse{
			"GET /api/projects":                           {http.StatusOK, testSnapshotJSON},
			"PATCH /api/projects/proj-2/workstreams/ws-3": {http.StatusConflict, `{"error":"a workstream named \"feature\" already exists"}`},
		})

		code, _, stderr := runClient(workstreamRename(), "extra", "feature")
		if code != exitFailed {
			t.Errorf("exit code = %d, want %d", code, exitFailed)
		}
		if !strings.Contains(stderr, "already exists") {
			t.Errorf("stderr = %q, want the daemon's own refusal", stderr)
		}
	})

	t.Run("an ambiguous name is refused before any PATCH", func(t *testing.T) {
		stub := newStubDaemon(t, snapshotRoute())

		// "feature" names a workstream in both projects.
		code, _, stderr := runClient(workstreamRename(), "feature", "whatever")
		if code != exitFailed {
			t.Errorf("exit code = %d, want %d", code, exitFailed)
		}
		if !strings.Contains(stderr, "workstreams are named") {
			t.Errorf("stderr = %q, want it to report the ambiguity", stderr)
		}
		for _, recorded := range stub.Calls() {
			if recorded.Method == http.MethodPatch {
				t.Errorf("the verb issued a PATCH for an ambiguous reference")
			}
		}
	})
}

func TestWorkstreamNote(t *testing.T) {
	t.Run("reports the path the daemon opened", func(t *testing.T) {
		newStubDaemon(t, map[string]stubResponse{
			"GET /api/projects": {http.StatusOK, testSnapshotJSON},
			"POST /api/projects/proj-2/workstreams/ws-3/note": {http.StatusOK,
				`{"path":"/tmp/beta/NOTES.md","created":false}`},
		})

		code, stdout, stderr := runClient(workstreamNote(), "extra")
		if code != exitOK {
			t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
		}
		if !strings.Contains(stdout, "opened /tmp/beta/NOTES.md") {
			t.Errorf("stdout = %q, want it to name the note it opened", stdout)
		}
	})

	t.Run("says when the note was created on this call", func(t *testing.T) {
		newStubDaemon(t, map[string]stubResponse{
			"GET /api/projects": {http.StatusOK, testSnapshotJSON},
			"POST /api/projects/proj-2/workstreams/ws-3/note": {http.StatusOK,
				`{"path":"/tmp/beta/NOTES.md","created":true}`},
		})

		_, stdout, _ := runClient(workstreamNote(), "extra")
		if !strings.Contains(stdout, "created and opened") {
			t.Errorf("stdout = %q, want it to report the note was created", stdout)
		}
	})
}

func TestWorkstreamClearStatus(t *testing.T) {
	t.Run("deletes the status subresource", func(t *testing.T) {
		stub := newStubDaemon(t, map[string]stubResponse{
			"GET /api/projects": {http.StatusOK, testSnapshotJSON},
			"DELETE /api/projects/proj-2/workstreams/ws-3/status": {http.StatusNoContent, ""},
		})

		code, stdout, stderr := runClient(workstreamClearStatus(), "extra")
		if code != exitOK {
			t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
		}
		stub.call(t, http.MethodDelete, "/api/projects/proj-2/workstreams/ws-3/status")
		if !strings.Contains(stdout, "cleared the status message") {
			t.Errorf("stdout = %q, want it to confirm the clear", stdout)
		}
	})

	t.Run("a workstream in error is the daemon's 409 to refuse", func(t *testing.T) {
		newStubDaemon(t, map[string]stubResponse{
			"GET /api/projects": {http.StatusOK, testSnapshotJSON},
			"DELETE /api/projects/proj-2/workstreams/ws-3/status": {http.StatusConflict,
				`{"error":"the workstream failed to be created; remove it instead"}`},
		})

		code, _, stderr := runClient(workstreamClearStatus(), "extra")
		if code != exitFailed {
			t.Errorf("exit code = %d, want %d", code, exitFailed)
		}
		if !strings.Contains(stderr, "remove it instead") {
			t.Errorf("stderr = %q, want the daemon's own refusal", stderr)
		}
	})
}
