package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// okHandler is a trivial next handler that records it was reached.
func okHandler(reached *bool) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		*reached = true
		writer.WriteHeader(http.StatusOK)
	})
}

func TestRequireToken(t *testing.T) {
	const expected = "s3cr3t-token-value"
	const loopbackPeer = "127.0.0.1:51234"
	tests := []struct {
		name       string
		expected   string
		bootstrap  bool
		remoteAddr string
		authHeader string
		cookie     string // hs_api_token cookie value; "" means no cookie
		wantStatus int
		wantNext   bool
		// wantCookie is the expected re-issued hs_api_token cookie value on the
		// response; "" means no Set-Cookie is expected. The 401 re-issue is the
		// stale-cookie self-heal, so it needs all three: the bootstrap boundary
		// (loopback bind), a loopback peer, and a cookie already on the request.
		wantCookie string
	}{
		{"valid bearer", expected, true, loopbackPeer, "Bearer " + expected, "", http.StatusOK, true, ""},
		{"bearer scheme case-insensitive", expected, true, loopbackPeer, "bEaReR " + expected, "", http.StatusOK, true, ""},
		{"wrong bearer no cookie withholds the token", expected, true, loopbackPeer, "Bearer nope", "", http.StatusUnauthorized, false, ""},
		{"same-length wrong token", expected, true, loopbackPeer, "Bearer s3cr3t-token-valu3", "", http.StatusUnauthorized, false, ""},
		{"missing credential withholds the token", expected, true, loopbackPeer, "", "", http.StatusUnauthorized, false, ""},
		{"malformed authorization no scheme falls through to (absent) cookie", expected, true, loopbackPeer, expected, "", http.StatusUnauthorized, false, ""},
		{"non-bearer scheme falls through to (absent) cookie", expected, true, loopbackPeer, "Basic " + expected, "", http.StatusUnauthorized, false, ""},
		{"non-bearer scheme falls through to valid cookie", expected, true, loopbackPeer, "Basic x", expected, http.StatusOK, true, ""},
		{"empty bearer falls through to valid cookie", expected, true, loopbackPeer, "Bearer ", expected, http.StatusOK, true, ""},
		{"valid cookie", expected, true, loopbackPeer, "", expected, http.StatusOK, true, ""},
		{"stale cookie self-heals", expected, true, loopbackPeer, "", "nope", http.StatusUnauthorized, false, expected},
		{"stale cookie alongside a wrong bearer self-heals", expected, true, loopbackPeer, "Bearer nope", "nope", http.StatusUnauthorized, false, expected},
		{"header preferred over cookie", expected, true, loopbackPeer, "Bearer " + expected, "nope", http.StatusOK, true, ""},
		{"stale cookie from a remote peer withholds the token", expected, true, "203.0.113.7:51234", "", "nope", http.StatusUnauthorized, false, ""},
		// A made-up cookie is not a credential, so on a public bind the bind gate —
		// not the cookie check — is what keeps the token in.
		{"forged cookie on a public bind withholds the token", expected, false, loopbackPeer, "", "nope", http.StatusUnauthorized, false, ""},
		{"missing credential on a public bind withholds the token", expected, false, loopbackPeer, "", "", http.StatusUnauthorized, false, ""},
		{"disabled empty expected", "", true, loopbackPeer, "", "", http.StatusOK, true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var reached bool
			handler := requireToken(tt.expected, tt.bootstrap, okHandler(&reached))
			request := httptest.NewRequest(http.MethodGet, "/api/hello", nil)
			request.RemoteAddr = tt.remoteAddr
			if tt.authHeader != "" {
				request.Header.Set("Authorization", tt.authHeader)
			}
			if tt.cookie != "" {
				request.AddCookie(&http.Cookie{Name: cookieName, Value: tt.cookie})
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)

			if recorder.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", recorder.Code, tt.wantStatus)
			}
			if reached != tt.wantNext {
				t.Errorf("next reached = %v, want %v", reached, tt.wantNext)
			}
			if got := setCookieValue(recorder, cookieName); got != tt.wantCookie {
				t.Errorf("re-issued %s cookie = %q, want %q", cookieName, got, tt.wantCookie)
			}
		})
	}
}

