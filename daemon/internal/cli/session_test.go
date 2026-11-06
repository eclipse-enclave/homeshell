package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// sessionSchemasPath is the endpoint `session new` reads its options from.
const sessionSchemasPath = "GET /api/projects/proj-1/workstreams/ws-1/session-schemas"

func TestSessionList(t *testing.T) {
	t.Run("lists every session with its workstream and project", func(t *testing.T) {
		newStubDaemon(t, snapshotRoute())

		code, stdout, stderr := runClient(sessionList())
		if code != exitOK {
			t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
		}
		for _, want := range []string{"sess-1", "Implement it", "Claude Code", "running", "feature", "alpha",
			"sess-2", "Codex", "stopped", "extra", "beta"} {
			if !strings.Contains(stdout, want) {
				t.Errorf("stdout = %q, want it to contain %q", stdout, want)
			}
		}
	})

	t.Run("an untitled session still lines up in the table", func(t *testing.T) {
		newStubDaemon(t, snapshotRoute())

		_, stdout, _ := runClient(sessionList())
		for _, line := range strings.Split(stdout, "\n") {
			if strings.HasPrefix(line, "sess-2") && !strings.Contains(line, "-") {
				t.Errorf("sess-2's row = %q, want a placeholder for the empty title", line)
			}
		}
	})

	t.Run("-workstream narrows the list", func(t *testing.T) {
		newStubDaemon(t, snapshotRoute())

		code, stdout, stderr := runClient(sessionList(), "-workstream", "extra")
		if code != exitOK {
			t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
		}
		if strings.Contains(stdout, "sess-1") || !strings.Contains(stdout, "sess-2") {
			t.Errorf("stdout = %q, want only the sessions of the named workstream", stdout)
		}
	})

	t.Run("--json emits the daemon's session objects", func(t *testing.T) {
		newStubDaemon(t, snapshotRoute())

		code, stdout, stderr := runClient(sessionList(), "-workstream", "ws-1", "--json")
		if code != exitOK {
			t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
		}
		var listed []map[string]any
		if err := json.Unmarshal([]byte(stdout), &listed); err != nil {
			t.Fatalf("stdout is not a JSON array: %v (%q)", err, stdout)
		}
		if len(listed) != 1 || listed[0]["id"] != "sess-1" || listed[0]["provider"] != "local" {
			t.Errorf("listed = %v, want the daemon's sess-1 object", listed)
		}
	})
}

