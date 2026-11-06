package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/agentcatalog"
	"github.com/eclipsesource/homeshell/daemon/internal/daemonstate"
	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/logging"
	"github.com/eclipsesource/homeshell/daemon/internal/persist"
)

func TestParseServeArgs(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantAddr    string
		wantDemo    bool
		wantLevel   string
		wantHomeDir string
		wantErr     bool
	}{
		{name: "defaults", args: nil, wantAddr: "127.0.0.1:8420", wantDemo: false},
		{name: "custom addr", args: []string{"-addr", ":9000"}, wantAddr: ":9000", wantDemo: false},
		{name: "demo enabled", args: []string{"-demo=true"}, wantAddr: "127.0.0.1:8420", wantDemo: true},
		{name: "demo disabled", args: []string{"-demo=false"}, wantAddr: "127.0.0.1:8420", wantDemo: false},
		{name: "log level", args: []string{"-log-level=debug"}, wantAddr: "127.0.0.1:8420", wantLevel: "debug"},
		{name: "custom home-dir", args: []string{"-home-dir", "/tmp/myhome"}, wantAddr: "127.0.0.1:8420", wantHomeDir: "/tmp/myhome"},
		{name: "unknown flag", args: []string{"-nope"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stderr bytes.Buffer
			flags, err := parseServeArgs(tt.args, &stderr)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected an error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if flags.addr != tt.wantAddr {
				t.Errorf("addr = %q, want %q", flags.addr, tt.wantAddr)
			}
			if flags.demo != tt.wantDemo {
				t.Errorf("demo = %v, want %v", flags.demo, tt.wantDemo)
			}
			if flags.homeDir != tt.wantHomeDir {
				t.Errorf("homeDir = %q, want %q", flags.homeDir, tt.wantHomeDir)
			}
			if flags.logLevel != tt.wantLevel {
				t.Errorf("level = %q, want %q", flags.logLevel, tt.wantLevel)
			}
		})
	}
}

