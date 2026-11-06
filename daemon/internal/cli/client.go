package cli

// Shared plumbing for the *data* verbs — `project`, `workstream`, `session`,
// `events`, `version` — as opposed to the lifecycle verbs (`status`, `stop`,
// `ensure-running`), which start from discovery instead. It holds the flags every
// data verb accepts, resolves the daemon they talk to, turns an HTTP answer into
// an exit code, and renders tables.
//
// Like every client verb these stay on the public contract — the discovery state
// file plus the HTTP API (docs/ARCHITECTURE.md, "CLI and the client boundary") —
// so nothing here reaches into the daemon's domain packages; the wire shapes are
// mirrored locally in snapshot.go.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"

	"github.com/eclipsesource/homeshell/daemon/internal/apiclient"
	"github.com/eclipsesource/homeshell/daemon/internal/daemonstate"
	"github.com/eclipsesource/homeshell/daemon/internal/persist"
)

// The exit codes the data verbs return. Scripts branch on these, so they are part
// of the CLI's contract — do not reshuffle them.
//
// exitNoDaemon exists because "there is nothing to talk to" is the one failure a
// script must distinguish: it means start a daemon and retry, whereas exitFailed
// means the daemon answered and refused. The lifecycle verbs predate this table
// and keep their own code 1 for a missing daemon, because for `status` a non-zero
// exit *is* the report rather than an error.
const (
	exitOK       = 0 // the command succeeded
	exitFailed   = 1 // the daemon answered and the request failed
	exitUsage    = 2 // the invocation is malformed (unknown command, flag or argument)
	exitNoDaemon = 3 // no daemon could be reached
)

// defaultClientTimeout bounds a single API call. It is generous because the
// requests behind these verbs are not all cheap: GET /api/version performs a
// release check, and removing a workstream tears its sessions down first.
const defaultClientTimeout = 30 * time.Second

// clientOptions are the flags every data verb accepts. They are registered from
// one place so `--json`, `--addr`, `-home-dir` and `--timeout` mean the same
// thing on every command.
type clientOptions struct {
	homeDir string
	addr    string
	asJSON  bool
	timeout time.Duration
}

func (o *clientOptions) register(fs *flag.FlagSet) {
	fs.StringVar(&o.homeDir, "home-dir", "", "data directory (must match the running daemon's -home-dir)")
	fs.StringVar(&o.addr, "addr", "", "daemon address as host:port, bypassing state-file discovery")
	fs.BoolVar(&o.asJSON, "json", false, "print the daemon's JSON payload instead of a table")
	fs.DurationVar(&o.timeout, "timeout", defaultClientTimeout, "how long to wait for the daemon to answer")
}

// connect resolves the daemon this invocation talks to. Reading the discovery
// state file is the normal path (a client finds the daemon the same way the
// lifecycle verbs do); --addr names one directly, for a port-forward or a daemon
// whose state file this process cannot read.
func (o *clientOptions) connect() (*apiclient.Client, error) {
	if o.homeDir != "" {
		persist.SetHomeDir(o.homeDir)
	}
	if o.addr != "" {
		return apiclient.New(o.addr, tokenForExplicitAddr(o.addr)), nil
	}
	client, _, err := apiclient.Discover()
	if err != nil {
		return nil, &unreachableError{message: fmt.Sprintf("no daemon is running for %s", statePath())}
	}
	return client, nil
}

// tokenForExplicitAddr resolves the API token for an --addr invocation, where
// discovery did not supply one. HOMESHELL_API_TOKEN comes first because it is
// what pins the daemon's own token, so the two agree by construction.
//
// The local state file is a fallback **only for a loopback address**, which
// covers pointing --addr at a port-forward of the daemon running on this
// machine. Any other host gets no token rather than this machine's: sending the
// local daemon's secret to whatever --addr names would hand it to a daemon that
// is not the one it belongs to. That host then answers 401, and reportError
// says to set HOMESHELL_API_TOKEN.
func tokenForExplicitAddr(addr string) string {
	if token := strings.TrimSpace(os.Getenv("HOMESHELL_API_TOKEN")); token != "" {
		return token
	}
	if !isLoopbackAddr(addr) {
		return ""
	}
	if st, err := daemonstate.Read(); err == nil {
		return st.Token
	}
	return ""
}

