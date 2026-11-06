package api

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/notify"
	"github.com/eclipsesource/homeshell/daemon/internal/store"
)

// guardedHandler builds the daemon HTTP surface with the loopback guard enabled
// on the given listen address.
func guardedHandler(t *testing.T, listenAddr string) http.Handler {
	t.Helper()
	st := store.NewTestStore(t, store.Seed())
	return handlerForTest(Deps{
		Version:       "t",
		Store:         st,
		Heartbeat:     time.Minute,
		Notifications: notify.NewHub(),
		ListenAddr:    listenAddr,
	})
}

// TestHandlerGuardsBeforeAuth pins the middleware order Handler itself builds, with a
// real token configured. requireToken has no Host check of its own — it relies on
// guardLoopback running outside it — so transposing the two would let a DNS-rebound
// page (which can create an hs_api_token for its own name via document.cookie, then
// fetch same-origin) collect the real token from the 401 self-heal, exactly the leak
// the document bootstrap's Host check closes. The other composed tests build the chain
// by hand and would not notice; every other api unit test leaves APIToken empty, which
// makes requireToken a pass-through and the order unobservable.
func TestHandlerGuardsBeforeAuth(t *testing.T) {
	st := store.NewTestStore(t, store.Seed())
	handler := handlerForTest(Deps{
		Version:       "t",
		Store:         st,
		Heartbeat:     time.Minute,
		Notifications: notify.NewHub(),
		ListenAddr:    "127.0.0.1:8420",
		APIToken:      "handler-order-token",
	})

	request := httptest.NewRequest(http.MethodGet, "/api/hello", nil)
	request.RemoteAddr = "127.0.0.1:51234"
	request.Host = "evil.com"
	request.AddCookie(&http.Cookie{Name: cookieName, Value: "planted-by-the-rebound-page"})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d (the guard must run outside requireToken)", recorder.Code, http.StatusForbidden)
	}
	if got := setCookieValue(recorder, cookieName); got != "" {
		t.Errorf("re-issued the token (%q) to a rebound Host", got)
	}
}