func TestParseServeArgsAllowPublic(t *testing.T) {
	var stderr bytes.Buffer
	flags, err := parseServeArgs([]string{"-allow-public"}, &stderr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !flags.allowPublic {
		t.Error("allowPublic = false, want true when --allow-public is passed")
	}

	defaults, err := parseServeArgs(nil, &stderr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if defaults.allowPublic {
		t.Error("allowPublic = true by default, want false")
	}
}

func TestClassifyBind(t *testing.T) {
	tests := []struct {
		name         string
		addr         string
		wantLoopback bool
		wantErr      bool
	}{
		{"loopback ipv4 with port", "127.0.0.1:8420", true, false},
		{"loopback ipv6 with port", "[::1]:8420", true, false},
		{"localhost with port", "localhost:8420", true, false},
		{"unspecified ipv4", "0.0.0.0:8420", false, false},
		{"bare port", ":9000", false, false},
		// No port at all: the whole value is the host, not an unparseable address.
		{"localhost without a port", "localhost", true, false},
		{"loopback ipv4 without a port", "127.0.0.1", true, false},
		{"lan address", "192.168.1.5:8420", false, false},
		// RFC 2606 reserved TLD: guaranteed not to resolve, so the test is
		// deterministic and offline. An unresolvable host is unclassifiable
		// (error), not "public".
		{"unresolvable host", "nonexistent.invalid:8420", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotLoopback, err := classifyBind(tt.addr)
			if gotLoopback != tt.wantLoopback {
				t.Errorf("classifyBind(%q) loopback = %v, want %v", tt.addr, gotLoopback, tt.wantLoopback)
			}
			if (err != nil) != tt.wantErr {
				t.Errorf("classifyBind(%q) err = %v, wantErr %v", tt.addr, err, tt.wantErr)
			}
		})
	}
}

func TestServeRefusesNonLoopbackWithoutFlag(t *testing.T) {
	isolateStateFile(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	env := Env{Version: testVersion, Stdout: io.Discard, Stderr: io.Discard}
	// A non-loopback bind without --allow-public must be refused before any listen.
	err := run(ctx, env, serveFlags{addr: "192.168.1.5:0"}, logging.NewLevel(slog.LevelInfo), logging.NewHub(), false)
	if err == nil {
		t.Fatal("expected an error binding a non-loopback address without --allow-public")
	}
	if !strings.Contains(err.Error(), "allow-public") {
		t.Errorf("error = %v, want it to mention --allow-public", err)
	}
	if !strings.Contains(err.Error(), "non-loopback address") {
		t.Errorf("error = %v, want the non-loopback wording for a resolvable LAN address", err)
	}
}

// TestServeIgnoresAllowPublicOnLoopbackBind verifies that --allow-public only takes
// effect when the bind really is network-reachable. Honoring the flag on a loopback
// bind would relax the Host guard and withhold the token cookie, leaving the
// daemon's own served UI permanently "offline" (no cookie, so every /api call 401s
// and a browser cannot supply a Bearer header) for no gain in exposure.
func TestServeIgnoresAllowPublicOnLoopbackBind(t *testing.T) {
	isolateStateFile(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	logs := captureDaemonLogs(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	env := Env{Version: testVersion, Stdout: io.Discard, Stderr: io.Discard}

	errCh := make(chan error, 1)
	go func() {
		errCh <- run(ctx, env, serveFlags{addr: "127.0.0.1:0", allowPublic: true},
			logging.NewLevel(slog.LevelInfo), logging.NewHub(), false)
	}()

	var address, token string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if st, _, err := discover(ctx); err == nil && st.Address != "" {
			address, token = st.Address, st.Token
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if address == "" {
		t.Fatal("daemon did not publish a state file")
	}
	t.Cleanup(func() {
		cancel()
		<-errCh
	})

	// The document bootstrap must still fire: that is what --allow-public would
	// have silently switched off.
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Accept", "text/html")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer response.Body.Close()
	if got := response.Header.Get("Set-Cookie"); !strings.Contains(got, "hs_api_token="+token) {
		t.Errorf("Set-Cookie = %q, want it to carry the token: --allow-public must not disable the bootstrap on a loopback bind", got)
	}

	// And the strict Host allow-list must still apply, rather than being relaxed to
	// the same-origin check a real public bind uses.
	rebound, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+"/api/hello", nil)
	if err != nil {
		t.Fatal(err)
	}
	rebound.Host = "evil.com"
	reboundResponse, err := http.DefaultClient.Do(rebound)
	if err != nil {
		t.Fatalf("GET /api/hello: %v", err)
	}
	defer reboundResponse.Body.Close()
	if reboundResponse.StatusCode != http.StatusForbidden {
		t.Errorf("rebound Host status = %d, want %d: the Host guard must stay strict on a loopback bind", reboundResponse.StatusCode, http.StatusForbidden)
	}

	// The operator gets told, too: an ignored flag is the only thing this branch
	// produces, so without this the whole `case allowPublic:` could be deleted.
	if !strings.Contains(logs.String(), "ignoring --allow-public") {
		t.Errorf("logs = %q, want a warning that --allow-public was ignored", logs.String())
	}
}

// stubAddrListener is a listener that reports a fixed address, so
// listenerIsLoopback can be driven with addresses net.Listen("tcp", ...) will not
// produce here: a non-loopback bind (which would open a network-reachable socket in
// a unit test) and a non-*net.TCPAddr (which a tcp listener never returns).
type stubAddrListener struct{ addr net.Addr }

func (stubAddrListener) Accept() (net.Conn, error) { return nil, errors.New("unused") }
func (stubAddrListener) Close() error              { return nil }
func (l stubAddrListener) Addr() net.Addr          { return l.addr }

// TestListenerIsLoopback pins the input to the publicBind decision. It is derived
// from the socket rather than from classifyBind because the two answer different
// questions — classifyBind requires *every* resolved address to be loopback while
// net.Listen binds exactly one, so a name with A=127.0.0.1 and a global AAAA
// classifies public and binds loopback. Getting this wrong in
// either direction is silent: too strict leaves the served UI permanently 401ing,
// too lax relaxes the Host guard on a reachable socket.
func TestListenerIsLoopback(t *testing.T) {
	tests := []struct {
		name         string
		listener     net.Listener
		wantAddr     string
		wantLoopback bool
	}{
		{"ipv4 loopback socket", mustListen(t, "127.0.0.1:0"), "", true},
		{"ipv6 loopback socket", mustListen(t, "[::1]:0"), "", true},
		{"lan address", stubAddrListener{addr: &net.TCPAddr{IP: net.IPv4(192, 168, 1, 5), Port: 8420}}, "192.168.1.5:8420", false},
		{"unspecified address", stubAddrListener{addr: &net.TCPAddr{IP: net.IPv4zero, Port: 8420}}, "0.0.0.0:8420", false},
		// Fails closed rather than defaulting to loopback: an unexpected address
		// shape must not silently relax the guard.
		{"non-tcp address", stubAddrListener{addr: &net.UnixAddr{Name: "/tmp/homeshell.sock", Net: "unix"}}, "/tmp/homeshell.sock", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr, isLoopback := listenerIsLoopback(tt.listener)
			if isLoopback != tt.wantLoopback {
				t.Errorf("listenerIsLoopback(%s) loopback = %v, want %v", tt.listener.Addr(), isLoopback, tt.wantLoopback)
			}
			if tt.wantAddr != "" && addr != tt.wantAddr {
				t.Errorf("listenerIsLoopback(%s) addr = %q, want %q", tt.listener.Addr(), addr, tt.wantAddr)
			}
			if tt.wantAddr == "" && addr != tt.listener.Addr().String() {
				t.Errorf("addr = %q, want the bound address %q", addr, tt.listener.Addr())
			}
		})
	}
}

func mustListen(t *testing.T, addr string) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("net.Listen(%q): %v", addr, err)
	}
	t.Cleanup(func() { listener.Close() })
	return listener
}

// publicAddrListener is a real (loopback) listener that reports a network-reachable
// address. It lets a test drive the daemon's public-bind branches — which read
// listener.Addr() — while the socket the daemon actually serves on stays loopback, so
// the test opens nothing reachable from the network.
type publicAddrListener struct {
	net.Listener
	addr net.Addr
}

func (l publicAddrListener) Addr() net.Addr { return l.addr }

// fakePublicBind installs such a listener in place of net.Listen for the duration of the
// test. The daemon publishes the *reported* address in its state file, so the test cannot
// discover the daemon the usual way; realAddr reports the loopback address it can be
// reached on instead (empty until the bind happens).
type fakePublicBind struct {
	mu   sync.Mutex
	real string
}

func (f *fakePublicBind) install(t *testing.T, reported net.Addr) {
	t.Helper()
	previous := listenTCP
	t.Cleanup(func() { listenTCP = previous })
	listenTCP = func(network, _ string) (net.Listener, error) {
		// The requested address is ignored on purpose: a test can then pass an -addr
		// that could not really be bound here (a public hostname, an unresolvable name)
		// and still get a working socket reporting the address it asked for.
		listener, err := previous(network, "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		f.mu.Lock()
		f.real = listener.Addr().String()
		f.mu.Unlock()
		return publicAddrListener{Listener: listener, addr: reported}, nil
	}
}

func (f *fakePublicBind) realAddr() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.real
}

// TestNewAPIServerBoundsHeaderReadsOnly pins both halves of the server's timeout wiring.
// ReadHeaderTimeout must be set — without it a client that opens a connection and never
// finishes its headers pins a goroutine indefinitely, which --allow-public makes
// reachable from the network — and the whole-exchange timeouts must stay unset, because
// /api/events and /api/logs are long-lived SSE streams that any of them would cut off
// mid-stream. Both directions are silent failures: one is invisible until someone probes
// the daemon, the other until a stream dies after exactly N seconds.
func TestNewAPIServerBoundsHeaderReadsOnly(t *testing.T) {
	server := newAPIServer(context.Background(), http.NotFoundHandler())

	if server.ReadHeaderTimeout <= 0 {
		t.Errorf("ReadHeaderTimeout = %v, want a positive bound on header reads", server.ReadHeaderTimeout)
	}
	for name, got := range map[string]time.Duration{
		"ReadTimeout":  server.ReadTimeout,
		"WriteTimeout": server.WriteTimeout,
		"IdleTimeout":  server.IdleTimeout,
	} {
		if got != 0 {
			t.Errorf("%s = %v, want 0: it bounds the whole exchange and would cut off the SSE streams", name, got)
		}
	}
	if server.BaseContext == nil {
		t.Error("BaseContext = nil, want the daemon ctx so shutdown propagates into handlers")
	}
}

// TestServeLogsTheAPITokenSource pins that the startup log says where the token came
// from — a pinned token is a legitimate choice, but nothing in a 401 tells the two apart
// and `make dev` pins a constant — and that it never logs the token itself.
func TestServeLogsTheAPITokenSource(t *testing.T) {
	const pinned = "pinned-token-for-this-test"
	tests := []struct {
		name   string
		envVar string
		want   string
		unwant string
	}{
		{"pinned from the environment", pinned, "pinned from HOMESHELL_API_TOKEN", "generated for this startup"},
		{"generated per startup", "", "generated for this startup", "pinned from HOMESHELL_API_TOKEN"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolateStateFile(t)
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			t.Setenv("HOMESHELL_API_TOKEN", tt.envVar)
			logs := captureDaemonLogs(t)

			startLoopbackDaemon(t, serveFlags{addr: "127.0.0.1:0"}, logs)

			if !strings.Contains(logs.String(), tt.want) {
				t.Errorf("logs = %q, want them to say the token was %s", logs.String(), tt.want)
			}
			if strings.Contains(logs.String(), tt.unwant) {
				t.Errorf("logs = %q, want them not to claim the token was %s", logs.String(), tt.unwant)
			}
			if tt.envVar != "" && strings.Contains(logs.String(), pinned) {
				t.Errorf("logs = %q, want the token value itself never logged", logs.String())
			}
		})
	}
}