// isLoopbackAddr reports whether a host:port names this machine: a loopback IP,
// or the name localhost. An address it cannot parse is not loopback, so the
// doubt falls on the side of not sending the local token.
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// unreachableError is the "there is no daemon to talk to" failure, whether the
// state file was missing or the recorded daemon did not answer. It carries the
// whole message because only the failing site knows what it tried to reach;
// reportError adds the shared advice and picks the exit code.
type unreachableError struct{ message string }

func (e *unreachableError) Error() string { return e.message }

// usageError is a malformed invocation the flag package cannot catch — a missing
// or surplus positional argument. It exits 2 like an unknown flag rather than 1,
// so a script can tell "I called this wrong" from "the daemon said no".
type usageError struct{ message string }

func (e *usageError) Error() string { return e.message }

func missingArgError(what string) error {
	return &usageError{message: fmt.Sprintf("%s is required", what)}
}

func surplusArgsError(args []string) error {
	return &usageError{message: fmt.Sprintf("unexpected argument %q", args[0])}
}

// apiError is a request the daemon answered and refused. It keeps the daemon's
// own message and the structured detail the API attaches to a validation failure
// or a running-sessions refusal, so the CLI reports what the UI would show
// instead of inventing its own wording.
type apiError struct {
	status   int
	message  string
	fields   map[string]string
	reason   string
	sessions []busySession
}

// busySession mirrors one entry of the `sessions` array on the 409 a workstream
// removal gets while its sessions are running.
type busySession struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	State string `json:"state"`
}

// reasonSessionsRunning is the machine-readable `reason` the daemon puts on that
// refusal. Branching on it rather than on the human message is the contract.
const reasonSessionsRunning = "sessionsRunning"

func (e *apiError) Error() string {
	if e.message != "" {
		return e.message
	}
	return fmt.Sprintf("the daemon answered %d %s", e.status, http.StatusText(e.status))
}

// writeDetail prints the structured part of a refusal below the message: which
// fields were invalid, or which sessions are in the way and how to override.
func (e *apiError) writeDetail(w io.Writer) {
	for _, field := range slices.Sorted(maps.Keys(e.fields)) {
		fmt.Fprintf(w, "  %s: %s\n", inert(field), inert(e.fields[field]))
	}
	if e.reason == reasonSessionsRunning {
		for _, session := range e.sessions {
			name := session.Title
			if strings.TrimSpace(name) == "" {
				name = session.ID
			}
			fmt.Fprintf(w, "  %s (%s)\n", inert(name), inert(session.State))
		}
		fmt.Fprintln(w, `homeshell: retry with --force to stop them and remove anyway`)
	}
	if e.status == http.StatusUnauthorized {
		fmt.Fprintln(w, "homeshell: the daemon rejected the API token; with --addr, set HOMESHELL_API_TOKEN to the daemon's token")
	}
}

// newAPIError parses a refusal body into an apiError. A body that is not the
// daemon's JSON error shape (or is empty) still yields a usable error naming the
// status, because a client must not depend on the shape of a failure.
func newAPIError(status int, body []byte) *apiError {
	failure := &apiError{status: status}
	var payload struct {
		Error    string            `json:"error"`
		Fields   map[string]string `json:"fields"`
		Reason   string            `json:"reason"`
		Sessions []busySession     `json:"sessions"`
	}
	if err := json.Unmarshal(body, &payload); err == nil {
		failure.message = payload.Error
		failure.fields = payload.Fields
		failure.reason = payload.Reason
		failure.sessions = payload.Sessions
	}
	return failure
}