func TestSessionNew(t *testing.T) {
	t.Run("posts the resolved provider and option", func(t *testing.T) {
		stub := newStubDaemon(t, map[string]stubResponse{
			"GET /api/projects": {http.StatusOK, testSnapshotJSON},
			sessionSchemasPath: {http.StatusOK,
				`[{"providerId":"local","label":"Local System","options":[` +
					`{"id":"anthropic/claude","label":"Claude Code","schema":{}},` +
					`{"id":"openai/codex","label":"Codex","schema":{}}]}]`},
			"POST /api/projects/proj-1/workstreams/ws-1/sessions": {http.StatusAccepted, ""},
		})

		code, stdout, stderr := runClient(sessionNew(),
			"-workstream", "ws-1", "-agent", "anthropic/claude", "-config", `{"model":"opus"}`)
		if code != exitOK {
			t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
		}
		var sent struct {
			ProviderID string          `json:"providerId"`
			OptionID   string          `json:"optionId"`
			Config     json.RawMessage `json:"config"`
		}
		body := stub.call(t, http.MethodPost, "/api/projects/proj-1/workstreams/ws-1/sessions").Body
		if err := json.Unmarshal([]byte(body), &sent); err != nil {
			t.Fatal(err)
		}
		if sent.ProviderID != "local" || sent.OptionID != "anthropic/claude" {
			t.Errorf("posted %+v, want the local provider and the claude option", sent)
		}
		if string(sent.Config) != `{"model":"opus"}` {
			t.Errorf("posted config = %s, want it forwarded verbatim", sent.Config)
		}
		if !strings.Contains(stdout, "creating") {
			t.Errorf("stdout = %q, want it to report creation as in progress (the API answers 202)", stdout)
		}
	})

	t.Run("a single offered option needs no -agent", func(t *testing.T) {
		stub := newStubDaemon(t, map[string]stubResponse{
			"GET /api/projects": {http.StatusOK, testSnapshotJSON},
			sessionSchemasPath: {http.StatusOK,
				`[{"providerId":"local","label":"Local System","options":[{"id":"anthropic/claude","label":"Claude Code","schema":{}}]}]`},
			"POST /api/projects/proj-1/workstreams/ws-1/sessions": {http.StatusAccepted, ""},
		})

		if code, _, stderr := runClient(sessionNew(), "-workstream", "ws-1"); code != exitOK {
			t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
		}
		body := stub.call(t, http.MethodPost, "/api/projects/proj-1/workstreams/ws-1/sessions").Body
		if !strings.Contains(body, `"optionId":"anthropic/claude"`) {
			t.Errorf("posted %s, want the single offered option", body)
		}
	})

	t.Run("several options and no -agent lists them", func(t *testing.T) {
		newStubDaemon(t, map[string]stubResponse{
			"GET /api/projects": {http.StatusOK, testSnapshotJSON},
			sessionSchemasPath: {http.StatusOK,
				`[{"providerId":"local","label":"Local System","options":[` +
					`{"id":"anthropic/claude","label":"Claude Code","schema":{}},` +
					`{"id":"openai/codex","label":"Codex","schema":{}}]}]`},
		})

		code, _, stderr := runClient(sessionNew(), "-workstream", "ws-1")
		if code != exitUsage {
			t.Errorf("exit code = %d, want %d", code, exitUsage)
		}
		for _, want := range []string{"-agent is required", "local/anthropic/claude", "local/openai/codex", "Codex"} {
			if !strings.Contains(stderr, want) {
				t.Errorf("stderr = %q, want it to contain %q", stderr, want)
			}
		}
	})

	t.Run("an unknown -agent reports what is on offer", func(t *testing.T) {
		newStubDaemon(t, map[string]stubResponse{
			"GET /api/projects": {http.StatusOK, testSnapshotJSON},
			sessionSchemasPath: {http.StatusOK,
				`[{"providerId":"local","label":"Local System","options":[{"id":"anthropic/claude","label":"Claude Code","schema":{}}]}]`},
		})

		code, _, stderr := runClient(sessionNew(), "-workstream", "ws-1", "-agent", "gemini")
		if code != exitFailed {
			t.Errorf("exit code = %d, want %d", code, exitFailed)
		}
		for _, want := range []string{`no session option "gemini"`, "local/anthropic/claude"} {
			if !strings.Contains(stderr, want) {
				t.Errorf("stderr = %q, want it to contain %q", stderr, want)
			}
		}
	})

	t.Run("no provider offering anything is explained", func(t *testing.T) {
		newStubDaemon(t, map[string]stubResponse{
			"GET /api/projects": {http.StatusOK, testSnapshotJSON},
			sessionSchemasPath:  {http.StatusOK, `[]`},
		})

		code, _, stderr := runClient(sessionNew(), "-workstream", "ws-1")
		if code != exitFailed {
			t.Errorf("exit code = %d, want %d", code, exitFailed)
		}
		if !strings.Contains(stderr, "no provider offers a session") {
			t.Errorf("stderr = %q, want it to explain there is nothing to create", stderr)
		}
	})

	t.Run("a missing -workstream is a usage error", func(t *testing.T) {
		newStubDaemon(t, snapshotRoute())

		code, _, stderr := runClient(sessionNew())
		if code != exitUsage {
			t.Errorf("exit code = %d, want %d", code, exitUsage)
		}
		if !strings.Contains(stderr, "-workstream is required") {
			t.Errorf("stderr = %q, want it to name the missing flag", stderr)
		}
	})

	t.Run("the concurrency limit is the daemon's to enforce and report", func(t *testing.T) {
		newStubDaemon(t, map[string]stubResponse{
			"GET /api/projects": {http.StatusOK, testSnapshotJSON},
			sessionSchemasPath: {http.StatusOK,
				`[{"providerId":"local","label":"Local System","options":[{"id":"anthropic/claude","label":"Claude Code","schema":{}}]}]`},
			"POST /api/projects/proj-1/workstreams/ws-1/sessions": {http.StatusConflict,
				`{"error":"the workstream already runs its maximum of 1 session"}`},
		})

		code, _, stderr := runClient(sessionNew(), "-workstream", "ws-1")
		if code != exitFailed {
			t.Errorf("exit code = %d, want %d", code, exitFailed)
		}
		if !strings.Contains(stderr, "maximum of 1 session") {
			t.Errorf("stderr = %q, want the daemon's own refusal", stderr)
		}
	})
}