func TestGuardLoopback(t *testing.T) {
	tests := []struct {
		name   string
		host   string
		origin string
		want   int
	}{
		{"loopback ip with port", "127.0.0.1:8420", "", http.StatusOK},
		{"localhost with port", "localhost:8420", "", http.StatusOK},
		{"ipv6 loopback with port", "[::1]:8420", "", http.StatusOK},
		{"matching origin ip", "127.0.0.1:8420", "http://127.0.0.1:8420", http.StatusOK},
		{"matching origin localhost alias", "127.0.0.1:8420", "http://localhost:8420", http.StatusOK},
		{"foreign host", "evil.com:8420", "", http.StatusForbidden},
		// A loopback literal on another port is a port-forward or a loopback proxy
		// (ssh -L 9999:localhost:8420), not a rebinding attempt.
		{"loopback ip on a forwarded port", "127.0.0.1:9999", "", http.StatusOK},
		{"forwarded port with its own matching origin", "localhost:9999", "http://localhost:9999", http.StatusOK},
		// A page served from another loopback port is same-site (so the cookie would
		// ride along) but cross-origin: the Origin must match the request's own port.
		{"cross-port loopback origin", "127.0.0.1:8420", "http://localhost:3000", http.StatusForbidden},
		{"daemon-port origin on a forwarded host", "localhost:9999", "http://localhost:8420", http.StatusForbidden},
		{"lan address", "192.168.1.5:8420", "", http.StatusForbidden},
		{"empty host", "", "", http.StatusForbidden},
		{"loopback ip no port", "127.0.0.1", "", http.StatusOK},
		{"cross-origin host", "127.0.0.1:8420", "http://evil.com", http.StatusForbidden},
		{"cross-origin host with port", "127.0.0.1:8420", "http://evil.com:8420", http.StatusForbidden},
		{"null origin", "127.0.0.1:8420", "null", http.StatusForbidden},
		{"https loopback origin accepted (parity with sameOrigin)", "127.0.0.1:8420", "https://127.0.0.1:8420", http.StatusOK},
		{"https localhost origin accepted", "127.0.0.1:8420", "https://localhost:8420", http.StatusOK},
		{"foreign https origin rejected", "127.0.0.1:8420", "https://evil.com:8420", http.StatusForbidden},
		{"non-http(s) scheme origin rejected", "127.0.0.1:8420", "ftp://127.0.0.1:8420", http.StatusForbidden},
		{"origin userinfo trick", "127.0.0.1:8420", "http://127.0.0.1:8420@evil.com", http.StatusForbidden},
		{"bracketed ipv6 loopback no port", "[::1]", "", http.StatusOK},
		{"malformed trailing bracket host", "127.0.0.1]", "", http.StatusForbidden},
		// Hostnames are not case-sensitive, and a non-browser client forwards the
		// case it was given (curl http://LOCALHOST:8420 -> "Host: LOCALHOST:8420").
		{"uppercase loopback host", "LOCALHOST:8420", "", http.StatusOK},
		{"mixed-case loopback host and origin", "LocalHost:8420", "http://LOCALHOST:8420", http.StatusOK},
		// Any loopback IP literal, not just 127.0.0.1: `serve -addr=127.0.0.2` is a
		// legitimate loopback bind, and rejecting its Host produced a daemon that
		// started clean and then 403'd everything.
		{"other 127/8 host", "127.0.0.2:8420", "", http.StatusOK},
		{"other 127/8 host with matching origin", "127.0.0.2:8420", "http://127.0.0.2:8420", http.StatusOK},
		{"127/8 upper bound", "127.255.255.254:8420", "", http.StatusOK},
		// Still not a loopback address, and still not a literal at all.
		{"near-loopback 126/8", "126.0.0.1:8420", "", http.StatusForbidden},
		{"near-loopback 128/8", "128.0.0.1:8420", "", http.StatusForbidden},
		{"loopback-looking name", "localhost.evil.com:8420", "", http.StatusForbidden},
	}

	handler := guardedHandler(t, "127.0.0.1:8420")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/api/hello", nil)
			request.Host = tt.host
			if tt.origin != "" {
				request.Header.Set("Origin", tt.origin)
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)

			if recorder.Code != tt.want {
				t.Fatalf("status = %d, want %d", recorder.Code, tt.want)
			}
		})
	}
}