// requestJSON issues one authenticated API call and returns the response body. A
// transport failure becomes an unreachableError (exit 3) and a non-2xx answer an
// *apiError (exit 1), so every verb reports both the same way.
func requestJSON(ctx context.Context, client *apiclient.Client, method, path string, payload any) ([]byte, error) {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("encoding the request for %s: %w", path, err)
		}
		body = bytes.NewReader(encoded)
	}
	res, err := client.Do(ctx, method, path, body)
	if err != nil {
		return nil, transportError(ctx, client, err)
	}
	defer res.Body.Close()
	answer, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("reading the response to %s %s: %w", method, path, err)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, newAPIError(res.StatusCode, answer)
	}
	return answer, nil
}

// transportError classifies a request that got no answer. Running out of the
// --timeout budget is checked first: the daemon may be up and merely slow (a
// workstream removal tearing its sessions down), so it is a failed request
// (exit 1) with a hint to wait longer, not "no daemon" (exit 3) with advice to
// start one that is already running.
func transportError(ctx context.Context, client *apiclient.Client, err error) error {
	if ctx.Err() != nil {
		return &timeoutError{address: client.BaseURL, cause: ctx.Err()}
	}
	return &unreachableError{message: fmt.Sprintf("cannot reach the daemon at %s: %v", client.BaseURL, err)}
}

// timeoutError is a request the daemon did not answer within --timeout.
type timeoutError struct {
	address string
	cause   error
}

func (e *timeoutError) Error() string {
	return fmt.Sprintf("the daemon at %s did not answer in time (%v); retry with a longer --timeout", e.address, e.cause)
}

func (e *timeoutError) Unwrap() error { return e.cause }

// clientCall is what a data verb's body receives once the shared plumbing has
// parsed its flags and resolved the daemon.
type clientCall struct {
	Ctx     context.Context
	Client  *apiclient.Client
	Options *clientOptions
	Args    []string // the positional arguments left after flag parsing
	Env     Env
}

// one returns the command's single positional argument, or a usage error naming
// what was missing or surplus.
func (c clientCall) one(what string) (string, error) {
	if len(c.Args) == 0 {
		return "", missingArgError(what)
	}
	if len(c.Args) > 1 {
		return "", surplusArgsError(c.Args[1:])
	}
	return c.Args[0], nil
}

// two returns the command's two positional arguments — the shape of every
// rename (`<thing> <new name>`) and of `settings set <key> <value>`. Each is
// named separately so a missing one says which is missing.
func (c clientCall) two(first, second string) (string, string, error) {
	switch {
	case len(c.Args) == 0:
		return "", "", missingArgError(first)
	case len(c.Args) == 1:
		return "", "", missingArgError(second)
	case len(c.Args) > 2:
		return "", "", surplusArgsError(c.Args[2:])
	}
	return c.Args[0], c.Args[1], nil
}

// none asserts that the command takes no positional arguments.
func (c clientCall) none() error {
	if len(c.Args) > 0 {
		return surplusArgsError(c.Args)
	}
	return nil
}

// decodePayload reads the fields a verb needs out of a response body. A decode
// failure is the daemon's answer not matching the contract, so it says so rather
// than surfacing as a nil-ish success.
func decodePayload(payload []byte, into any) error {
	if err := json.Unmarshal(payload, into); err != nil {
		return fmt.Errorf("decoding the daemon's response: %w", err)
	}
	return nil
}

// clientCommand describes one data verb. Verb is the group it belongs to
// ("project"), empty for a top-level verb; Args documents the positional
// arguments; Setup registers the command's own flags on top of the shared ones.
//
// Aliases are additional names the subcommand answers to. They exist for the
// destructive verbs, where `remove` is the name the UI uses and `delete` is the
// name the HTTP method suggests: a caller reaching for either should not have to
// discover which one this CLI picked. The group listing shows them in
// parentheses — an alias nobody can see is one everybody still has to guess at —
// while Name stays the single spelling used in messages.
type clientCommand struct {
	Verb    string
	Name    string
	Aliases []string
	Args    string
	Summary string
	Setup   func(fs *flag.FlagSet)
	Run     func(call clientCall) error
}

// listing is how the subcommand appears in its group's usage: its name, plus any
// aliases in parentheses.
func (c clientCommand) listing() string {
	if len(c.Aliases) == 0 {
		return c.Name
	}
	return c.Name + " (" + strings.Join(c.Aliases, ", ") + ")"
}

