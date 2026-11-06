package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/eclipsesource/homeshell/daemon/internal/daemonstate"
)

// --- shared harness for the data verbs ---

// apiCall records one request a verb made, so a test can assert the CLI stayed on
// the public API and sent what the endpoint expects.
type apiCall struct {
	Method string
	Path   string
	Query  string
	Body   string
	Token  string
}

// stubResponse is the canned answer for one route.
type stubResponse struct {
	status int
	body   string
}

// stubDaemon is an httptest server standing in for the daemon, keyed by
// "METHOD /path" so a test declares exactly the endpoints it expects to be called
// — an unexpected path answers 404 and shows up in the failure.
type stubDaemon struct {
	server *httptest.Server
	mu     sync.Mutex
	calls  []apiCall
}

// newStubDaemon starts the stub and publishes a discovery state file pointing at
// it, so a verb finds it through exactly the path it uses in production (state
// file → address + token → Bearer header).
func newStubDaemon(t *testing.T, routes map[string]stubResponse) *stubDaemon {
	t.Helper()
	isolateStateFile(t)

	stub := &stubDaemon{}
	stub.server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		stub.mu.Lock()
		stub.calls = append(stub.calls, apiCall{
			Method: request.Method,
			Path:   request.URL.Path,
			Query:  request.URL.RawQuery,
			Body:   string(body),
			Token:  request.Header.Get("Authorization"),
		})
		stub.mu.Unlock()

		answer, ok := routes[request.Method+" "+request.URL.Path]
		if !ok {
			http.Error(writer, `{"error":"no stub route"}`, http.StatusNotFound)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(answer.status)
		io.WriteString(writer, answer.body)
	}))
	t.Cleanup(stub.server.Close)

	addr := strings.TrimPrefix(stub.server.URL, "http://")
	if err := daemonstate.Write(daemonstate.State{
		Address: addr, PID: os.Getpid(), Version: testVersion, Token: "stub-token",
	}); err != nil {
		t.Fatal(err)
	}
	return stub
}

func (s *stubDaemon) Calls() []apiCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]apiCall(nil), s.calls...)
}

// call returns the recorded request for a method+path, failing the test when the
// verb never made it.
func (s *stubDaemon) call(t *testing.T, method, path string) apiCall {
	t.Helper()
	for _, recorded := range s.Calls() {
		if recorded.Method == method && recorded.Path == path {
			return recorded
		}
	}
	t.Fatalf("the verb never called %s %s; it called %+v", method, path, s.Calls())
	return apiCall{}
}

// runClient dispatches one data verb with the given arguments, as the group
// dispatcher would.
func runClient(command clientCommand, args ...string) (code int, stdout, stderr string) {
	return runClientWithStdin(command, "", args...)
}

func runClientWithStdin(command clientCommand, stdin string, args ...string) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	code = command.dispatch(Env{
		Version: testVersion,
		Stdin:   strings.NewReader(stdin),
		Stdout:  &out,
		Stderr:  &errOut,
	}, args)
	return code, out.String(), errOut.String()
}

// testSnapshotJSON is the GET /api/projects payload the verb tests read. Both
// projects hold a workstream named "feature", which is what makes an
// ambiguous-name lookup testable; "extra" appears only in beta.
const testSnapshotJSON = `{"projects":[` +
	`{"id":"proj-1","name":"alpha","path":"/tmp/alpha","gitBacked":true,"state":"running",` +
	`"runtimeState":"running","effectiveState":"running","workstreams":[` +
	`{"id":"ws-1","name":"feature","provider":"core","strategyId":"worktree","strategy":"worktree",` +
	`"workingDir":"/tmp/alpha/.ws/feature","state":"running","runtimeState":"running","effectiveState":"running",` +
	`"openModes":[{"id":"terminal","label":"Terminal"},{"id":"web-ide","label":"Web IDE"}],"materialized":true,` +
	`"sessions":[{"id":"sess-1","title":"Implement it","agent":"Claude Code","provider":"local","state":"running"}]}]},` +
	`{"id":"proj-2","name":"beta","path":"/tmp/beta","gitBacked":false,"state":"idle",` +
	`"runtimeState":"unknown","effectiveState":"idle","workstreams":[` +
	`{"id":"ws-2","name":"feature","provider":"core","strategyId":"copy","strategy":"copy",` +
	`"workingDir":"/tmp/beta/.ws/feature","state":"idle","runtimeState":"unknown","effectiveState":"idle",` +
	`"openModes":[],"materialized":true,"sessions":[]},` +
	`{"id":"ws-3","name":"extra","provider":"core","strategyId":"in-place","strategy":"in place",` +
	`"workingDir":"/tmp/beta","state":"idle","runtimeState":"unknown","effectiveState":"idle",` +
	`"openModes":[],"materialized":true,"sessions":[` +
	`{"id":"sess-2","title":"","agent":"Codex","provider":"local","state":"stopped"}]}]}]}`

