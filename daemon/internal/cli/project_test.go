package cli

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestProjectList(t *testing.T) {
	t.Run("prints a table of projects with their states", func(t *testing.T) {
		newStubDaemon(t, snapshotRoute())

		code, stdout, stderr := runClient(projectList())
		if code != exitOK {
			t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
		}
		for _, want := range []string{"ID", "NAME", "STATE", "WORKSTREAMS", "PATH",
			"proj-1", "alpha", "running", "/tmp/alpha", "proj-2", "beta", "idle"} {
			if !strings.Contains(stdout, want) {
				t.Errorf("stdout = %q, want it to contain %q", stdout, want)
			}
		}
	})

	t.Run("counts each project's workstreams", func(t *testing.T) {
		newStubDaemon(t, snapshotRoute())

		_, stdout, _ := runClient(projectList())
		for _, line := range strings.Split(stdout, "\n") {
			if strings.HasPrefix(line, "proj-2") && !strings.Contains(line, " 2 ") {
				t.Errorf("beta's row = %q, want it to report 2 workstreams", line)
			}
		}
	})

	t.Run("--json passes the payload through untouched", func(t *testing.T) {
		newStubDaemon(t, snapshotRoute())

		code, stdout, stderr := runClient(projectList(), "--json")
		if code != exitOK {
			t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
		}
		if strings.TrimSpace(stdout) != testSnapshotJSON {
			t.Errorf("stdout = %q, want the exact payload the daemon sent", stdout)
		}
	})

	t.Run("a surplus argument is a usage error", func(t *testing.T) {
		newStubDaemon(t, snapshotRoute())

		code, _, stderr := runClient(projectList(), "oops")
		if code != exitUsage {
			t.Errorf("exit code = %d, want %d", code, exitUsage)
		}
		if !strings.Contains(stderr, "unexpected argument") {
			t.Errorf("stderr = %q, want it to reject the argument", stderr)
		}
	})
}

func TestProjectAdd(t *testing.T) {
	t.Run("posts the path and reports what the daemon created", func(t *testing.T) {
		stub := newStubDaemon(t, map[string]stubResponse{
			"POST /api/projects": {http.StatusCreated,
				`{"id":"proj-new","name":"tools","path":"/srv/tools","state":"idle","workstreams":[]}`},
		})

		code, stdout, stderr := runClient(projectAdd(), "/srv/tools")
		if code != exitOK {
			t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
		}
		var sent map[string]string
		if err := json.Unmarshal([]byte(stub.call(t, http.MethodPost, "/api/projects").Body), &sent); err != nil {
			t.Fatal(err)
		}
		if sent["path"] != "/srv/tools" {
			t.Errorf("posted path = %q, want /srv/tools", sent["path"])
		}
		for _, want := range []string{"tools", "proj-new", "/srv/tools"} {
			if !strings.Contains(stdout, want) {
				t.Errorf("stdout = %q, want it to contain %q", stdout, want)
			}
		}
	})

	t.Run("-name is forwarded for the daemon to apply", func(t *testing.T) {
		stub := newStubDaemon(t, map[string]stubResponse{
			"POST /api/projects": {http.StatusCreated, `{"id":"proj-new","name":"My Tools","path":"/srv/tools"}`},
		})

		if code, _, stderr := runClient(projectAdd(), "/srv/tools", "-name", "My Tools"); code != exitOK {
			t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
		}
		var sent map[string]string
		if err := json.Unmarshal([]byte(stub.call(t, http.MethodPost, "/api/projects").Body), &sent); err != nil {
			t.Fatal(err)
		}
		if sent["name"] != "My Tools" {
			t.Errorf("posted name = %q, want %q", sent["name"], "My Tools")
		}
	})

	t.Run("the daemon's validation message is what the user sees", func(t *testing.T) {
		newStubDaemon(t, map[string]stubResponse{
			"POST /api/projects": {http.StatusBadRequest, `{"error":"path does not exist"}`},
		})

		code, _, stderr := runClient(projectAdd(), "/nope")
		if code != exitFailed {
			t.Errorf("exit code = %d, want %d", code, exitFailed)
		}
		if !strings.Contains(stderr, "path does not exist") {
			t.Errorf("stderr = %q, want the daemon's own message", stderr)
		}
	})

	t.Run("a missing path is a usage error", func(t *testing.T) {
		newStubDaemon(t, snapshotRoute())

		code, _, stderr := runClient(projectAdd())
		if code != exitUsage {
			t.Errorf("exit code = %d, want %d", code, exitUsage)
		}
		if !strings.Contains(stderr, "a directory path is required") {
			t.Errorf("stderr = %q, want it to name the missing argument", stderr)
		}
	})
}