func TestSessionStartAndStop(t *testing.T) {
	tests := []struct {
		name     string
		command  clientCommand
		path     string
		wantWord string
	}{
		{name: "stop", command: sessionStop(), path: "/api/projects/proj-1/workstreams/ws-1/sessions/sess-1/stop", wantWord: "stopped"},
		{name: "start", command: sessionStart(), path: "/api/projects/proj-1/workstreams/ws-1/sessions/sess-1/start", wantWord: "started"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := newStubDaemon(t, map[string]stubResponse{
				"GET /api/projects": {http.StatusOK, testSnapshotJSON},
				"POST " + tt.path:   {http.StatusNoContent, ""},
			})

			code, stdout, stderr := runClient(tt.command, "sess-1")
			if code != exitOK {
				t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
			}
			stub.call(t, http.MethodPost, tt.path)
			if !strings.Contains(stdout, tt.wantWord) {
				t.Errorf("stdout = %q, want it to report the session %s", stdout, tt.wantWord)
			}
		})
	}

	t.Run("a session can be named by its title", func(t *testing.T) {
		stub := newStubDaemon(t, map[string]stubResponse{
			"GET /api/projects": {http.StatusOK, testSnapshotJSON},
			"POST /api/projects/proj-1/workstreams/ws-1/sessions/sess-1/stop": {http.StatusNoContent, ""},
		})

		if code, _, stderr := runClient(sessionStop(), "Implement it"); code != exitOK {
			t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
		}
		stub.call(t, http.MethodPost, "/api/projects/proj-1/workstreams/ws-1/sessions/sess-1/stop")
	})

	t.Run("a stopped session refusing to be prompted is the daemon's answer", func(t *testing.T) {
		newStubDaemon(t, map[string]stubResponse{
			"GET /api/projects": {http.StatusOK, testSnapshotJSON},
			"POST /api/projects/proj-1/workstreams/ws-1/sessions/sess-1/start": {http.StatusConflict,
				`{"error":"the session is stopped"}`},
		})

		code, _, stderr := runClient(sessionStart(), "sess-1")
		if code != exitFailed {
			t.Errorf("exit code = %d, want %d", code, exitFailed)
		}
		if !strings.Contains(stderr, "the session is stopped") {
			t.Errorf("stderr = %q, want the daemon's message", stderr)
		}
	})
}