// snapshotRoute is the route every verb that resolves a reference needs.
func snapshotRoute() map[string]stubResponse {
	return map[string]stubResponse{
		"GET /api/projects": {http.StatusOK, testSnapshotJSON},
	}
}

// --- the plumbing itself ---

func TestParseInterspersed(t *testing.T) {
	tests := []struct {
		name           string
		args           []string
		wantPositional []string
		wantMode       string
		wantErr        bool
	}{
		{
			name:           "flags before positionals",
			args:           []string{"-mode", "terminal", "ws-1"},
			wantPositional: []string{"ws-1"},
			wantMode:       "terminal",
		},
		{
			// Go's flag package stops at the first positional, so without the
			// repeated-parse loop this flag would be silently treated as text.
			name:           "flags after positionals",
			args:           []string{"ws-1", "-mode", "terminal"},
			wantPositional: []string{"ws-1"},
			wantMode:       "terminal",
		},
		{
			name:           "several positionals with a flag between them",
			args:           []string{"sess-1", "-mode", "x", "hello", "world"},
			wantPositional: []string{"sess-1", "hello", "world"},
			wantMode:       "x",
		},
		{
			name:           "a terminator passes dashed text through",
			args:           []string{"sess-1", "--", "-not-a-flag"},
			wantPositional: []string{"sess-1", "-not-a-flag"},
		},
		{
			// The case above is the only one the repeated-parse loop got right by
			// accident: it peels one positional per round, so a second dashed word
			// came back round as a flag. `homeshell session prompt s1 -- fix the -v
			// flag` exited 2 with "flag provided but not defined: -v" and the prompt
			// was never sent.
			name:           "a terminator protects every word after it, not just the first",
			args:           []string{"sess-1", "--", "fix", "the", "-v", "flag"},
			wantPositional: []string{"sess-1", "fix", "the", "-v", "flag"},
		},
		{
			// Worse than an error: a known flag after the terminator was silently
			// consumed as a flag, so the prompt was sent minus that word.
			name:           "a known flag after the terminator stays text",
			args:           []string{"sess-1", "--", "-v", "-mode", "x"},
			wantPositional: []string{"sess-1", "-v", "-mode", "x"},
		},
		{
			// Only the first terminator is one; the rest are text like anything else.
			name:           "a second terminator is passed through as text",
			args:           []string{"sess-1", "--", "a", "--", "b"},
			wantPositional: []string{"sess-1", "a", "--", "b"},
		},
		{
			// Flags before the terminator are still flags.
			name:           "flags before the terminator are still parsed",
			args:           []string{"sess-1", "-mode", "terminal", "--", "-v"},
			wantPositional: []string{"sess-1", "-v"},
			wantMode:       "terminal",
		},
		{
			name:    "an unknown flag is still an error",
			args:    []string{"ws-1", "-nope"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := flag.NewFlagSet("test", flag.ContinueOnError)
			fs.SetOutput(io.Discard)
			mode := fs.String("mode", "", "")
			positional, err := parseInterspersed(fs, tt.args)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got positional %q", positional)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if strings.Join(positional, ",") != strings.Join(tt.wantPositional, ",") {
				t.Errorf("positional = %q, want %q", positional, tt.wantPositional)
			}
			if *mode != tt.wantMode {
				t.Errorf("-mode = %q, want %q", *mode, tt.wantMode)
			}
		})
	}
}