// TestGuardPublicBind covers the relaxed guard used when the daemon really is bound
// to a network-reachable address: the Host allow-list is skipped and the Origin check
// is generalized to same-origin (matching the request's own Host).
func TestGuardPublicBind(t *testing.T) {
	tests := []struct {
		name   string
		host   string
		origin string
		want   int
	}{
		{"non-loopback host accepted", "192.168.1.5:8420", "", http.StatusOK},
		{"public dns host accepted", "homeshell.example:8420", "", http.StatusOK},
		{"same-origin accepted", "192.168.1.5:8420", "http://192.168.1.5:8420", http.StatusOK},
		{"foreign origin rejected", "192.168.1.5:8420", "http://evil.com", http.StatusForbidden},
		{"cross-port origin rejected", "192.168.1.5:8420", "http://192.168.1.5:9999", http.StatusForbidden},
		// The hostname half of the check, on the port the request itself was addressed
		// to: without these rows every rejection above is decided by the port alone
		// (evil.com resolves to the default 80, not 8420), so dropping the hostname
		// comparison entirely would leave the suite green.
		{"foreign origin on the request's own port rejected", "192.168.1.5:8420", "http://evil.com:8420", http.StatusForbidden},
		{"foreign origin on a portless host rejected", "homeshell.example", "http://evil.example", http.StatusForbidden},
		{"loopback host still accepted", "127.0.0.1:8420", "", http.StatusOK},
		// sameOrigin has the same userinfo trap as originAllowed: url.Parse would report
		// a matching Host while evil.com sits in User.
		{"userinfo origin rejected", "192.168.1.5:8420", "http://evil.com@192.168.1.5:8420", http.StatusForbidden},
		// An omitted port is the scheme's default, so the first two pairs address the
		// same origin: comparing Origin.Host against request.Host as one string made
		// exactly those a 403, while the loopback path accepted the equivalent
		// (loopback) pair. The rest bound the rule and passed before the change too.
		{"portless https origin on a 443 host", "homeshell.example:443", "https://homeshell.example", http.StatusOK},
		{"portless http origin on an 80 host", "homeshell.example:80", "http://homeshell.example", http.StatusOK},
		{"portless origin on a portless host", "homeshell.example", "http://homeshell.example", http.StatusOK},
		{"scheme default still has to match", "homeshell.example:80", "https://homeshell.example", http.StatusForbidden},
		{"explicit port on a portless host rejected", "homeshell.example", "https://homeshell.example:443", http.StatusForbidden},
		// Hostnames compare case-insensitively and an unbracketed IPv6 Host matches a
		// bracketed Origin, both of which the one-string comparison got wrong.
		{"mixed-case origin hostname", "homeshell.example:8420", "http://HomeShell.Example:8420", http.StatusOK},
		{"bracketed origin on an unbracketed ipv6 host", "[2001:db8::1]:8420", "http://[2001:db8::1]:8420", http.StatusOK},
		{"portless bracketed origin on a portless ipv6 host", "[2001:db8::1]", "http://[2001:db8::1]", http.StatusOK},
	}

	next := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
	})
	guard := guardLoopback("0.0.0.0:8420", true, next)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/api/hello", nil)
			request.Host = tt.host
			if tt.origin != "" {
				request.Header.Set("Origin", tt.origin)
			}
			recorder := httptest.NewRecorder()
			guard.ServeHTTP(recorder, request)
			if recorder.Code != tt.want {
				t.Fatalf("status = %d, want %d", recorder.Code, tt.want)
			}
		})
	}
}

// TestOriginAllowed covers the two properties of the loopback Origin check: the
// scheme parity with sameOrigin (http and https both accepted, nothing else), and
// that the Origin's port must match the port the *request* was addressed to — not
// the daemon's own — so a port-forward works while another loopback port does not.
func TestOriginAllowed(t *testing.T) {
	tests := []struct {
		name        string
		origin      string
		requestHost string
		want        bool
	}{
		{"http loopback ip", "http://127.0.0.1:8420", "127.0.0.1:8420", true},
		{"https loopback ip", "https://127.0.0.1:8420", "127.0.0.1:8420", true},
		{"https localhost alias", "https://localhost:8420", "127.0.0.1:8420", true},
		{"https ipv6 loopback", "https://[::1]:8420", "127.0.0.1:8420", true},
		{"forwarded port matching the request host", "http://localhost:9999", "localhost:9999", true},
		// A ported Host is compared against the Origin's port, with the Origin scheme's
		// default filled in when it omits one.
		{"portless https origin on a 443 host", "https://localhost", "localhost:443", true},
		{"portless http origin on an 80 host", "http://localhost", "localhost:80", true},
		{"portless https origin on an 80 host", "https://localhost", "localhost:80", false},
		// A portless Host means an indirection stripped the port (a proxy on 80 or 443),
		// so the client's port is unknowable: accept an Origin that also omits one —
		// this is the TLS-proxy-on-443 case the https allowance exists for, and it must
		// hold whatever port the daemon itself is on — and reject an explicit one.
		{"portless https origin on a portless host", "https://localhost", "localhost", true},
		{"portless http origin on a portless host", "http://localhost", "localhost", true},
		{"explicit 443 origin on a portless host", "https://localhost:443", "localhost", false},
		{"explicit 80 origin on a portless host", "http://localhost:80", "localhost", false},
		// Ports compare as strings: a numeric comparison would only widen this, letting
		// "Host: localhost:08420" match an Origin on 8420.
		{"non-canonical host port", "http://localhost:8420", "localhost:08420", false},
		{"foreign https host", "https://evil.com:8420", "127.0.0.1:8420", false},
		{"other loopback port", "https://127.0.0.1:9999", "127.0.0.1:8420", false},
		{"daemon port against a forwarded host", "http://localhost:8420", "localhost:9999", false},
		{"portless origin on a ported host", "http://localhost", "localhost:8420", false},
		{"non-http(s) scheme", "ftp://127.0.0.1:8420", "127.0.0.1:8420", false},
		{"opaque null origin", "null", "127.0.0.1:8420", false},
		// url.Parse reports the loopback literal as Hostname here and puts evil.com in
		// User, so a Hostname-only check would accept it.
		{"userinfo prefix trick", "http://evil.com@127.0.0.1:8420", "127.0.0.1:8420", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := originAllowed(tt.origin, tt.requestHost); got != tt.want {
				t.Errorf("originAllowed(%q, %q) = %v, want %v", tt.origin, tt.requestHost, got, tt.want)
			}
		})
	}
}