// TestServeTrimsThePinnedToken pins that a pinned token surrounded by whitespace still
// authenticates over the Authorization header. The credential check trims the value a
// client *presents*, so an untrimmed expected token could only ever match through the
// cookie — and `export HOMESHELL_API_TOKEN=$(cat secret)` keeps the trailing newline, so
// this is the likely spelling. The symptom was a bare "unauthorized" from every client
// verb with nothing pointing at the whitespace.
func TestServeTrimsThePinnedToken(t *testing.T) {
	isolateStateFile(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("HOMESHELL_API_TOKEN", "  padded-token\n")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	env := Env{Version: testVersion, Stdout: io.Discard, Stderr: io.Discard}
	errCh := make(chan error, 1)
	go func() {
		errCh <- run(ctx, env, serveFlags{addr: "127.0.0.1:0"},
			logging.NewLevel(slog.LevelInfo), logging.NewHub(), false)
	}()
	t.Cleanup(func() {
		cancel()
		<-errCh
	})

	state := waitForState(t, ctx)
	// Fatal: an untrimmed token makes the request below meaningless, and Go's own client
	// refuses to send a header value containing the newline.
	if state.Token != "padded-token" {
		t.Fatalf("state file token = %q, want it trimmed", state.Token)
	}

	// The header path is the one that was broken: apiclient, the CLI verbs and the vite
	// proxy all authenticate this way.
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+state.Address+"/api/hello", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+state.Token)
	response, err := waitForResponse(t, request)
	if err != nil {
		t.Fatalf("GET /api/hello: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want %d for the published token", response.StatusCode, http.StatusOK)
	}
}