func TestSessionPrompt(t *testing.T) {
	const promptPath = "/api/projects/proj-1/workstreams/ws-1/sessions/sess-1/prompt"

	t.Run("joins the trailing arguments into one line", func(t *testing.T) {
		stub := newStubDaemon(t, map[string]stubResponse{
			"GET /api/projects":  {http.StatusOK, testSnapshotJSON},
			"POST " + promptPath: {http.StatusNoContent, ""},
		})

		code, stdout, stderr := runClient(sessionPrompt(), "sess-1", "run", "the", "tests")
		if code != exitOK {
			t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
		}
		var sent map[string]string
		if err := json.Unmarshal([]byte(stub.call(t, http.MethodPost, promptPath).Body), &sent); err != nil {
			t.Fatal(err)
		}
		if sent["text"] != "run the tests" {
			t.Errorf("sent text = %q, want %q", sent["text"], "run the tests")
		}
		if !strings.Contains(stdout, "sent 13 characters") {
			t.Errorf("stdout = %q, want it to report what was sent", stdout)
		}
	})

	t.Run("reads stdin when no text is given", func(t *testing.T) {
		stub := newStubDaemon(t, map[string]stubResponse{
			"GET /api/projects":  {http.StatusOK, testSnapshotJSON},
			"POST " + promptPath: {http.StatusNoContent, ""},
		})

		code, _, stderr := runClientWithStdin(sessionPrompt(), "from a pipe\n", "sess-1")
		if code != exitOK {
			t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
		}
		var sent map[string]string
		if err := json.Unmarshal([]byte(stub.call(t, http.MethodPost, promptPath).Body), &sent); err != nil {
			t.Fatal(err)
		}
		// The trailing newline is the pipe's, not part of the line to type.
		if sent["text"] != "from a pipe" {
			t.Errorf("sent text = %q, want %q", sent["text"], "from a pipe")
		}
	})

	t.Run("no text anywhere is a usage error", func(t *testing.T) {
		newStubDaemon(t, snapshotRoute())

		code, _, stderr := runClient(sessionPrompt(), "sess-1")
		if code != exitUsage {
			t.Errorf("exit code = %d, want %d", code, exitUsage)
		}
		if !strings.Contains(stderr, "prompt text") {
			t.Errorf("stderr = %q, want it to say text is required", stderr)
		}
	})

	t.Run("dashed text survives a terminator", func(t *testing.T) {
		stub := newStubDaemon(t, map[string]stubResponse{
			"GET /api/projects":  {http.StatusOK, testSnapshotJSON},
			"POST " + promptPath: {http.StatusNoContent, ""},
		})

		if code, _, stderr := runClient(sessionPrompt(), "sess-1", "--", "-y"); code != exitOK {
			t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
		}
		if !strings.Contains(stub.call(t, http.MethodPost, promptPath).Body, `"text":"-y"`) {
			t.Errorf("sent %s, want the dashed text as the prompt", stub.call(t, http.MethodPost, promptPath).Body)
		}
	})

	t.Run("a provider that cannot be prompted is reported as unsupported", func(t *testing.T) {
		newStubDaemon(t, map[string]stubResponse{
			"GET /api/projects":  {http.StatusOK, testSnapshotJSON},
			"POST " + promptPath: {http.StatusNotImplemented, `{"error":"the provider does not support prompting"}`},
		})

		code, _, stderr := runClient(sessionPrompt(), "sess-1", "hello")
		if code != exitFailed {
			t.Errorf("exit code = %d, want %d", code, exitFailed)
		}
		if !strings.Contains(stderr, "does not support prompting") {
			t.Errorf("stderr = %q, want the daemon's message", stderr)
		}
	})
}

func TestSessionOpen(t *testing.T) {
	const openPath = "/api/projects/proj-1/workstreams/ws-1/sessions/sess-1/open"

	t.Run("reports the mode the daemon opened", func(t *testing.T) {
		stub := newStubDaemon(t, map[string]stubResponse{
			"GET /api/projects": {http.StatusOK, testSnapshotJSON},
			"POST " + openPath:  {http.StatusOK, `{"mode":"terminal","data":{}}`},
		})

		code, stdout, stderr := runClient(sessionOpen(), "sess-1")
		if code != exitOK {
			t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
		}
		stub.call(t, http.MethodPost, openPath)
		if !strings.Contains(stdout, "terminal") {
			t.Errorf("stdout = %q, want it to name the mode", stdout)
		}
	})

	t.Run("--json passes the open result through", func(t *testing.T) {
		newStubDaemon(t, map[string]stubResponse{
			"GET /api/projects": {http.StatusOK, testSnapshotJSON},
			"POST " + openPath:  {http.StatusOK, `{"mode":"terminal","data":{"pid":42}}`},
		})

		code, stdout, stderr := runClient(sessionOpen(), "sess-1", "--json")
		if code != exitOK {
			t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
		}
		if strings.TrimSpace(stdout) != `{"mode":"terminal","data":{"pid":42}}` {
			t.Errorf("stdout = %q, want the payload untouched", stdout)
		}
	})
}