// TestRequireTokenEmptyCookieValueWithholdsToken is the regression test for the
// 401 self-heal being unlockable with a cookie the caller only had to name:
// http.Request.Cookie returns a nil error for "Cookie: hs_api_token=", so checking
// the cookie's presence rather than its value let a zero-credential request read
// the real token out of the re-issued Set-Cookie header. The table above cannot
// express this case — it adds a cookie only for a non-empty value — so the raw
// header is set here.
func TestRequireTokenEmptyCookieValueWithholdsToken(t *testing.T) {
	const expected = "s3cr3t-token-value"
	for _, header := range []string{cookieName + "=", cookieName + `=""`, cookieName + "=; other=x"} {
		var reached bool
		handler := requireToken(expected, true, okHandler(&reached))
		request := httptest.NewRequest(http.MethodGet, "/api/hello", nil)
		request.RemoteAddr = "127.0.0.1:51234"
		request.Header.Set("Cookie", header)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusUnauthorized {
			t.Errorf("Cookie %q: status = %d, want %d", header, recorder.Code, http.StatusUnauthorized)
		}
		if got := setCookieValue(recorder, cookieName); got != "" {
			t.Errorf("Cookie %q: re-issued the token (%q) to a caller that presented no value", header, got)
		}
	}
}

// setCookieValue returns the value of the named cookie the response set, or ""
// when no such Set-Cookie header is present.
func setCookieValue(recorder *httptest.ResponseRecorder, name string) string {
	for _, c := range recorder.Result().Cookies() {
		if c.Name == name {
			return c.Value
		}
	}
	return ""
}

// TestRequireTokenIgnoresShadowCookie is the regression test for a permanent 401
// lockout. Cookies are not port-scoped, so a page on another loopback port can add a
// second hs_api_token at a longer path — HttpOnly stops it overwriting the daemon's,
// not adding its own — and RFC 6265 orders longer paths first. Checking only the first
// cookie let that shadow the real value forever, self-heal included.
func TestRequireTokenIgnoresShadowCookie(t *testing.T) {
	const expected = "s3cr3t-token-value"
	headers := []string{
		"hs_api_token=shadow; hs_api_token=" + expected,
		"hs_api_token=" + expected + "; hs_api_token=shadow",
		"hs_api_token=; hs_api_token=" + expected,
		"other=x; hs_api_token=shadow; hs_api_token=" + expected + "; more=y",
	}
	for _, header := range headers {
		var reached bool
		handler := requireToken(expected, true, okHandler(&reached))
		request := httptest.NewRequest(http.MethodGet, "/api/hello", nil)
		request.RemoteAddr = "127.0.0.1:51234"
		request.Header.Set("Cookie", header)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusOK {
			t.Errorf("Cookie %q: status = %d, want %d (a shadow cookie must not lock the real one out)", header, recorder.Code, http.StatusOK)
		}
	}
}