func TestProjectRename(t *testing.T) {
	t.Run("patches the resolved project with the new name", func(t *testing.T) {
		stub := newStubDaemon(t, map[string]stubResponse{
			"GET /api/projects":          {http.StatusOK, testSnapshotJSON},
			"PATCH /api/projects/proj-2": {http.StatusNoContent, ""},
		})

		code, stdout, stderr := runClient(projectRename(), "beta", "Beta Renamed")
		if code != exitOK {
			t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
		}
		var sent map[string]string
		if err := json.Unmarshal([]byte(stub.call(t, http.MethodPatch, "/api/projects/proj-2").Body), &sent); err != nil {
			t.Fatal(err)
		}
		if sent["name"] != "Beta Renamed" {
			t.Errorf("patched name = %q, want %q", sent["name"], "Beta Renamed")
		}
		if !strings.Contains(stdout, "renamed project beta to Beta Renamed") {
			t.Errorf("stdout = %q, want it to confirm the rename", stdout)
		}
	})

	t.Run("a missing new name is a usage error naming it", func(t *testing.T) {
		newStubDaemon(t, snapshotRoute())

		code, _, stderr := runClient(projectRename(), "beta")
		if code != exitUsage {
			t.Errorf("exit code = %d, want %d", code, exitUsage)
		}
		if !strings.Contains(stderr, "a new name is required") {
			t.Errorf("stderr = %q, want it to name the missing argument", stderr)
		}
	})

	t.Run("the daemon's refusal is what the user sees", func(t *testing.T) {
		newStubDaemon(t, map[string]stubResponse{
			"GET /api/projects":          {http.StatusOK, testSnapshotJSON},
			"PATCH /api/projects/proj-2": {http.StatusBadRequest, `{"error":"name must not be empty"}`},
		})

		code, _, stderr := runClient(projectRename(), "beta", " ")
		if code != exitFailed {
			t.Errorf("exit code = %d, want %d", code, exitFailed)
		}
		if !strings.Contains(stderr, "name must not be empty") {
			t.Errorf("stderr = %q, want the daemon's own message", stderr)
		}
	})
}

func TestProjectRefresh(t *testing.T) {
	t.Run("posts the refresh and reports what was re-detected", func(t *testing.T) {
		stub := newStubDaemon(t, map[string]stubResponse{
			"GET /api/projects":                 {http.StatusOK, testSnapshotJSON},
			"POST /api/projects/proj-1/refresh": {http.StatusNoContent, ""},
		})

		code, stdout, stderr := runClient(projectRefresh(), "alpha")
		if code != exitOK {
			t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
		}
		stub.call(t, http.MethodPost, "/api/projects/proj-1/refresh")
		// alpha is gitBacked in the fixture, so the re-read reports that.
		if !strings.Contains(stdout, "git-backed") {
			t.Errorf("stdout = %q, want it to report the detected git-backing", stdout)
		}
	})

	t.Run("a project that is not git-backed says so", func(t *testing.T) {
		newStubDaemon(t, map[string]stubResponse{
			"GET /api/projects":                 {http.StatusOK, testSnapshotJSON},
			"POST /api/projects/proj-2/refresh": {http.StatusNoContent, ""},
		})

		_, stdout, _ := runClient(projectRefresh(), "beta")
		if !strings.Contains(stdout, "not git-backed") {
			t.Errorf("stdout = %q, want it to report beta as not git-backed", stdout)
		}
	})
}

func TestProjectRemove(t *testing.T) {
	t.Run("resolves a name to the ID the API needs", func(t *testing.T) {
		stub := newStubDaemon(t, map[string]stubResponse{
			"GET /api/projects":           {http.StatusOK, testSnapshotJSON},
			"DELETE /api/projects/proj-2": {http.StatusNoContent, ""},
		})

		code, stdout, stderr := runClient(projectRemove(), "beta")
		if code != exitOK {
			t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
		}
		stub.call(t, http.MethodDelete, "/api/projects/proj-2")
		if !strings.Contains(stdout, "removed project beta") {
			t.Errorf("stdout = %q, want it to confirm the removal", stdout)
		}
	})

	t.Run("an unknown project is refused before any DELETE", func(t *testing.T) {
		stub := newStubDaemon(t, snapshotRoute())

		code, _, stderr := runClient(projectRemove(), "gamma")
		if code != exitFailed {
			t.Errorf("exit code = %d, want %d", code, exitFailed)
		}
		if !strings.Contains(stderr, `no project matches "gamma"`) {
			t.Errorf("stderr = %q, want it to say which reference missed", stderr)
		}
		for _, recorded := range stub.Calls() {
			if recorded.Method == http.MethodDelete {
				t.Errorf("the verb issued %s %s for an unresolved project", recorded.Method, recorded.Path)
			}
		}
	})
}