func TestSessionAgents(t *testing.T) {
	t.Run("lists the option IDs that -agent takes", func(t *testing.T) {
		newStubDaemon(t, map[string]stubResponse{
			"GET /api/projects": {http.StatusOK, testSnapshotJSON},
			"GET /api/projects/proj-1/workstreams/ws-1/session-schemas": {http.StatusOK,
				`[{"providerId":"local","label":"Local System","options":[` +
					`{"id":"anthropic/claude-opus-4-8","label":"Claude Opus 4.8"},` +
					`{"id":"openai/gpt-5.5","label":"Codex (GPT-5.5)"}]}]`},
		})

		code, stdout, stderr := runClient(sessionAgents(), "-workstream", "ws-1")
		if code != exitOK {
			t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
		}
		for _, want := range []string{"AGENT", "LABEL", "PROVIDER",
			"anthropic/claude-opus-4-8", "Claude Opus 4.8", "openai/gpt-5.5", "local"} {
			if !strings.Contains(stdout, want) {
				t.Errorf("stdout = %q, want it to contain %q", stdout, want)
			}
		}
	})

	t.Run("-workstream is required", func(t *testing.T) {
		newStubDaemon(t, snapshotRoute())

		code, _, stderr := runClient(sessionAgents())
		if code != exitUsage {
			t.Errorf("exit code = %d, want %d", code, exitUsage)
		}
		if !strings.Contains(stderr, "-workstream is required") {
			t.Errorf("stderr = %q, want it to name the missing flag", stderr)
		}
	})
}

func TestSessionRename(t *testing.T) {
	t.Run("patches the session with the new title", func(t *testing.T) {
		stub := newStubDaemon(t, map[string]stubResponse{
			"GET /api/projects": {http.StatusOK, testSnapshotJSON},
			"PATCH /api/projects/proj-1/workstreams/ws-1/sessions/sess-1": {http.StatusNoContent, ""},
		})

		code, stdout, stderr := runClient(sessionRename(), "sess-1", "My Session")
		if code != exitOK {
			t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
		}
		var sent map[string]string
		body := stub.call(t, http.MethodPatch, "/api/projects/proj-1/workstreams/ws-1/sessions/sess-1").Body
		if err := json.Unmarshal([]byte(body), &sent); err != nil {
			t.Fatal(err)
		}
		if sent["title"] != "My Session" {
			t.Errorf("patched title = %q, want %q", sent["title"], "My Session")
		}
		if !strings.Contains(stdout, "renamed session sess-1 to My Session") {
			t.Errorf("stdout = %q, want it to confirm the rename", stdout)
		}
	})

	t.Run("-clear deletes the title instead, and takes no new name", func(t *testing.T) {
		stub := newStubDaemon(t, map[string]stubResponse{
			"GET /api/projects": {http.StatusOK, testSnapshotJSON},
			"DELETE /api/projects/proj-1/workstreams/ws-1/sessions/sess-1/title": {http.StatusNoContent, ""},
		})

		code, stdout, stderr := runClient(sessionRename(), "sess-1", "-clear")
		if code != exitOK {
			t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
		}
		stub.call(t, http.MethodDelete, "/api/projects/proj-1/workstreams/ws-1/sessions/sess-1/title")
		for _, recorded := range stub.Calls() {
			if recorded.Method == http.MethodPatch {
				t.Errorf("-clear issued a PATCH as well as the DELETE")
			}
		}
		if !strings.Contains(stdout, "cleared the name") {
			t.Errorf("stdout = %q, want it to confirm the clear", stdout)
		}
	})

	t.Run("a missing title is a usage error naming it", func(t *testing.T) {
		newStubDaemon(t, snapshotRoute())

		code, _, stderr := runClient(sessionRename(), "sess-1")
		if code != exitUsage {
			t.Errorf("exit code = %d, want %d", code, exitUsage)
		}
		if !strings.Contains(stderr, "a new title is required") {
			t.Errorf("stderr = %q, want it to name the missing argument", stderr)
		}
	})

	t.Run("a title a sibling already shows is the daemon's 409", func(t *testing.T) {
		newStubDaemon(t, map[string]stubResponse{
			"GET /api/projects": {http.StatusOK, testSnapshotJSON},
			"PATCH /api/projects/proj-1/workstreams/ws-1/sessions/sess-1": {http.StatusConflict,
				`{"error":"another session is already called \"Taken\""}`},
		})

		code, _, stderr := runClient(sessionRename(), "sess-1", "Taken")
		if code != exitFailed {
			t.Errorf("exit code = %d, want %d", code, exitFailed)
		}
		if !strings.Contains(stderr, "already called") {
			t.Errorf("stderr = %q, want the daemon's own refusal", stderr)
		}
	})
}

