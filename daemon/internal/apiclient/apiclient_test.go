package apiclient

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/eclipsesource/homeshell/daemon/internal/daemonstate"
	"github.com/eclipsesource/homeshell/daemon/internal/persist"
)

func TestNewBaseURL(t *testing.T) {
	c := New("127.0.0.1:8420", "")
	if c.BaseURL != "http://127.0.0.1:8420" {
		t.Errorf("BaseURL = %q, want %q", c.BaseURL, "http://127.0.0.1:8420")
	}
}

func TestHelloProbe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/hello" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"message":"Hello from the HomeShell daemon","version":"7.7.7"}`))
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, hc: srv.Client()}
	hello, err := c.Hello(context.Background())
	if err != nil {
		t.Fatalf("Hello: %v", err)
	}
	if hello.Version != "7.7.7" {
		t.Errorf("version = %q, want %q", hello.Version, "7.7.7")
	}
}

func TestHelloSendsBearerToken(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"message":"hi","version":"1.0.0"}`))
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, token: "my-token", hc: srv.Client()}
	if _, err := c.Hello(context.Background()); err != nil {
		t.Fatalf("Hello: %v", err)
	}
	if gotAuth != "Bearer my-token" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer my-token")
	}
}

// countingListener records how many connections were accepted, so a test can tell
// whether two clients shared a connection pool.
type countingListener struct {
	net.Listener
	accepted atomic.Int64
}

func (l *countingListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err == nil {
		l.accepted.Add(1)
	}
	return conn, err
}

// TestNewDoesNotShareDefaultTransport pins that a Client keeps its own connection
// pool. Reading the code is not enough to know this: a bare &http.Client{} has a nil
// Transport and silently falls back to http.DefaultTransport, so an earlier attempt at
// this isolation was a no-op that no test noticed. Counting accepted connections is
// what actually distinguishes the two.
func TestNewDoesNotShareDefaultTransport(t *testing.T) {
	base, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listener := &countingListener{Listener: base}
	srv := &httptest.Server{
		Listener: listener,
		Config: &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"message":"hi","version":"1.0.0"}`))
		})},
	}
	srv.Start()
	defer srv.Close()

	// Warm http.DefaultTransport's pool against the same server: one connection.
	warm, err := http.Get(srv.URL + "/api/hello")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, warm.Body)
	warm.Body.Close()
	if got := listener.accepted.Load(); got != 1 {
		t.Fatalf("after warming DefaultTransport: accepted = %d, want 1", got)
	}

	// The client must dial its own, and then reuse it for both of its http.Clients.
	client := New(strings.TrimPrefix(srv.URL, "http://"), "tok")
	if _, err := client.Hello(context.Background()); err != nil {
		t.Fatalf("Hello: %v", err)
	}
	if got := listener.accepted.Load(); got != 2 {
		t.Errorf("after Client.Hello: accepted = %d, want 2 (the Client must not use DefaultTransport's pool)", got)
	}
	// Drop DefaultTransport's idle connection first. Without this the Do assertion below
	// pins nothing: an untimed client that fell back to DefaultTransport would reuse the
	// connection warmed above and the count would stay 2 either way.
	http.DefaultTransport.(*http.Transport).CloseIdleConnections()
	response, err := client.Do(context.Background(), http.MethodGet, "/api/hello", nil)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if got := listener.accepted.Load(); got != 2 {
		t.Errorf("after Client.Do: accepted = %d, want 2 (Do must share the Client's own pool)", got)
	}
}

// TestDoOnLiteralClientUsesInjectedTransport covers the struct-literal idiom the
// package's own tests use: Do must fall back to the injected client's transport rather
// than panicking on a nil field or reaching for http.DefaultTransport.
func TestDoOnLiteralClientUsesInjectedTransport(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, hc: srv.Client()}
	response, err := c.Do(context.Background(), http.MethodPost, "/api/update", nil)
	if err != nil {
		t.Fatalf("Do on a struct-literal Client: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d, want %d", response.StatusCode, http.StatusNoContent)
	}
}

func TestHelloWithoutTokenSendsNoAuthHeader(t *testing.T) {
	var hadAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hadAuth = r.Header["Authorization"]
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"message":"hi","version":"1.0.0"}`))
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, hc: srv.Client()}
	if _, err := c.Hello(context.Background()); err != nil {
		t.Fatalf("Hello: %v", err)
	}
	if hadAuth {
		t.Error("an Authorization header was sent for a token-less client")
	}
}

func TestDiscoverPopulatesToken(t *testing.T) {
	old := persist.HomeDir()
	persist.SetHomeDir(t.TempDir())
	t.Cleanup(func() { persist.SetHomeDir(old) })
	if err := daemonstate.Write(daemonstate.State{Address: "127.0.0.1:9999", PID: 1, Version: "v", Token: "state-token"}); err != nil {
		t.Fatal(err)
	}

	c, _, err := Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if c.token != "state-token" {
		t.Errorf("client token = %q, want %q", c.token, "state-token")
	}
}

func TestHelloProbeReportsBadStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, hc: srv.Client()}
	if _, err := c.Hello(context.Background()); err == nil {
		t.Fatal("Hello against a 500 = nil error, want a failure")
	}
}

func TestHelloProbeReportsUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // now nothing is listening

	c := &Client{BaseURL: url, hc: &http.Client{}}
	if _, err := c.Hello(context.Background()); err == nil {
		t.Fatal("Hello against a closed server = nil error, want a failure")
	}
}

func TestDiscoverAimsAtRecordedAddress(t *testing.T) {
	old := persist.HomeDir()
	persist.SetHomeDir(t.TempDir())
	t.Cleanup(func() { persist.SetHomeDir(old) })
	if err := daemonstate.Write(daemonstate.State{Address: "127.0.0.1:9999", PID: 1, Version: "v"}); err != nil {
		t.Fatal(err)
	}

	c, st, err := Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if st.Address != "127.0.0.1:9999" {
		t.Errorf("state address = %q, want %q", st.Address, "127.0.0.1:9999")
	}
	if c.BaseURL != "http://127.0.0.1:9999" {
		t.Errorf("client BaseURL = %q, want %q", c.BaseURL, "http://127.0.0.1:9999")
	}
}

func TestDiscoverWithoutStateFileWrapsNotExist(t *testing.T) {
	old := persist.HomeDir()
	persist.SetHomeDir(t.TempDir())
	t.Cleanup(func() { persist.SetHomeDir(old) })
	if _, _, err := Discover(); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Discover with no state file = %v, want an fs.ErrNotExist", err)
	}
}