func TestGuardRejectionIsJSON(t *testing.T) {
	handler := guardedHandler(t, "127.0.0.1:8420")
	request := httptest.NewRequest(http.MethodGet, "/api/hello", nil)
	request.Host = "evil.com"
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
	if contentType := recorder.Header().Get("Content-Type"); contentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", contentType)
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if body.Error == "" {
		t.Error("error message is empty")
	}
}

// TestGuardEnabledForPortlessListenAddr pins that only an *empty* ListenAddr
// disables the guard. The seam used to key off SplitHostPort succeeding, so a
// ListenAddr with no port ("127.0.0.1") silently switched off both the rebinding and
// the CSRF check — a fail-open on a malformed value, and the guard no longer reads
// the port for anything anyway.
func TestGuardEnabledForPortlessListenAddr(t *testing.T) {
	next := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
	})
	guard := guardLoopback("127.0.0.1", false, next)
	request := httptest.NewRequest(http.MethodGet, "/api/hello", nil)
	request.Host = "evil.com"
	recorder := httptest.NewRecorder()
	guard.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d (a portless ListenAddr must not disable the guard)", recorder.Code, http.StatusForbidden)
	}
}

func TestGuardDisabledWhenNoListenAddr(t *testing.T) {
	st := store.NewTestStore(t, store.Seed())
	handler := handlerForTest(Deps{Version: "t", Store: st, Heartbeat: time.Minute, Notifications: notify.NewHub()})

	request := httptest.NewRequest(http.MethodGet, "/api/hello", nil)
	request.Host = "example.com"
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (guard should be disabled without ListenAddr)", recorder.Code, http.StatusOK)
	}
}

// TestHandlerWithdrawsHandoutsOnPublicBind pins the PublicBind wiring at the
// composition layer, which api.Handler's own comment calls the easy thing to get
// wrong. Both `!deps.PublicBind` and `deps.PublicBind` could be replaced by a constant
// without any test noticing: the middleware tests pass their arguments by hand and no
// suite starts a public-bind daemon.
func TestHandlerWithdrawsHandoutsOnPublicBind(t *testing.T) {
	const token = "public-bind-token"
	st := store.NewTestStore(t, store.Seed())
	handler := handlerForTest(Deps{
		Version:       "t",
		Store:         st,
		Heartbeat:     time.Minute,
		Notifications: notify.NewHub(),
		ListenAddr:    "0.0.0.0:8420",
		APIToken:      token,
		PublicBind:    true,
	})

	// The document bootstrap: withheld even for a loopback peer on a loopback Host,
	// because on a network bind such a peer may be a relaying reverse proxy.
	document := httptest.NewRequest(http.MethodGet, "/", nil)
	document.RemoteAddr = "127.0.0.1:51234"
	document.Host = "127.0.0.1:8420"
	document.Header.Set("Accept", "text/html")
	documentRecorder := httptest.NewRecorder()
	handler.ServeHTTP(documentRecorder, document)
	if got := setCookieValue(documentRecorder, cookieName); got != "" {
		t.Errorf("document bootstrapped the token (%q) on a public bind", got)
	}

	// The /api 401 self-heal: same boundary.
	api := httptest.NewRequest(http.MethodGet, "/api/hello", nil)
	api.RemoteAddr = "127.0.0.1:51234"
	api.Host = "127.0.0.1:8420"
	api.Header.Set("Sec-Fetch-Site", "same-origin")
	api.AddCookie(&http.Cookie{Name: cookieName, Value: "stale"})
	apiRecorder := httptest.NewRecorder()
	handler.ServeHTTP(apiRecorder, api)
	if apiRecorder.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", apiRecorder.Code, http.StatusUnauthorized)
	}
	if got := setCookieValue(apiRecorder, cookieName); got != "" {
		t.Errorf("401 re-issued the token (%q) on a public bind", got)
	}
}