func TestReportErrorExitCodes(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantCode   int
		wantStderr []string
	}{
		{
			name:       "a malformed invocation exits 2",
			err:        &usageError{message: "a project is required"},
			wantCode:   exitUsage,
			wantStderr: []string{"a project is required", "Usage: homeshell test"},
		},
		{
			name:       "an unreachable daemon exits 3 and says how to start one",
			err:        &unreachableError{message: "no daemon is running for /tmp/daemon.json"},
			wantCode:   exitNoDaemon,
			wantStderr: []string{"no daemon is running", `"homeshell serve"`, "--addr", "-home-dir"},
		},
		{
			name:       "a refused request exits 1 with the daemon's message",
			err:        newAPIError(http.StatusBadRequest, []byte(`{"error":"path does not exist"}`)),
			wantCode:   exitFailed,
			wantStderr: []string{"path does not exist"},
		},
		{
			name:       "field detail is printed under a validation failure",
			err:        newAPIError(http.StatusBadRequest, []byte(`{"error":"configuration is invalid","fields":{"branch":"is required","strategy":"unknown"}}`)),
			wantCode:   exitFailed,
			wantStderr: []string{"configuration is invalid", "branch: is required", "strategy: unknown"},
		},
		{
			name:       "a busy-session refusal names the sessions and the override",
			err:        newAPIError(http.StatusConflict, []byte(`{"error":"cannot remove","reason":"sessionsRunning","sessions":[{"id":"sess-1","title":"Implement it","state":"running"}]}`)),
			wantCode:   exitFailed,
			wantStderr: []string{"cannot remove", "Implement it (running)", "--force"},
		},
		{
			name:       "a 401 explains where the token comes from",
			err:        newAPIError(http.StatusUnauthorized, []byte(`{"error":"unauthorized"}`)),
			wantCode:   exitFailed,
			wantStderr: []string{"unauthorized", "HOMESHELL_API_TOKEN"},
		},
		{
			name:       "a body that is not the error shape still names the status",
			err:        newAPIError(http.StatusBadGateway, []byte("<html>nope</html>")),
			wantCode:   exitFailed,
			wantStderr: []string{"502"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stderr bytes.Buffer
			fs := flag.NewFlagSet("test", flag.ContinueOnError)
			fs.SetOutput(&stderr)
			fs.Usage = func() { io.WriteString(&stderr, "Usage: homeshell test\n") }

			code := reportError(Env{Stdout: io.Discard, Stderr: &stderr}, fs, tt.err)
			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d", code, tt.wantCode)
			}
			for _, want := range tt.wantStderr {
				if !strings.Contains(stderr.String(), want) {
					t.Errorf("stderr = %q, want it to contain %q", stderr.String(), want)
				}
			}
		})
	}
}

// A missing daemon must be exit 3 all the way through a real dispatch, not only
// in reportError: that code is the one thing a script branches on.
func TestDispatchWithNoDaemonExitsNoDaemon(t *testing.T) {
	isolateStateFile(t)

	code, _, stderr := runClient(projectList())
	if code != exitNoDaemon {
		t.Errorf("exit code = %d, want %d", code, exitNoDaemon)
	}
	if !strings.Contains(stderr, "no daemon is running") {
		t.Errorf("stderr = %q, want it to report no daemon", stderr)
	}
}

// The token lives in the 0600 state file; every data verb must present it, or a
// real daemon answers 401 and the CLI looks broken.
func TestDataVerbsPresentTheDiscoveredToken(t *testing.T) {
	stub := newStubDaemon(t, snapshotRoute())

	if code, _, stderr := runClient(projectList()); code != exitOK {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
	}
	if got := stub.call(t, http.MethodGet, "/api/projects").Token; got != "Bearer stub-token" {
		t.Errorf("Authorization = %q, want %q", got, "Bearer stub-token")
	}
}

func TestTokenForExplicitAddr(t *testing.T) {
	tests := []struct {
		name     string
		addr     string
		envToken string
		// fileToken, when set, is published in a local state file first.
		fileToken string
		want      string
	}{
		{"the environment wins", "127.0.0.1:8420", "  from-env  ", "from-file", "from-env"},
		{"the environment is sent to a remote host too", "build-box:8420", "from-env", "from-file", "from-env"},
		{"a loopback address falls back to the state file", "127.0.0.1:8420", "", "from-file", "from-file"},
		{"localhost falls back to the state file", "localhost:8420", "", "from-file", "from-file"},
		{"an IPv6 loopback falls back to the state file", "[::1]:8420", "", "from-file", "from-file"},
		{"a remote host never gets the local token", "build-box:8420", "", "from-file", ""},
		{"a remote IP never gets the local token", "192.168.1.20:8420", "", "from-file", ""},
		{"an unparseable address never gets the local token", "no-port", "", "from-file", ""},
		{"no token at all is not an error", "127.0.0.1:8420", "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolateStateFile(t)
			if tt.fileToken != "" {
				if err := daemonstate.Write(daemonstate.State{Address: "127.0.0.1:1", Token: tt.fileToken}); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("HOMESHELL_API_TOKEN", tt.envToken)
			if got := tokenForExplicitAddr(tt.addr); got != tt.want {
				t.Errorf("tokenForExplicitAddr(%q) = %q, want %q", tt.addr, got, tt.want)
			}
		})
	}
}