// answersTo reports whether the command is being invoked under its own name or
// one of its aliases.
func (c clientCommand) answersTo(name string) bool {
	if c.Name == name {
		return true
	}
	for _, alias := range c.Aliases {
		if alias == name {
			return true
		}
	}
	return false
}

// fullName is how the command is spelled on the command line, used in usage and
// error messages.
func (c clientCommand) fullName() string {
	if c.Verb == "" {
		return c.Name
	}
	return c.Verb + " " + c.Name
}

// dispatch is the command's entry point: parse, connect, run, and map whatever
// comes back onto an exit code.
func (c clientCommand) dispatch(env Env, args []string) int {
	fs := flag.NewFlagSet(c.fullName(), flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	fs.Usage = func() {
		fmt.Fprintf(env.Stderr, "Usage: homeshell %s\n\n%s\n\nFlags:\n", c.usageLine(), c.Summary)
		fs.PrintDefaults()
	}
	var options clientOptions
	options.register(fs)
	if c.Setup != nil {
		c.Setup(fs)
	}
	positional, err := parseInterspersed(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}

	client, err := options.connect()
	if err != nil {
		return reportError(env, fs, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), options.timeout)
	defer cancel()

	if err := c.Run(clientCall{Ctx: ctx, Client: client, Options: &options, Args: positional, Env: env}); err != nil {
		return reportError(env, fs, err)
	}
	return exitOK
}

// dispatchGroup routes `homeshell <verb> <subcommand>` to one of a group's
// commands. A group is a namespace and nothing more: it owns no flags, so an
// invocation that names no subcommand prints the group's usage instead of doing
// anything, and every flag is parsed by the subcommand that runs. The exit codes
// mirror top-level Dispatch — usage on request succeeds, an unknown name is a
// malformed invocation.
func dispatchGroup(env Env, verb, summary string, commands []clientCommand, args []string) int {
	if len(args) == 0 {
		writeGroupUsage(env.Stdout, verb, summary, commands)
		return exitOK
	}
	switch args[0] {
	case "-h", "--help", "help":
		writeGroupUsage(env.Stdout, verb, summary, commands)
		return exitOK
	}
	for _, command := range commands {
		if command.answersTo(args[0]) {
			return command.dispatch(env, args[1:])
		}
	}
	fmt.Fprintf(env.Stderr, "homeshell: unknown subcommand %q\n\n", args[0])
	writeGroupUsage(env.Stderr, verb, summary, commands)
	return exitUsage
}

// writeGroupUsage lists a group's subcommands. It keeps the order the group
// declares them in — read-only first, then the mutations — rather than sorting,
// because that order is what makes the group legible at a glance.
func writeGroupUsage(w io.Writer, verb, summary string, commands []clientCommand) {
	fmt.Fprintf(w, "Usage: homeshell %s <command> [flags]\n\n%s\n\nCommands:\n", verb, summary)
	width := 0
	for _, command := range commands {
		if len(command.listing()) > width {
			width = len(command.listing())
		}
	}
	for _, command := range commands {
		fmt.Fprintf(w, "  %-*s  %s\n", width, command.listing(), command.Summary)
	}
	fmt.Fprintf(w, "\nRun %q for command-specific flags.\n", "homeshell "+verb+" <command> -h")
}

// usageLine renders "project remove <project> [flags]" for the help text.
func (c clientCommand) usageLine() string {
	line := c.fullName()
	if c.Args != "" {
		line += " " + c.Args
	}
	return line + " [flags]"
}

// parseInterspersed parses args allowing flags to appear *after* positional
// arguments, which Go's flag package otherwise stops at — so both
// `session prompt s1 --json` and `session prompt --json s1` work. It parses
// repeatedly, peeling off one positional each round.
//
// Everything after a `--` terminator is taken verbatim, so
// `prompt s1 -- fix the -v flag` reaches the verb as its five words rather than
// as an unknown flag. The terminator is split off here rather than left to
// flag.Parse — which honors one perfectly well — because this function parses
// *repeatedly*: Parse consumes the `--`, so every later round would re-parse the
// very words it was there to protect, and only the first of them would survive.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var verbatim []string
	if i := slices.Index(args, "--"); i >= 0 {
		args, verbatim = args[:i], args[i+1:]
	}
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return append(positional, verbatim...), nil
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
}