// TestIsLoopbackHostnameMatchesNetIPIsLoopback pins the guard's notion of "loopback
// literal" to net.IP.IsLoopback, which is what cli.classifyBind uses to decide a
// bind is loopback. The two drifting apart is not a cosmetic inconsistency: any
// address classifyBind accepts but this rejects starts a daemon that serves no token
// cookie and 403s every /api request, with no refusal or warning at startup.
func TestIsLoopbackHostnameMatchesNetIPIsLoopback(t *testing.T) {
	for _, host := range []string{
		"127.0.0.1", "127.0.0.2", "127.1.2.3", "127.255.255.254",
		"::1", "0:0:0:0:0:0:0:1",
		"126.255.255.255", "128.0.0.1", "192.168.1.5", "0.0.0.0", "::",
	} {
		want := net.ParseIP(host).IsLoopback()
		if got := isLoopbackHostname(host); got != want {
			t.Errorf("isLoopbackHostname(%q) = %v, want %v (net.IP.IsLoopback)", host, got, want)
		}
	}
	// "localhost" is the one accepted value that is a name, not an IP literal, so it
	// cannot be derived from net.IP and is checked separately.
	if !isLoopbackHostname("localhost") {
		t.Error(`isLoopbackHostname("localhost") = false, want true`)
	}
}

// TestIsLoopbackHostnameRejectsAliases pins the *deliberate* half of the divergence
// from classifyBind: a name that resolves to loopback is still refused, because
// resolving names is what the rebinding defense refuses to do. The previous test
// walks only IP literals, so it stayed green while this axis was unpinned — and the
// resulting gap (a bind on such an alias starting clean and then 403ing everything)
// is reported by serve.go via GuardAcceptsHost, not fixed by widening this.
func TestIsLoopbackHostnameRejectsAliases(t *testing.T) {
	for _, host := range []string{"ip6-localhost", "localhost.localdomain", "loopback", "my-dev-box"} {
		if isLoopbackHostname(host) {
			t.Errorf("isLoopbackHostname(%q) = true, want false: names must not be resolved", host)
		}
	}
}