// TestServeReissuesTheCookieAfterATokenRotation is the regression test for the headline
// behavior of the token work: a daemon restart mints a new token, so an already-open web
// UI keeps sending the stale hs_api_token cookie. It must get a 401 that carries the
// *fresh* cookie, which is what lets the recreated EventSource re-authenticate without a
// reload. Nothing else composes this — the api e2e fakes a stale cookie by hand, the hook
// spec fakes the browser, and both harnesses pin a fixed token so no rotation happens.
func TestServeReissuesTheCookieAfterATokenRotation(t *testing.T) {
	isolateStateFile(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	// First startup: collect the cookie a browser would have been given, then stop it —
	// a second daemon would otherwise refuse to start against a running one.
	firstState, stopFirst := runUntilState(t)
	staleCookie := firstState.Token
	stopFirst()
	if staleCookie == "" {
		t.Fatal("first startup published no token")
	}

	// Second startup: a fresh per-startup token, i.e. the rotation `homeshell update`
	// performs.
	secondState, stopSecond := runUntilState(t)
	defer stopSecond()
	if secondState.Token == staleCookie {
		t.Fatal("the restart did not rotate the token, so this test proves nothing")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+secondState.Address+"/api/hello", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.AddCookie(&http.Cookie{Name: "hs_api_token", Value: staleCookie})
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	response, err := waitForResponse(t, request)
	if err != nil {
		t.Fatalf("GET /api/hello: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d for a stale cookie", response.StatusCode, http.StatusUnauthorized)
	}
	if got := response.Header.Get("Set-Cookie"); !strings.Contains(got, "hs_api_token="+secondState.Token) {
		t.Errorf("Set-Cookie = %q, want it to carry the rotated token %q", got, secondState.Token)
	}
}

// runUntilState starts a daemon and waits for its state file, returning that state and a
// stop function. Each call is a separate startup with its own generated token, so calling
// it twice — stopping the first, since a second daemon refuses to start against a running
// one — models the restart that rotates the token.
func runUntilState(t *testing.T) (daemonstate.State, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	env := Env{Version: testVersion, Stdout: io.Discard, Stderr: io.Discard}
	errCh := make(chan error, 1)
	go func() {
		errCh <- run(ctx, env, serveFlags{addr: "127.0.0.1:0"},
			logging.NewLevel(slog.LevelInfo), logging.NewHub(), false)
	}()
	state := waitForState(t, ctx)
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			<-errCh
		})
	}
	t.Cleanup(stop)
	return state, stop
}

// waitForState blocks until the daemon has published its discovery state file.
func waitForState(t *testing.T, ctx context.Context) daemonstate.State {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if state, _, err := discover(ctx); err == nil && state.Address != "" && state.Token != "" {
			return state
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("daemon did not publish a state file")
	return daemonstate.State{}
}

// TestServeWarnsOnAShortPinnedTokenOnlyOnAPublicBind pins the warning that a pinned
// token is the whole perimeter once the daemon is network-reachable. On loopback it must
// stay quiet: the document bootstrap hands the real token to any local process anyway,
// and both `make dev` and the e2e harness pin a short constant there.
func TestServeWarnsOnAShortPinnedTokenOnlyOnAPublicBind(t *testing.T) {
	const warning = "PINNED API TOKEN IS SHORT"
	tests := []struct {
		name        string
		token       string
		public      bool
		wantWarning bool
	}{
		{"short token on a public bind", "x", true, true},
		{"short token on a loopback bind", "x", false, false},
		{"long token on a public bind", strings.Repeat("a", minPinnedTokenLength), true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolateStateFile(t)
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			t.Setenv("HOMESHELL_API_TOKEN", tt.token)
			logs := captureDaemonLogs(t)

			flags := serveFlags{addr: "127.0.0.1:0"}
			if tt.public {
				var bind fakePublicBind
				bind.install(t, &net.TCPAddr{IP: net.IPv4(192, 168, 1, 5), Port: 8420})
				flags.allowPublic = true
			}
			startLoopbackDaemon(t, flags, logs)

			if got := strings.Contains(logs.String(), warning); got != tt.wantWarning {
				t.Errorf("warning present = %v, want %v (logs=%q)", got, tt.wantWarning, logs.String())
			}
		})
	}
}