// TestRequireTokenSelfHealNeedsSameOriginFetch is the regression test for the one path
// by which a page on another loopback port could obtain the real token. Cookies are not
// port-scoped, so such a page mints `hs_api_token=x` for the daemon's host in one line
// of JavaScript, triggers a 401 with an <img> — no Origin, loopback Host, so the guard
// passes and hasTokenCookie is satisfied — and reads the re-issued token out of the
// Set-Cookie the browser then sends to its own server. Same-*site* requests carry
// Sec-Fetch-Site: same-site, which a browser cannot forge; the web UI's own
// fetch/EventSource calls are same-origin.
func TestRequireTokenSelfHealNeedsSameOriginFetch(t *testing.T) {
	const expected = "s3cr3t-token-value"
	tests := map[string]string{
		"same-origin": expected, // the web UI itself: must self-heal
		"":            expected, // no fetch metadata: not a browser
		"same-site":   "",       // another port on this host: the attack
		"cross-site":  "",
		"none":        "",
	}
	for site, wantCookie := range tests {
		var reached bool
		handler := requireToken(expected, true, okHandler(&reached))
		request := httptest.NewRequest(http.MethodGet, "/api/hello", nil)
		request.RemoteAddr = "127.0.0.1:51234"
		request.AddCookie(&http.Cookie{Name: cookieName, Value: "minted-by-the-attacker"})
		if site != "" {
			request.Header.Set("Sec-Fetch-Site", site)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusUnauthorized {
			t.Errorf("Sec-Fetch-Site %q: status = %d, want %d", site, recorder.Code, http.StatusUnauthorized)
		}
		if got := setCookieValue(recorder, cookieName); got != wantCookie {
			t.Errorf("Sec-Fetch-Site %q: re-issued cookie = %q, want %q", site, got, wantCookie)
		}
	}
}

// TestHasTokenCookie pins the self-heal gate independently of the auth path: it must
// see a real value hidden behind a shadow or an empty one, and must not fire on a
// request that presents no value at all.
func TestHasTokenCookie(t *testing.T) {
	tests := map[string]bool{
		"":                                    false,
		"other=x":                             false,
		"hs_api_token=":                       false,
		`hs_api_token=""`:                     false,
		"hs_api_token=; hs_api_token=":        false,
		"hs_api_token=x":                      true,
		"hs_api_token=; hs_api_token=x":       true,
		"hs_api_token=shadow; hs_api_token=x": true,
		"other=x; hs_api_token=y; another=z":  true,
		"HS_API_TOKEN=x":                      false, // cookie names are case-sensitive
	}
	for header, want := range tests {
		request := httptest.NewRequest(http.MethodGet, "/api/hello", nil)
		if header != "" {
			request.Header.Set("Cookie", header)
		}
		if got := hasTokenCookie(request); got != want {
			t.Errorf("hasTokenCookie(Cookie: %q) = %v, want %v", header, got, want)
		}
	}
}

// TestRequireTokenSendsChallenge covers RFC 9110 §11.6.1: a 401 carries a
// WWW-Authenticate challenge naming the scheme. "Bearer" is deliberate — "Basic"
// would make the browser pop a credential dialog on the web UI's own 401s.
func TestRequireTokenSendsChallenge(t *testing.T) {
	var reached bool
	handler := requireToken("s3cr3t", true, okHandler(&reached))
	request := httptest.NewRequest(http.MethodGet, "/api/hello", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
	if got := recorder.Header().Get("WWW-Authenticate"); got != "Bearer" {
		t.Errorf("WWW-Authenticate = %q, want %q", got, "Bearer")
	}
}

// TestRequireTokenNeverEchoesToken guards against leaking the secret in the
// rejection body.
func TestRequireTokenNeverEchoesToken(t *testing.T) {
	const expected = "super-secret"
	var reached bool
	handler := requireToken(expected, true, okHandler(&reached))
	request := httptest.NewRequest(http.MethodGet, "/api/hello", nil)
	request.Header.Set("Authorization", "Bearer "+expected+"-wrong")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
	if body := recorder.Body.String(); strings.Contains(body, expected) {
		t.Errorf("response body leaked the token: %q", body)
	}
}

// navigation builds a top-level document GET from the given transport peer, as a
// local browser would send it. httptest.NewRequest defaults RemoteAddr to a
// non-loopback documentation address and Host to "example.com", neither of which
// the bootstrap accepts, so both are set here; cases that care override them.
func navigation(remoteAddr string) *http.Request {
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.RemoteAddr = remoteAddr
	request.Host = "127.0.0.1:8420"
	request.Header.Set("Accept", "text/html")
	return request
}

func TestIssueTokenCookie(t *testing.T) {
	t.Run("sets a hardened cookie on a loopback document navigation", func(t *testing.T) {
		var reached bool
		handler := issueTokenCookie("the-token", false, okHandler(&reached))
		request := navigation("127.0.0.1:51234")
		request.Header.Set("Accept", "text/html,application/xhtml+xml")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)

		if !reached {
			t.Fatal("next handler was not reached")
		}
		cookies := recorder.Result().Cookies()
		if len(cookies) != 1 {
			t.Fatalf("got %d cookies, want 1", len(cookies))
		}
		cookie := cookies[0]
		if cookie.Name != cookieName {
			t.Errorf("cookie name = %q, want %q", cookie.Name, cookieName)
		}
		if cookie.Value != "the-token" {
			t.Errorf("cookie value = %q, want %q", cookie.Value, "the-token")
		}
		if !cookie.HttpOnly {
			t.Error("cookie is not HttpOnly")
		}
		if cookie.SameSite != http.SameSiteStrictMode {
			t.Errorf("cookie SameSite = %v, want Strict", cookie.SameSite)
		}
		if cookie.Secure {
			t.Error("cookie is Secure, want not Secure (daemon serves plain http over loopback)")
		}
		if cookie.Path != "/" {
			t.Errorf("cookie Path = %q, want /", cookie.Path)
		}
	})

	// A non-navigation request (a static asset fetch: Accept has no text/html) is
	// not bootstrapped — the navigation already seeded the cookie, and the /api
	// 401 self-heal covers a later token rotation.
	t.Run("no cookie on a non-navigation request", func(t *testing.T) {
		for _, accept := range []string{"*/*", "application/json", "text/css,*/*;q=0.1", ""} {
			var reached bool
			handler := issueTokenCookie("the-token", false, okHandler(&reached))
			request := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
			request.RemoteAddr = "127.0.0.1:51234"
			// Loopback Host so this case fails only on the Accept check, not
			// incidentally on the Host one.
			request.Host = "127.0.0.1:8420"
			if accept != "" {
				request.Header.Set("Accept", accept)
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)

			if !reached {
				t.Fatalf("Accept %q: next handler was not reached", accept)
			}
			if cookies := recorder.Result().Cookies(); len(cookies) != 0 {
				t.Errorf("Accept %q: got %d cookies, want 0 on a non-navigation request", accept, len(cookies))
			}
		}
	})

	// A remote peer must NOT be bootstrapped: any unauthenticated caller can read a
	// Set-Cookie header (HttpOnly does not stop a non-browser client), so handing out
	// the token over the network would defeat the bind guard. The peer check states
	// the property that makes the bootstrap acceptable (the caller is local) instead
	// of leaving the bind mode to imply it.
	t.Run("withheld from a non-loopback peer", func(t *testing.T) {
		for _, peer := range []string{"203.0.113.7:51234", "192.168.1.5:51234", "[2001:db8::1]:51234"} {
			var reached bool
			handler := issueTokenCookie("the-token", false, okHandler(&reached))
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, navigation(peer))

			if !reached {
				t.Fatalf("peer %q: next handler was not reached", peer)
			}
			if cookies := recorder.Result().Cookies(); len(cookies) != 0 {
				t.Errorf("peer %q: got %d cookies, want 0 (token must not reach the network)", peer, len(cookies))
			}
		}
	})

	t.Run("issued to an ipv6 loopback peer", func(t *testing.T) {
		var reached bool
		handler := issueTokenCookie("the-token", false, okHandler(&reached))
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, navigation("[::1]:51234"))

		if got := setCookieValue(recorder, cookieName); got != "the-token" {
			t.Errorf("cookie = %q, want %q", got, "the-token")
		}
	})

	// What actually separates a navigation from a script's fetch() is Sec-Fetch-Dest:
	// Accept is CORS-safelisted, so a hostile page can put "text/html" on a no-cors GET
	// and — on the method and Accept checks alone — have the token planted for
	// 127.0.0.1 in a visitor who never opened HomeShell. Absent still passes, for
	// non-browser clients.
	t.Run("withheld from a non-document fetch destination", func(t *testing.T) {
		for _, dest := range []string{"empty", "iframe", "image", "script", "object", "frame"} {
			var reached bool
			handler := issueTokenCookie("the-token", false, okHandler(&reached))
			request := navigation("127.0.0.1:51234")
			request.Header.Set("Sec-Fetch-Dest", dest)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)

			if got := setCookieValue(recorder, cookieName); got != "" {
				t.Errorf("Sec-Fetch-Dest %q: bootstrapped the token (%q)", dest, got)
			}
		}
	})

	t.Run("issued for a document fetch destination", func(t *testing.T) {
		var reached bool
		handler := issueTokenCookie("the-token", false, okHandler(&reached))
		request := navigation("127.0.0.1:51234")
		request.Header.Set("Sec-Fetch-Dest", "document")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)

		if got := setCookieValue(recorder, cookieName); got != "the-token" {
			t.Errorf("cookie = %q, want %q", got, "the-token")
		}
	})

	// The method check narrows this further, though on its own it closes nothing: a
	// cross-origin GET is as easy to issue as a POST.
	t.Run("withheld from a non-GET request", func(t *testing.T) {
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
			var reached bool
			handler := issueTokenCookie("the-token", false, okHandler(&reached))
			request := httptest.NewRequest(method, "/does/not/exist", nil)
			request.RemoteAddr = "127.0.0.1:51234"
			request.Host = "127.0.0.1:8420"
			request.Header.Set("Accept", "text/html")
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)

			if got := setCookieValue(recorder, cookieName); got != "" {
				t.Errorf("%s: bootstrapped the token (%q) on a non-GET request", method, got)
			}
		}
	})

	// The document handler sits outside guardLoopback, so the Host check has to be
	// repeated here: a DNS-rebound page (Host: evil.com re-resolved to 127.0.0.1) is
	// a loopback peer on a loopback bind, and without this it collects the cookie
	// under its own hostname — after which the browser sends the token to the
	// attacker's real server, which HttpOnly does nothing about.
	t.Run("withheld from a rebound Host", func(t *testing.T) {
		for _, host := range []string{"evil.com", "evil.com:8420", "192.168.1.5:8420", "notlocalhost"} {
			var reached bool
			handler := issueTokenCookie("the-token", false, okHandler(&reached))
			request := navigation("127.0.0.1:51234")
			request.Host = host
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)

			if !reached {
				t.Fatalf("Host %q: next handler was not reached (the document must still be served)", host)
			}
			if got := setCookieValue(recorder, cookieName); got != "" {
				t.Errorf("Host %q: bootstrapped the token (%q) for a non-loopback Host", host, got)
			}
		}
	})

	// The Host check reuses the guard's predicate, so it stays port-agnostic and a
	// loopback port-forward still gets a working UI.
	t.Run("issued through a loopback port-forward", func(t *testing.T) {
		var reached bool
		handler := issueTokenCookie("the-token", false, okHandler(&reached))
		request := navigation("127.0.0.1:51234")
		request.Host = "localhost:9999"
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)

		if got := setCookieValue(recorder, cookieName); got != "the-token" {
			t.Errorf("cookie = %q, want %q", got, "the-token")
		}
	})

	// The bind gate is the load-bearing one and is deliberately coarser than the
	// peer check: on a public bind a loopback peer may be a reverse proxy that
	// would relay the Set-Cookie onward, and nothing in the request distinguishes
	// it from a local browser. So public mode withholds the cookie from everyone,
	// local peers included.
	t.Run("withheld on a public bind even from a loopback peer", func(t *testing.T) {
		var reached bool
		handler := issueTokenCookie("the-token", true, okHandler(&reached))
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, navigation("127.0.0.1:51234"))

		if !reached {
			t.Fatal("next handler was not reached")
		}
		if cookies := recorder.Result().Cookies(); len(cookies) != 0 {
			t.Errorf("got %d cookies, want 0 on a public bind (a loopback peer may be a relaying proxy)", len(cookies))
		}
	})

	t.Run("no-op when token empty", func(t *testing.T) {
		var reached bool
		handler := issueTokenCookie("", false, okHandler(&reached))
		request := navigation("127.0.0.1:51234")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)

		if !reached {
			t.Fatal("next handler was not reached")
		}
		if cookies := recorder.Result().Cookies(); len(cookies) != 0 {
			t.Errorf("got %d cookies, want 0 when token is empty", len(cookies))
		}
	})
}