// reportError prints err and returns the exit code it maps to. It is the single
// place the code table is applied, so a new verb cannot invent its own.
func reportError(env Env, fs *flag.FlagSet, err error) int {
	var malformed *usageError
	if errors.As(err, &malformed) {
		fmt.Fprintf(env.Stderr, "homeshell: %v\n\n", malformed)
		if fs != nil {
			fs.Usage()
		}
		return exitUsage
	}

	fmt.Fprintf(env.Stderr, "homeshell: %v\n", err)

	var down *unreachableError
	if errors.As(err, &down) {
		fmt.Fprintln(env.Stderr, `homeshell: start a daemon with "homeshell serve", or name a running one with --addr host:port`)
		fmt.Fprintln(env.Stderr, "homeshell: a daemon started with a different -home-dir publishes its own state file; pass that -home-dir")
		return exitNoDaemon
	}
	var failure *apiError
	if errors.As(err, &failure) {
		failure.writeDetail(env.Stderr)
	}
	return exitFailed
}

// --- output ---

// writeRaw prints a payload exactly as the daemon sent it, so `--json` output is
// the API contract rather than this CLI's re-encoding of it — including fields
// this CLI does not know about. An empty body prints nothing.
func writeRaw(w io.Writer, payload []byte) {
	trimmed := bytes.TrimRight(payload, "\n")
	if len(trimmed) == 0 {
		return
	}
	w.Write(trimmed)
	io.WriteString(w, "\n")
}

// writeRawList prints a JSON array of raw objects, for a list verb that filters
// the daemon's snapshot: each element is byte-identical to what the daemon sent.
func writeRawList(w io.Writer, objects []json.RawMessage) {
	w.Write(append(rawArray(objects), '\n'))
}

// writeRawEnvelope prints raw objects inside the one-key object a list endpoint
// answers with (`{"entries": [...]}`), for a verb that filters such a listing and
// must keep its shape.
func writeRawEnvelope(w io.Writer, key string, objects []json.RawMessage) {
	name, _ := json.Marshal(key)
	var out bytes.Buffer
	out.WriteByte('{')
	out.Write(name)
	out.WriteByte(':')
	out.Write(rawArray(objects))
	out.WriteString("}\n")
	w.Write(out.Bytes())
}

// rawArray joins raw objects into a JSON array without re-encoding any of them.
func rawArray(objects []json.RawMessage) []byte {
	var out bytes.Buffer
	out.WriteByte('[')
	for i, object := range objects {
		if i > 0 {
			out.WriteByte(',')
		}
		out.Write(bytes.TrimSpace(object))
	}
	out.WriteByte(']')
	return out.Bytes()
}

// writeTable prints rows under header as space-aligned columns. Widths come from
// the content so the output stays compact; it is for humans — scripts pass --json
// rather than parsing this.
func writeTable(w io.Writer, header []string, rows [][]string) {
	table := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, strings.Join(header, "\t"))
	for _, row := range rows {
		cells := make([]string, len(row))
		for i, cell := range row {
			cells[i] = tableCell(cell)
		}
		fmt.Fprintln(table, strings.Join(cells, "\t"))
	}
	table.Flush()
}

// tableCell renders one cell. A dash stands in for an empty value so a sparse row
// still lines up with its header, and embedded tabs or newlines (a session title
// is provider-supplied text) are flattened so they cannot break the layout.
func tableCell(value string) string {
	value = inert(strings.NewReplacer("\t", " ", "\r", " ", "\n", " ").Replace(value))
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}

// inert replaces control and bidi-control runes with U+FFFD, so daemon-relayed
// text (agent titles above all) cannot drive the user's terminal.
func inert(text string) string {
	return strings.Map(func(char rune) rune {
		if unicode.IsControl(char) || unicode.Is(unicode.Bidi_Control, char) {
			return unicode.ReplacementChar
		}
		return char
	}, text)
}