// startLoopbackDaemon runs the daemon until it reports itself listening — the last
// startup log line, so everything a test asserts on has been emitted — then shuts it
// down. It waits on the log rather than on the state file so it also works for a
// fakePublicBind daemon, whose published address nothing answers on.
func startLoopbackDaemon(t *testing.T, flags serveFlags, logs *syncBuffer) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	env := Env{Version: testVersion, Stdout: io.Discard, Stderr: io.Discard}

	errCh := make(chan error, 1)
	go func() {
		errCh <- run(ctx, env, flags, logging.NewLevel(slog.LevelInfo), logging.NewHub(), false)
	}()
	deadline := time.Now().Add(5 * time.Second)
	var started bool
	for time.Now().Before(deadline) {
		if strings.Contains(logs.String(), "HomeShell daemon listening") {
			started = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-errCh; err != nil && started {
		t.Fatalf("daemon exited with an error: %v", err)
	}
	if !started {
		t.Fatalf("daemon did not report itself listening, logs=%q", logs.String())
	}
}

// TestServeRefusesASocketThatBoundPublicWithoutFlag pins the post-listen refusal: the
// pre-listen check passed (127.0.0.1 classifies loopback) but the socket came back
// network-reachable, which is what happens when the name resolves differently inside
// net.Listen than classifyBind saw. The opt-in is re-checked there rather than trusted
// from before the bind.
func TestServeRefusesASocketThatBoundPublicWithoutFlag(t *testing.T) {
	isolateStateFile(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	var bind fakePublicBind
	bind.install(t, &net.TCPAddr{IP: net.IPv4(192, 168, 1, 5), Port: 8420})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	env := Env{Version: testVersion, Stdout: io.Discard, Stderr: io.Discard}

	// In a goroutine, because a daemon that fails to refuse serves forever: this must
	// report a missing refusal rather than hanging until the package timeout.
	errCh := make(chan error, 1)
	go func() {
		errCh <- run(ctx, env, serveFlags{addr: "127.0.0.1:0"}, logging.NewLevel(slog.LevelInfo), logging.NewHub(), false)
	}()
	var err error
	select {
	case err = <-errCh:
	case <-time.After(10 * time.Second):
		cancel()
		<-errCh
		t.Fatal("daemon kept serving a socket that bound non-loopback without --allow-public")
	}
	if err == nil {
		t.Fatal("expected a refusal when the bound socket is not loopback and --allow-public is absent")
	}
	if !strings.Contains(err.Error(), "allow-public") {
		t.Errorf("error = %v, want it to mention --allow-public", err)
	}
	// The flag string alone was factually false in exactly this case, so the message
	// has to name what was actually bound.
	if !strings.Contains(err.Error(), "192.168.1.5:8420") {
		t.Errorf("error = %v, want it to report the resolved address", err)
	}
}

// TestServeOnAPublicBindWarnsAndRelaxesTheHostGuard pins the other half: with the opt-in
// present, a daemon that really is network-reachable says so loudly and switches the
// guard from the loopback Host allow-list to a same-origin check. The e2e harness never
// binds publicly, so this is the only coverage of that mode above the api unit tests.
func TestServeOnAPublicBindWarnsAndRelaxesTheHostGuard(t *testing.T) {
	isolateStateFile(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	logs := captureDaemonLogs(t)
	var bind fakePublicBind
	bind.install(t, &net.TCPAddr{IP: net.IPv4(192, 168, 1, 5), Port: 8420})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	env := Env{Version: testVersion, Stdout: io.Discard, Stderr: io.Discard}

	errCh := make(chan error, 1)
	go func() {
		errCh <- run(ctx, env, serveFlags{addr: "127.0.0.1:0", allowPublic: true},
			logging.NewLevel(slog.LevelInfo), logging.NewHub(), false)
	}()
	t.Cleanup(func() {
		cancel()
		<-errCh
	})

	// The state file carries the reported address, so wait on the real one instead.
	var address string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if address = bind.realAddr(); address != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if address == "" {
		t.Fatalf("daemon did not bind, logs=%q", logs.String())
	}

	// A foreign Host is no longer refused: on a public bind the daemon may legitimately
	// be addressed by any name, so the token carries the weight and the answer is the
	// 401 of a credential-less request rather than the strict mode's 403.
	foreign, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+"/api/hello", nil)
	if err != nil {
		t.Fatal(err)
	}
	foreign.Host = "homeshell.example:8420"
	response, err := waitForResponse(t, foreign)
	if err != nil {
		t.Fatalf("GET /api/hello: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Errorf("foreign Host status = %d, want %d: a public bind relaxes the Host allow-list", response.StatusCode, http.StatusUnauthorized)
	}
	if !strings.Contains(logs.String(), "BINDING A NON-LOOPBACK ADDRESS") {
		t.Errorf("logs = %q, want the public-bind warning", logs.String())
	}
}

// waitForResponse retries a request until the daemon's listener is accepting, so a test
// does not race the goroutine that starts serving.
func waitForResponse(t *testing.T, request *http.Request) (*http.Response, error) {
	t.Helper()
	var err error
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var response *http.Response
		response, err = http.DefaultClient.Do(request)
		if err == nil {
			return response, nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil, err
}

// captureDaemonLogs redirects the slog default handler — which is what the daemon's
// startup warnings go through — into a buffer for the duration of the test.
func captureDaemonLogs(t *testing.T) *syncBuffer {
	t.Helper()
	var logs syncBuffer
	previous := slog.Default()
	logging.Configure(&logs, logging.NewLevel(slog.LevelInfo))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &logs
}

// TestServeWarnsWhenBindHostIsNotALoopbackLiteral pins the startup warning for a bind
// host that resolves to loopback but is not a literal the /api guard recognizes (an
// /etc/hosts alias such as "ip6-localhost", or the absolute "localhost." used here).
// Such a daemon starts clean and then 403s every request that addresses it by that
// name, so the warning is the whole fix — and a warning nothing asserts can be
// deleted undetected.
func TestServeWarnsWhenBindHostIsNotALoopbackLiteral(t *testing.T) {
	// "localhost." is the same name with an explicit root label: it resolves through
	// the hosts file like "localhost" but is not a literal, so the guard rejects it.
	// Skip rather than fail where the resolver does not accept the trailing dot.
	if probe, err := net.Listen("tcp", "localhost.:0"); err != nil {
		t.Skipf("this resolver cannot bind %q: %v", "localhost.:0", err)
	} else {
		probe.Close()
	}

	tests := []struct {
		name        string
		addr        string
		bindHost    string
		public      bool
		wantWarning bool
		wantStatus  int
	}{
		// The status is the consequence the warning describes: a client addressing the
		// daemon by the bind host either gets through the guard to the 401 of a
		// credential-less request, or is refused 403 by the guard itself.
		{"loopback alias warns", "localhost.:0", "localhost.", false, true, http.StatusForbidden},
		{"loopback literal is silent", "127.0.0.1:0", "127.0.0.1", false, false, http.StatusUnauthorized},
		// On a public bind the guard does not apply the Host allow-list at all, so the
		// advice ("use 127.0.0.1 or localhost") would be wrong: the operator picked that
		// name deliberately and requests addressed to it are accepted. Without the
		// !publicBind conjunct this row gets the warning right after being told the
		// daemon is network-reachable.
		{"public bind is silent for a non-literal host", "homeshell.example:8420", "homeshell.example", true, false, http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolateStateFile(t)
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			logs := captureDaemonLogs(t)

			flags := serveFlags{addr: tt.addr}
			var bind fakePublicBind
			if tt.public {
				// A real public socket is never opened: the listener binds loopback and
				// reports a reachable address, which is what publicBind is derived from.
				bind.install(t, &net.TCPAddr{IP: net.IPv4(192, 168, 1, 5), Port: 8420})
				flags.allowPublic = true
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			env := Env{Version: testVersion, Stdout: io.Discard, Stderr: io.Discard}

			errCh := make(chan error, 1)
			go func() {
				errCh <- run(ctx, env, flags,
					logging.NewLevel(slog.LevelInfo), logging.NewHub(), false)
			}()

			// Wait until the daemon is up. The warnings are emitted before it starts
			// serving, so by then the decision has been made either way. A public-bind
			// daemon publishes its *reported* address, which nothing answers on, so take
			// the real one from the fake listener instead of from the state file.
			deadline := time.Now().Add(5 * time.Second)
			var address string
			for time.Now().Before(deadline) {
				if tt.public {
					address = bind.realAddr()
				} else if st, _, err := discover(ctx); err == nil {
					address = st.Address
				}
				if address != "" {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if address == "" {
				t.Fatalf("daemon did not start, logs=%q", logs.String())
			}

			// Assert the consequence before shutting down, so the warning cannot become
			// a lie (or a false alarm) with this test still green.
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+"/api/hello", nil)
			if err != nil {
				t.Fatal(err)
			}
			_, port, err := net.SplitHostPort(address)
			if err != nil {
				t.Fatalf("splitting %q: %v", address, err)
			}
			request.Host = net.JoinHostPort(tt.bindHost, port)
			response, err := waitForResponse(t, request)
			if err != nil {
				t.Fatalf("GET /api/hello: %v", err)
			}
			if response.StatusCode != tt.wantStatus {
				t.Errorf("status for Host %q = %d, want %d", request.Host, response.StatusCode, tt.wantStatus)
			}
			response.Body.Close()

			cancel()
			<-errCh

			const warning = "BIND HOST IS NOT A LOOPBACK LITERAL"
			if got := strings.Contains(logs.String(), warning); got != tt.wantWarning {
				t.Errorf("warning present = %v, want %v for -addr=%s (logs=%q)",
					got, tt.wantWarning, tt.addr, logs.String())
			}
		})
	}
}

// TestServeRefusesUnresolvableHostWithoutFlag verifies that an unresolvable bind
// host stays fail-closed (still needs --allow-public) but is reported with an
// accurate "cannot verify … loopback" message rather than being mislabeled a
// non-loopback/public address.
func TestServeRefusesUnresolvableHostWithoutFlag(t *testing.T) {
	isolateStateFile(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	env := Env{Version: testVersion, Stdout: io.Discard, Stderr: io.Discard}
	// RFC 2606 reserved TLD: never resolves, so this is deterministic and offline.
	err := run(ctx, env, serveFlags{addr: "nonexistent.invalid:0"}, logging.NewLevel(slog.LevelInfo), logging.NewHub(), false)
	if err == nil {
		t.Fatal("expected an error binding an unresolvable host without --allow-public")
	}
	if !strings.Contains(err.Error(), "allow-public") {
		t.Errorf("error = %v, want it to mention --allow-public", err)
	}
	if !strings.Contains(err.Error(), "cannot verify") {
		t.Errorf("error = %v, want the accurate 'cannot verify' wording", err)
	}
	if strings.Contains(err.Error(), "non-loopback address") {
		t.Errorf("error = %v, should not mislabel an unresolvable host as a non-loopback address", err)
	}
}

func TestResolveLogLevel(t *testing.T) {
	t.Run("flag wins over env", func(t *testing.T) {
		t.Setenv("HOMESHELL_LOG_LEVEL", "error")
		got, explicit, err := resolveLogLevel("debug")
		if err != nil || got != slog.LevelDebug || !explicit {
			t.Fatalf("got (%v, %v, %v), want (Debug, true, nil)", got, explicit, err)
		}
	})
	t.Run("env when flag empty", func(t *testing.T) {
		t.Setenv("HOMESHELL_LOG_LEVEL", "warn")
		got, explicit, err := resolveLogLevel("")
		if err != nil || got != slog.LevelWarn || !explicit {
			t.Fatalf("got (%v, %v, %v), want (Warn, true, nil)", got, explicit, err)
		}
	})
	t.Run("verbose level", func(t *testing.T) {
		t.Setenv("HOMESHELL_LOG_LEVEL", "")
		got, explicit, err := resolveLogLevel("verbose")
		if err != nil || got != logging.LevelVerbose || !explicit {
			t.Fatalf("got (%v, %v, %v), want (Verbose, true, nil)", got, explicit, err)
		}
	})
	t.Run("default info is not explicit", func(t *testing.T) {
		t.Setenv("HOMESHELL_LOG_LEVEL", "")
		got, explicit, err := resolveLogLevel("")
		if err != nil || got != slog.LevelInfo || explicit {
			t.Fatalf("got (%v, %v, %v), want (Info, false, nil)", got, explicit, err)
		}
	})
	t.Run("invalid value errors", func(t *testing.T) {
		if _, _, err := resolveLogLevel("loud"); err == nil {
			t.Fatal("expected an error for an invalid level")
		}
	})
}

// syncBuffer is a bytes.Buffer safe for the daemon goroutine to write while the
// test goroutine reads.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestServeReportsStatusOnStart verifies that a fresh daemon echoes the same
// running report `status` gives, so its address and state file are visible
// without a second command (PR #38 review).
func TestServeReportsStatusOnStart(t *testing.T) {
	isolateStateFile(t)
	// Isolate the data dir too: without this the daemon loads the developer's
	// real persisted projects and runs git-backing detection over all of them at
	// startup, which can blow the report timeout on a machine with many projects.
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var out syncBuffer
	env := Env{Version: testVersion, Stdout: &out, Stderr: io.Discard}

	done := make(chan error, 1)
	go func() {
		done <- run(ctx, env, serveFlags{addr: "127.0.0.1:0"}, logging.NewLevel(slog.LevelInfo), logging.NewHub(), false)
	}()

	deadline := time.After(5 * time.Second)
	for !strings.Contains(out.String(), "running") {
		select {
		case err := <-done:
			t.Fatalf("daemon exited before reporting: err=%v, output=%q", err, out.String())
		case <-deadline:
			t.Fatalf("daemon did not report running within the timeout, output=%q", out.String())
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("daemon exited with error: %v", err)
	}

	got := out.String()
	for _, want := range []string{"running", testVersion, "daemon", statePath()} {
		if !strings.Contains(got, want) {
			t.Errorf("startup report = %q, want it to contain %q", got, want)
		}
	}
}

// --- persistence integration tests ---

// startNonDemoDaemon starts the daemon in non-demo mode, loading state from
// dataDir/homeshell (via persist.SetHomeDir). It returns the base URL and the
// per-startup API token (read from the published state file) and registers a
// cleanup that cancels and waits for shutdown.
func startNonDemoDaemon(t *testing.T, dataDir string) (base, token string) {
	t.Helper()
	base, token, _ = startStoppableNonDemoDaemon(t, dataDir)
	return base, token
}

// startStoppableNonDemoDaemon is startNonDemoDaemon for a test that stops the
// daemon itself, to start another one in the same directory. stop is
// idempotent and also runs at cleanup.
func startStoppableNonDemoDaemon(t *testing.T, dataDir string) (base, token string, stop func()) {
	t.Helper()
	old := persist.HomeDir()
	persist.SetHomeDir(filepath.Join(dataDir, "homeshell"))
	t.Cleanup(func() { persist.SetHomeDir(old) })

	ctx, cancel := context.WithCancel(context.Background())
	env := Env{Version: testVersion, Stdout: io.Discard, Stderr: io.Discard}

	errCh := make(chan error, 1)
	go func() {
		errCh <- run(ctx, env, serveFlags{addr: "127.0.0.1:0"}, logging.NewLevel(slog.LevelInfo), logging.NewHub(), false)
	}()

	// Wait for the daemon to publish its address via the state file.
	var addr string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		st, _, discoverErr := discover(ctx)
		if discoverErr == nil && st.Address != "" {
			addr = st.Address
			token = st.Token
			break
		}
		select {
		case err := <-errCh:
			cancel()
			t.Fatalf("daemon exited during startup: %v", err)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	if addr == "" {
		cancel()
		t.Fatal("daemon did not publish its address within the timeout")
	}

	var stopOnce sync.Once
	stop = func() {
		stopOnce.Do(func() {
			cancel()
			select {
			case <-errCh:
			case <-time.After(5 * time.Second):
				t.Error("daemon did not shut down within 5s")
			}
		})
	}
	t.Cleanup(stop)

	return fmt.Sprintf("http://%s", addr), token, stop
}

// getAPIProjects issues an authenticated GET /api/projects against the daemon.
func getAPIProjects(t *testing.T, base, token string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, base+"/api/projects", nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/projects: %v", err)
	}
	return resp
}

func TestNonDemoStartLoadsPersistedState(t *testing.T) {
	dataDir := t.TempDir()
	persistDir := filepath.Join(dataDir, "homeshell")

	projects := []domain.Project{
		{
			ID:   "proj-persisted",
			Name: "persisted-project",
			Path: "/tmp/persisted",
			Workstreams: []domain.Workstream{
				{
					ID:         "ws-1",
					Name:       "ws",
					Provider:   "Local System",
					StrategyID: "in-place",
					Sessions: []domain.Session{
						// Owned by a provider this daemon does not register, so
						// the startup reconcile drops it instead of serving
						// an un-routable ghost.
						{ID: "s-1", Title: "persistence works", Agent: "Test", Provider: "retired"},
					},
				},
			},
		},
	}
	writeStateFile(t, persistDir, projects)

	base, token := startNonDemoDaemon(t, dataDir)

	resp := getAPIProjects(t, base, token)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var payload struct {
		Projects []domain.Project `json:"projects"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(payload.Projects) != 1 {
		t.Fatalf("got %d projects, want 1", len(payload.Projects))
	}
	if payload.Projects[0].ID != "proj-persisted" {
		t.Errorf("project ID = %q, want proj-persisted", payload.Projects[0].ID)
	}
	if payload.Projects[0].Workstreams[0].ID != "ws-1" {
		t.Errorf("workstream ID = %q, want ws-1", payload.Projects[0].Workstreams[0].ID)
	}
	// The workstream record survives; its session does not, because no
	// registered provider claims it. Before sessions persisted their provider,
	// such a session was retained forever and 404'd on delete.
	if n := len(payload.Projects[0].Workstreams[0].Sessions); n != 0 {
		t.Errorf("got %d sessions after startup reconcile, want 0 (unclaimed session is not a ghost)", n)
	}
}

func TestNonDemoEmptyDirStartsEmpty(t *testing.T) {
	base, token := startNonDemoDaemon(t, t.TempDir())

	resp := getAPIProjects(t, base, token)
	defer resp.Body.Close()

	var payload struct {
		Projects []domain.Project `json:"projects"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(payload.Projects) != 0 {
		t.Errorf("expected 0 projects, got %d", len(payload.Projects))
	}
}

// writeStateFile persists projects the way the daemon does, so a test can hand
// a daemon a state to start from.
func writeStateFile(t *testing.T, dir string, projects []domain.Project) {
	t.Helper()
	snapshot := persist.EmptySnapshot()
	for _, project := range projects {
		record := persist.RecordProject(project)
		for _, workstream := range project.Workstreams {
			workstreamRecord := persist.RecordWorkstream(workstream)
			for _, session := range workstream.Sessions {
				workstreamRecord.Sessions = append(workstreamRecord.Sessions, persist.RecordSession(session))
			}
			record.Workstreams = append(record.Workstreams, workstreamRecord)
		}
		snapshot.Projects = append(snapshot.Projects, record)
	}
	if err := persist.NewFileSaver(filepath.Join(dir, persist.StateFileName)).Save(snapshot); err != nil {
		t.Fatalf("writing the state file: %v", err)
	}
}

// runNonDemoInDir runs the daemon once against dir and returns its error.
func runNonDemoInDir(t *testing.T, dir string) error {
	t.Helper()
	old := persist.HomeDir()
	persist.SetHomeDir(dir)
	t.Cleanup(func() { persist.SetHomeDir(old) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	env := Env{Version: testVersion, Stdout: io.Discard, Stderr: io.Discard}
	return run(ctx, env, serveFlags{addr: "127.0.0.1:0"}, logging.NewLevel(slog.LevelInfo), logging.NewHub(), false)
}

func TestNonDemoCorruptStateFileRefusesToStart(t *testing.T) {
	for _, file := range []string{persist.StateFileName, agentcatalog.FileName} {
		t.Run(file, func(t *testing.T) {
			persistDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(persistDir, file), []byte("not json"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := runNonDemoInDir(t, persistDir); err == nil {
				t.Fatalf("expected an error on a corrupt %s, got nil", file)
			}
		})
	}
}

// A pre-release database is not read: the daemon starts empty, with the default
// catalog, and only warns about the file.
func TestNonDemoIgnoresALeftoverDatabase(t *testing.T) {
	dataDir := t.TempDir()
	persistDir := filepath.Join(dataDir, "homeshell")
	if err := os.MkdirAll(persistDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(persistDir, "homeshell.db"), []byte("not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	base, token := startNonDemoDaemon(t, dataDir)

	resp := getAPIProjects(t, base, token)
	defer resp.Body.Close()
	var payload struct {
		Projects []domain.Project `json:"projects"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(payload.Projects) != 0 {
		t.Errorf("expected 0 projects, got %d", len(payload.Projects))
	}
	if _, err := os.Stat(filepath.Join(persistDir, agentcatalog.FileName)); err != nil {
		t.Errorf("the default agent catalog was not written: %v", err)
	}
}

// What a daemon persists, the next daemon in the same directory serves again.
func TestNonDemoStateSurvivesARestart(t *testing.T) {
	dataDir := t.TempDir()
	persistDir := filepath.Join(dataDir, "homeshell")

	first, token, stop := startStoppableNonDemoDaemon(t, dataDir)
	body := strings.NewReader(`{"name":"persisted","path":"` + t.TempDir() + `"}`)
	req, _ := http.NewRequest(http.MethodPost, first+"/api/projects", body)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /api/projects: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/projects status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	// The write is synchronous with the request, so the file holds it now.
	snapshot, err := persist.LoadState(filepath.Join(persistDir, persist.StateFileName))
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if len(snapshot.Projects) != 1 || snapshot.Projects[0].Name != "persisted" {
		t.Fatalf("state file holds %+v, want the added project", snapshot.Projects)
	}
	stop()

	second, token := startNonDemoDaemon(t, dataDir)
	resp = getAPIProjects(t, second, token)
	defer resp.Body.Close()
	var payload struct {
		Projects []domain.Project `json:"projects"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(payload.Projects) != 1 || payload.Projects[0].Name != "persisted" {
		t.Errorf("after a restart got %+v, want the project added before it", payload.Projects)
	}
}