// A daemon that is up but slower than --timeout is not "no daemon": the verb must
// exit 1 and say to wait longer, not exit 3 and say to start one.
func TestTimeoutIsAFailureNotAMissingDaemon(t *testing.T) {
	newStubDaemon(t, snapshotRoute())

	code, _, stderr := runClient(projectList(), "--timeout", "1ns")
	if code != exitFailed {
		t.Errorf("exit code = %d, want %d (stderr: %q)", code, exitFailed, stderr)
	}
	if !strings.Contains(stderr, "--timeout") {
		t.Errorf("stderr = %q, want it to suggest a longer --timeout", stderr)
	}
	if strings.Contains(stderr, "homeshell serve") {
		t.Errorf("stderr = %q, want no advice to start a daemon that is running", stderr)
	}
}

// --addr must reach a daemon this process could not have discovered, which is the
// whole point of the override.
func TestAddrOverrideBypassesDiscovery(t *testing.T) {
	isolateStateFile(t)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		io.WriteString(writer, testSnapshotJSON)
	}))
	t.Cleanup(server.Close)

	code, stdout, stderr := runClient(projectList(), "--addr", strings.TrimPrefix(server.URL, "http://"))
	if code != exitOK {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
	}
	if !strings.Contains(stdout, "alpha") {
		t.Errorf("stdout = %q, want it to list the projects", stdout)
	}
}