// TestGuardOutsideAuth verifies the composition order: guardLoopback runs before
// requireToken, so a bad Host is a 403 (guard) even without a token, while a good
// Host with no token is a 401 (auth).
func TestGuardOutsideAuth(t *testing.T) {
	const token = "compose-token"
	var reached bool
	handler := guardLoopback("127.0.0.1:8420", false, requireToken(token, true, okHandler(&reached)))

	t.Run("good host no token is 401", func(t *testing.T) {
		reached = false
		request := httptest.NewRequest(http.MethodGet, "/api/hello", nil)
		request.Host = "127.0.0.1:8420"
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
		}
		if reached {
			t.Error("next handler reached without a token")
		}
	})

	// Unlike issueTokenCookie, requireToken has no Host check of its own — it relies
	// on sitting inside guardLoopback, which 403s a rebound Host before the 401
	// self-heal can run. That makes the property hold by *composition*, so assert it
	// on the composed chain: reordering the middleware would otherwise reopen the
	// same leak the document bootstrap was fixed for, silently.
	t.Run("bad host is 403 before auth and re-issues nothing", func(t *testing.T) {
		reached = false
		request := httptest.NewRequest(http.MethodGet, "/api/hello", nil)
		request.RemoteAddr = "127.0.0.1:51234"
		request.Host = "evil.com"
		request.AddCookie(&http.Cookie{Name: cookieName, Value: "stale"})
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusForbidden {
			t.Errorf("status = %d, want %d (guard runs before auth)", recorder.Code, http.StatusForbidden)
		}
		if got := setCookieValue(recorder, cookieName); got != "" {
			t.Errorf("re-issued the token (%q) to a rebound Host: the guard must reject before requireToken", got)
		}
	})

	t.Run("good host valid token passes", func(t *testing.T) {
		reached = false
		request := httptest.NewRequest(http.MethodGet, "/api/hello", nil)
		request.Host = "127.0.0.1:8420"
		request.Header.Set("Authorization", "Bearer "+token)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Errorf("status = %d, want %d", recorder.Code, http.StatusOK)
		}
		if !reached {
			t.Error("next handler not reached with a valid token")
		}
	})
}