// TestSameOriginAndOriginAllowedSharePortRule pins what the two guard modes have in
// common: the port decision, which both take from samePortAsRequest. Ports drifting
// apart is how "Origin: https://example.com" became a 403 against
// "Host: example.com:443" on a public bind while the loopback path accepted the
// equivalent pair.
//
// The hostnames are deliberately NOT shared, so this asserts agreement only for pairs
// whose Origin hostname is the request's own — see the difference pinned below. A blanket
// "the two always agree" assertion would be false: it passes only for a hand-picked
// table and breaks on the loopback-alias Origin the api e2e requires to work.
func TestSameOriginAndOriginAllowedSharePortRule(t *testing.T) {
	tests := []struct {
		origin       string
		requestHost  string
		wantSamePort bool
	}{
		{"http://127.0.0.1:8420", "127.0.0.1:8420", true},
		{"https://localhost", "localhost:443", true},
		{"http://localhost", "localhost:80", true},
		{"https://localhost", "localhost:80", false},
		{"https://localhost", "localhost", true},
		{"https://localhost:443", "localhost", false},
		{"http://localhost:8420", "localhost:9999", false},
		// Ports compare as strings, so a non-canonical spelling does not match.
		{"http://localhost:8420", "localhost:08420", false},
		{"http://[::1]:8420", "[::1]:8420", true},
	}
	for _, tt := range tests {
		parsed, err := url.Parse(tt.origin)
		if err != nil {
			t.Fatalf("url.Parse(%q): %v", tt.origin, err)
		}
		if got := samePortAsRequest(parsed, tt.requestHost); got != tt.wantSamePort {
			t.Errorf("samePortAsRequest(%q, %q) = %v, want %v", tt.origin, tt.requestHost, got, tt.wantSamePort)
		}
		// Both modes must reach that same answer for a matching hostname, rather than
		// each carrying its own port rule.
		if got := originAllowed(tt.origin, tt.requestHost); got != tt.wantSamePort {
			t.Errorf("originAllowed(%q, %q) = %v, want the shared port rule's %v",
				tt.origin, tt.requestHost, got, tt.wantSamePort)
		}
		if got := sameOrigin(tt.origin, tt.requestHost); got != tt.wantSamePort {
			t.Errorf("sameOrigin(%q, %q) = %v, want the shared port rule's %v",
				tt.origin, tt.requestHost, got, tt.wantSamePort)
		}
	}

	// The deliberate difference: a *different* loopback literal on the same port is a
	// loopback Origin (strict mode's rule) but not the request's own host (relaxed
	// mode's), and the api e2e requires the strict answer.
	if !originAllowed("http://localhost:8420", "127.0.0.1:8420") {
		t.Error("originAllowed rejected a loopback-alias Origin; the strict path accepts any loopback literal")
	}
	if sameOrigin("http://localhost:8420", "127.0.0.1:8420") {
		t.Error("sameOrigin accepted an Origin that is not the request's own host")
	}
}

// TestGuardAcceptsHost covers the seam serve.go uses to detect that its own bind
// address would be rejected by the guard on every request.
func TestGuardAcceptsHost(t *testing.T) {
	tests := map[string]bool{
		"127.0.0.1:8420":     true,
		"localhost:8420":     true,
		"127.0.0.2:8420":     true,
		"[::1]:8420":         true,
		"127.0.0.1":          true,
		"ip6-localhost:8420": false,
		"my-dev-box:8420":    false,
		"192.168.1.5:8420":   false,
		"0.0.0.0:8420":       false,
	}
	for addr, want := range tests {
		if got := GuardAcceptsHost(addr); got != want {
			t.Errorf("GuardAcceptsHost(%q) = %v, want %v", addr, got, want)
		}
	}
}

// TestGuardSurvivesPortForward is the regression test for the guard being usable
// through "ssh -L 9999:localhost:8420": the browser then addresses the daemon on a
// port it does not itself listen on, and every /api call must still pass.
func TestGuardSurvivesPortForward(t *testing.T) {
	handler := guardedHandler(t, "127.0.0.1:8420")

	forwarded := httptest.NewRequest(http.MethodGet, "/api/hello", nil)
	forwarded.Host = "localhost:9999"
	forwarded.Header.Set("Origin", "http://localhost:9999")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, forwarded)
	if recorder.Code != http.StatusOK {
		t.Fatalf("forwarded-port status = %d, want %d", recorder.Code, http.StatusOK)
	}
}

// flushRecorder is a ResponseWriter that also implements http.Flusher, so the
// guard's next handler can verify the Flusher passes through unwrapped.
type flushRecorder struct {
	*httptest.ResponseRecorder
	flushed bool
}

func (f *flushRecorder) Flush() { f.flushed = true }

func TestGuardPreservesFlusher(t *testing.T) {
	var sawFlusher bool
	next := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, sawFlusher = writer.(http.Flusher)
	})

	guard := guardLoopback("127.0.0.1:8420", false, next)
	request := httptest.NewRequest(http.MethodGet, "/api/hello", nil)
	request.Host = "127.0.0.1:8420"
	recorder := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
	guard.ServeHTTP(recorder, request)

	if !sawFlusher {
		t.Error("next handler did not see an http.Flusher: the guard must not wrap the ResponseWriter")
	}
}