func TestTableCell(t *testing.T) {
	tests := []struct{ name, value, want string }{
		{"a value passes through", "ws-1", "ws-1"},
		{"an empty value becomes a dash", "", "-"},
		{"whitespace becomes a dash", "   ", "-"},
		{"a tab cannot break the columns", "a\tb", "a b"},
		{"a newline cannot break the row", "a\nb", "a b"},
		{"a C1 control is shown, not sent to the terminal", "a\u009b2Kb", "a\ufffd2Kb"},
		{"a bidi override is shown, not applied", "\u202eevil", "\ufffdevil"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tableCell(tt.value); got != tt.want {
				t.Errorf("tableCell(%q) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}

// Titles and refusal details are daemon-relayed text; none of it may reach the
// terminal as a control sequence.
func TestMessagesShowControlCharactersInert(t *testing.T) {
	title := "fix\u009b1A\u202e"
	if got, want := sessionLabel(sessionEntry{Session: &apiSession{sessionFields: sessionFields{ID: "s-1", Title: title}}}), "fix\ufffd1A\ufffd"; got != want {
		t.Errorf("sessionLabel = %q, want %q", got, want)
	}
	var detail strings.Builder
	(&apiError{
		fields:   map[string]string{"name": "x\x1b[2J"},
		reason:   reasonSessionsRunning,
		sessions: []busySession{{ID: "s-1", Title: title, State: "running"}},
	}).writeDetail(&detail)
	if strings.ContainsAny(detail.String(), "\x1b\u009b\u202e") {
		t.Errorf("writeDetail wrote control characters: %q", detail.String())
	}
}

func TestWriteTableAlignsColumns(t *testing.T) {
	var out bytes.Buffer
	writeTable(&out, []string{"ID", "NAME"}, [][]string{{"a-long-id", "x"}, {"b", "yy"}})
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want a header and two rows:\n%s", len(lines), out.String())
	}
	// The NAME column must start at the same offset on every line, header included.
	offset := strings.Index(lines[0], "NAME")
	for _, line := range lines[1:] {
		if got := strings.Index(line, strings.Fields(line)[1]); got != offset {
			t.Errorf("second column of %q starts at %d, want %d", line, got, offset)
		}
	}
}

// --json output has to stay the daemon's bytes: a re-encoding would drop any
// field this CLI does not know about, which is the opposite of what a tooling
// contract needs.
func TestWriteRawListKeepsPayloadBytes(t *testing.T) {
	var out bytes.Buffer
	writeRawList(&out, []json.RawMessage{
		json.RawMessage(`{"id":"a","unknownToTheCLI":42}`),
		json.RawMessage(`{"id":"b"}`),
	})
	got := strings.TrimRight(out.String(), "\n")
	want := `[{"id":"a","unknownToTheCLI":42},{"id":"b"}]`
	if got != want {
		t.Errorf("output = %s, want %s", got, want)
	}
	if !json.Valid([]byte(got)) {
		t.Errorf("output is not valid JSON: %s", got)
	}
}

func TestWriteRawSkipsAnEmptyBody(t *testing.T) {
	var out bytes.Buffer
	writeRaw(&out, nil)
	writeRaw(&out, []byte("\n"))
	if out.Len() != 0 {
		t.Errorf("output = %q, want nothing for an empty body", out.String())
	}
}

func TestPositionalArgumentChecks(t *testing.T) {
	t.Run("one reports a missing argument", func(t *testing.T) {
		_, err := clientCall{}.one("a project")
		var malformed *usageError
		if !errors.As(err, &malformed) {
			t.Fatalf("error = %v, want a usageError", err)
		}
		if !strings.Contains(err.Error(), "a project") {
			t.Errorf("error = %q, want it to name what was missing", err)
		}
	})

	t.Run("one reports a surplus argument", func(t *testing.T) {
		_, err := clientCall{Args: []string{"a", "b"}}.one("a project")
		if err == nil || !strings.Contains(err.Error(), `"b"`) {
			t.Errorf("error = %v, want it to name the surplus argument", err)
		}
	})

	t.Run("none rejects any argument", func(t *testing.T) {
		if err := (clientCall{Args: []string{"oops"}}).none(); err == nil {
			t.Error("expected an error for an argument the verb does not take")
		}
		if err := (clientCall{}).none(); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})
}

// --- group dispatch ---

// groupCommands is a two-command stand-in for a real group, so these tests
// exercise the routing rather than any one verb's behavior. The recorded flag
// proves the subcommand — not the group — parsed the command line.
func groupCommands(ran *string, seen *[]string) []clientCommand {
	record := func(name string) clientCommand {
		return clientCommand{
			Verb:    "widget",
			Name:    name,
			Summary: "Summary of " + name,
			Run: func(call clientCall) error {
				*ran = name
				*seen = call.Args
				return nil
			},
		}
	}
	return []clientCommand{record("list"), record("add")}
}

func runGroup(commands []clientCommand, args ...string) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	code = dispatchGroup(Env{Version: testVersion, Stdout: &out, Stderr: &errOut},
		"widget", "Manage the widgets.", commands, args)
	return code, out.String(), errOut.String()
}

func TestDispatchGroup(t *testing.T) {
	t.Run("routes to the named subcommand with the rest of the line", func(t *testing.T) {
		newStubDaemon(t, snapshotRoute())
		var ran string
		var seen []string

		code, _, stderr := runGroup(groupCommands(&ran, &seen), "add", "/srv/tools")
		if code != exitOK {
			t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
		}
		if ran != "add" {
			t.Errorf("ran %q, want the add subcommand", ran)
		}
		if len(seen) != 1 || seen[0] != "/srv/tools" {
			t.Errorf("subcommand saw args %q, want the group name stripped", seen)
		}
	})

	t.Run("no subcommand prints the group usage on stdout", func(t *testing.T) {
		var ran string
		var seen []string

		code, stdout, _ := runGroup(groupCommands(&ran, &seen))
		if code != exitOK {
			t.Errorf("exit code = %d, want 0", code)
		}
		for _, want := range []string{"Usage: homeshell widget", "Manage the widgets.",
			"list", "Summary of list", "add", "Summary of add"} {
			if !strings.Contains(stdout, want) {
				t.Errorf("stdout = %q, want it to contain %q", stdout, want)
			}
		}
		if ran != "" {
			t.Errorf("ran %q, want no subcommand to run", ran)
		}
	})

	t.Run("help asks for the same usage on stdout", func(t *testing.T) {
		for _, arg := range []string{"-h", "--help", "help"} {
			var ran string
			var seen []string

			code, stdout, _ := runGroup(groupCommands(&ran, &seen), arg)
			if code != exitOK {
				t.Errorf("%s: exit code = %d, want 0", arg, code)
			}
			if !strings.Contains(stdout, "Usage: homeshell widget") {
				t.Errorf("%s: stdout = %q, want the group usage", arg, stdout)
			}
		}
	})

	t.Run("an unknown subcommand is a usage error naming it", func(t *testing.T) {
		var ran string
		var seen []string

		code, _, stderr := runGroup(groupCommands(&ran, &seen), "bogus")
		if code != exitUsage {
			t.Errorf("exit code = %d, want %d", code, exitUsage)
		}
		for _, want := range []string{"unknown subcommand", `"bogus"`, "Usage: homeshell widget"} {
			if !strings.Contains(stderr, want) {
				t.Errorf("stderr = %q, want it to contain %q", stderr, want)
			}
		}
		if ran != "" {
			t.Errorf("ran %q, want no subcommand to run", ran)
		}
	})

	t.Run("the registered groups reach their own subcommands", func(t *testing.T) {
		for _, group := range []struct {
			verb string
			run  func(env Env, args []string) int
			want string
		}{
			{"project", runProject, "list"},
			{"workstream", runWorkstream, "list"},
			{"session", runSession, "list"},
		} {
			var out, errOut bytes.Buffer
			env := Env{Version: testVersion, Stdout: &out, Stderr: &errOut}
			if code := group.run(env, nil); code != exitOK {
				t.Errorf("%s: exit code = %d, want 0", group.verb, code)
			}
			if !strings.Contains(out.String(), "Usage: homeshell "+group.verb) {
				t.Errorf("%s: stdout = %q, want its group usage", group.verb, out.String())
			}
			if !strings.Contains(out.String(), group.want) {
				t.Errorf("%s: stdout = %q, want it to list the %q subcommand",
					group.verb, out.String(), group.want)
			}
		}
	})
}

// A verb that reads a pipe must not panic on an Env whose Stdin was never set —
// it should see an empty pipe and fall through to its own usage error.
func TestEnvStdinDefaultsToAnEmptyReader(t *testing.T) {
	piped, err := io.ReadAll(Env{}.stdin())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(piped) != 0 {
		t.Errorf("read %q, want nothing", piped)
	}

	newStubDaemon(t, snapshotRoute())
	var out, errOut bytes.Buffer
	code := sessionPrompt().dispatch(Env{Version: testVersion, Stdout: &out, Stderr: &errOut}, []string{"sess-1"})
	if code != exitUsage {
		t.Errorf("exit code = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(errOut.String(), "prompt text") {
		t.Errorf("stderr = %q, want it to name the missing prompt text", errOut.String())
	}
}

// TestSubcommandAliases pins the alias mechanism: a destructive verb answers to
// both spellings, and the group listing shows the alias so it is discoverable
// rather than something a caller has to already know.
func TestSubcommandAliases(t *testing.T) {
	t.Run("remove and delete reach the same subcommand in every group", func(t *testing.T) {
		groups := map[string][]clientCommand{
			"project":    projectSubcommands(),
			"workstream": workstreamSubcommands(),
			"session":    sessionSubcommands(),
		}
		for verb, commands := range groups {
			t.Run(verb, func(t *testing.T) {
				var removal *clientCommand
				for i := range commands {
					if commands[i].Name == "remove" {
						removal = &commands[i]
					}
					if commands[i].Name == "delete" {
						t.Errorf("%s uses delete as the canonical name; remove is the one shown in help", verb)
					}
				}
				if removal == nil {
					t.Fatalf("%s has no remove subcommand", verb)
				}
				if !removal.answersTo("delete") {
					t.Errorf("%s remove does not answer to delete", verb)
				}
			})
		}
	})

	t.Run("the group listing advertises the alias", func(t *testing.T) {
		var buf bytes.Buffer
		writeGroupUsage(&buf, "project", "Manage projects.", projectSubcommands())
		if !strings.Contains(buf.String(), "remove (delete)") {
			t.Errorf("group usage = %q, want it to show the delete alias", buf.String())
		}
	})

	t.Run("an unknown subcommand is still a usage error", func(t *testing.T) {
		var out, errOut bytes.Buffer
		env := Env{Version: testVersion, Stdout: &out, Stderr: &errOut}
		if code := dispatchGroup(env, "project", "Manage projects.", projectSubcommands(), []string{"destroy"}); code != exitUsage {
			t.Errorf("exit code = %d, want %d", code, exitUsage)
		}
		if !strings.Contains(errOut.String(), `unknown subcommand "destroy"`) {
			t.Errorf("stderr = %q, want it to name the unknown subcommand", errOut.String())
		}
	})
}

func TestClientCallTwo(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"both arguments given", []string{"a", "b"}, ""},
		{"no arguments names the first", nil, "a key is required"},
		{"one argument names the second", []string{"a"}, "a value is required"},
		{"three arguments reject the surplus", []string{"a", "b", "c"}, `unexpected argument "c"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			first, second, err := clientCall{Args: tt.args}.two("a key", "a value")
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("two() returned %v, want no error", err)
				}
				if first != "a" || second != "b" {
					t.Errorf("two() = %q, %q, want a, b", first, second)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("two() error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}