func TestSessionKey(t *testing.T) {
	t.Run("posts the named key to the session", func(t *testing.T) {
		stub := newStubDaemon(t, map[string]stubResponse{
			"GET /api/projects": {http.StatusOK, testSnapshotJSON},
			"POST /api/projects/proj-1/workstreams/ws-1/sessions/sess-1/key": {http.StatusNoContent, ""},
		})

		code, stdout, stderr := runClient(sessionKey(), "sess-1", "down")
		if code != exitOK {
			t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
		}
		var sent map[string]string
		body := stub.call(t, http.MethodPost, "/api/projects/proj-1/workstreams/ws-1/sessions/sess-1/key").Body
		if err := json.Unmarshal([]byte(body), &sent); err != nil {
			t.Fatal(err)
		}
		if sent["key"] != "down" {
			t.Errorf("posted key = %q, want %q", sent["key"], "down")
		}
		if !strings.Contains(stdout, "sent down to session") {
			t.Errorf("stdout = %q, want it to confirm the key", stdout)
		}
	})

	t.Run("an unknown key is the daemon's 400 to give", func(t *testing.T) {
		newStubDaemon(t, map[string]stubResponse{
			"GET /api/projects": {http.StatusOK, testSnapshotJSON},
			"POST /api/projects/proj-1/workstreams/ws-1/sessions/sess-1/key": {http.StatusBadRequest,
				`{"error":"unknown key \"wiggle\""}`},
		})

		code, _, stderr := runClient(sessionKey(), "sess-1", "wiggle")
		if code != exitFailed {
			t.Errorf("exit code = %d, want %d", code, exitFailed)
		}
		if !strings.Contains(stderr, `unknown key "wiggle"`) {
			t.Errorf("stderr = %q, want the daemon's own message", stderr)
		}
	})

	t.Run("a missing key is a usage error", func(t *testing.T) {
		newStubDaemon(t, snapshotRoute())

		code, _, stderr := runClient(sessionKey(), "sess-1")
		if code != exitUsage {
			t.Errorf("exit code = %d, want %d", code, exitUsage)
		}
		if !strings.Contains(stderr, "a key is required") {
			t.Errorf("stderr = %q, want it to name the missing argument", stderr)
		}
	})
}

func TestSessionRemove(t *testing.T) {
	t.Run("deletes the session and confirms it", func(t *testing.T) {
		stub := newStubDaemon(t, map[string]stubResponse{
			"GET /api/projects": {http.StatusOK, testSnapshotJSON},
			"DELETE /api/projects/proj-1/workstreams/ws-1/sessions/sess-1": {http.StatusNoContent, ""},
		})

		code, stdout, stderr := runClient(sessionRemove(), "sess-1")
		if code != exitOK {
			t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
		}
		stub.call(t, http.MethodDelete, "/api/projects/proj-1/workstreams/ws-1/sessions/sess-1")
		if !strings.Contains(stdout, "removed session") {
			t.Errorf("stdout = %q, want it to confirm the removal", stdout)
		}
	})

	t.Run("answers to the delete alias", func(t *testing.T) {
		if !sessionRemove().answersTo("delete") {
			t.Error("session remove should also answer to delete")
		}
		if !sessionRemove().answersTo("remove") {
			t.Error("session remove should still answer to its own name")
		}
	})
}

func TestSessionRestart(t *testing.T) {
	const base = "/api/projects/proj-1/workstreams/ws-1/sessions/sess-1"
	restartBody := func(t *testing.T, stub *stubDaemon) (config string, fresh bool) {
		t.Helper()
		var sent struct {
			Config json.RawMessage `json:"config"`
			Fresh  bool            `json:"fresh"`
		}
		if err := json.Unmarshal([]byte(stub.call(t, http.MethodPost, base+"/restart").Body), &sent); err != nil {
			t.Fatal(err)
		}
		return string(sent.Config), sent.Fresh
	}

	t.Run("with new settings", func(t *testing.T) {
		stub := newStubDaemon(t, map[string]stubResponse{
			"GET /api/projects":         {http.StatusOK, testSnapshotJSON},
			"POST " + base + "/restart": {http.StatusAccepted, `{"id":"sess-1"}`},
		})
		code, stdout, stderr := runClient(sessionRestart(), "-config", `{"enclave":{"hostConfig":true}}`, "-fresh", "sess-1")
		if code != exitOK {
			t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
		}
		if config, fresh := restartBody(t, stub); config != `{"enclave":{"hostConfig":true}}` || !fresh {
			t.Errorf("posted config %s fresh %v; want the given settings, fresh", config, fresh)
		}
		if !strings.Contains(stdout, "restarting") {
			t.Errorf("stdout = %q, want it to report the restart as in progress", stdout)
		}
	})

	t.Run("without -config it leaves the settings to the daemon", func(t *testing.T) {
		stub := newStubDaemon(t, map[string]stubResponse{
			"GET /api/projects":         {http.StatusOK, testSnapshotJSON},
			"POST " + base + "/restart": {http.StatusAccepted, `{"id":"sess-1"}`},
		})
		if code, _, stderr := runClient(sessionRestart(), "-fresh", "sess-1"); code != exitOK {
			t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
		}
		if config, fresh := restartBody(t, stub); config != "" || !fresh {
			t.Errorf("posted config %q fresh %v; want no config, fresh", config, fresh)
		}
		for _, call := range stub.Calls() {
			if strings.HasSuffix(call.Path, "/launch-config") {
				t.Errorf("the verb read %s; the daemon replays the recorded settings itself", call.Path)
			}
		}
	})

	t.Run("--json passes the restarting session through", func(t *testing.T) {
		newStubDaemon(t, map[string]stubResponse{
			"GET /api/projects":         {http.StatusOK, testSnapshotJSON},
			"POST " + base + "/restart": {http.StatusAccepted, `{"id":"sess-1","state":"starting"}`},
		})
		code, stdout, stderr := runClient(sessionRestart(), "-json", "sess-1")
		if code != exitOK {
			t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
		}
		if strings.TrimSpace(stdout) != `{"id":"sess-1","state":"starting"}` {
			t.Errorf("stdout = %q, want the payload untouched", stdout)
		}
	})

	for _, refusal := range []stubResponse{
		{http.StatusConflict, `{"error":"session is still starting; wait for it to come up"}`},
		{http.StatusBadRequest, `{"error":"configuration is invalid","fields":{"/model":"is not one of the models"}}`},
	} {
		t.Run(fmt.Sprintf("a %d refusal fails the command", refusal.status), func(t *testing.T) {
			newStubDaemon(t, map[string]stubResponse{
				"GET /api/projects":         {http.StatusOK, testSnapshotJSON},
				"POST " + base + "/restart": refusal,
			})
			code, _, stderr := runClient(sessionRestart(), "-config", `{"model":"nope"}`, "sess-1")
			if code != exitFailed {
				t.Errorf("exit code = %d, want %d", code, exitFailed)
			}
			if stderr == "" {
				t.Error("stderr is empty, want the daemon's reason")
			}
		})
	}

	t.Run("invalid -config is a usage error, before any request", func(t *testing.T) {
		stub := newStubDaemon(t, map[string]stubResponse{"GET /api/projects": {http.StatusOK, testSnapshotJSON}})
		if code, _, _ := runClient(sessionRestart(), "-config", "{nope", "sess-1"); code != exitUsage {
			t.Errorf("exit code = %d, want %d (usage)", code, exitUsage)
		}
		if calls := stub.Calls(); len(calls) != 0 {
			t.Errorf("the verb called the daemon %+v; want no request for a malformed flag", calls)
		}
	})
}
