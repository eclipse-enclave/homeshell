package api

import (
	"crypto/subtle"
	"net"
	"net/http"
	"strings"
)

// cookieName is the cookie the daemon sets on served web-UI responses so a
// browser (whose fetch/EventSource cannot attach an Authorization header
// transparently) authenticates to /api without any web-UI source changes. It
// is HttpOnly and SameSite=Strict; see issueTokenCookie and docs/API.md, "Authentication".
const cookieName = "hs_api_token"

// requireToken gates the wrapped handler behind a shared secret. A request must
// present the token either as an "Authorization: Bearer <token>" header
// (case-insensitive scheme) or as the hs_api_token cookie; the presented value
// is compared to the expected one in constant time. A missing or mismatched
// credential is answered with 401 and the token is never echoed.
//
// A 401 re-issues the fresh hs_api_token cookie via setTokenCookie before the
// status is written, but only when bootstrapCookie is set (see issueTokenCookie
// for that boundary) AND the request already presented an hs_api_token cookie.
// That self-heals a browser whose cookie went stale after a daemon restart
// rotated the per-startup token (e.g. `homeshell update`): an already open web UI
// keeps sending the old cookie on its fetch/EventSource calls and would 401
// forever until a manual reload.
//
// The re-issue needs all four of bootstrapCookie, a loopback peer, an existing
// non-empty hs_api_token cookie, and same-origin fetch metadata. Two of those are
// easy to misread:
//
//   - hasTokenCookie keeps the re-issue off the *zero-credential* path, but it is
//     emphatically NOT a credential check: a page can mint an hs_api_token cookie for
//     the daemon's host in one line of JavaScript, because cookies are not port-scoped
//     and HttpOnly only stops it *overwriting* one. That is why the other three apply.
//   - sameOriginFetchSite is what stops such a page triggering a 401 with an <img> and
//     collecting the real token from the Set-Cookie. Its request is same-*site* but not
//     same-origin, and no browser can forge the difference, while the web UI's own
//     fetch/EventSource calls are same-origin (measured, including through a
//     port-forward).
//
// A request with **no** fetch metadata passes, so the gate fails open for a non-browser
// client and for Firefox before 90 / Safari before 16.4.
//
// What this gate does not cover — a hostile same-host page can still reach the token
// through a *navigation* the document bootstrap serves — and why that is accepted are in
// docs/API.md, "Authentication", together with the deployment caveat that follows from it.
//
// Unlike issueTokenCookie this has no Host check of its own: it runs inside
// guardLoopback, which rejects a rebound Host with a 403 before the re-issue can
// happen. That makes the property hold by composition — keep this handler inside
// the guard, or the document bootstrap's rebinding fix has to be repeated here.
//
// When expectedToken is empty the guard is disabled and requests pass through
// unchanged — the same disabled seam guardLoopback uses for an empty ListenAddr,
// which keeps the api unit tests (built without a token) working.
func requireToken(expectedToken string, bootstrapCookie bool, next http.Handler) http.Handler {
	if expectedToken == "" {
		return next
	}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !authenticated(request, expectedToken) {
			if bootstrapCookie && hasTokenCookie(request) &&
				peerIsLoopback(request) && sameOriginFetchSite(request) {
				setTokenCookie(writer, expectedToken)
			}
			// RFC 9110 §11.6.1 requires a challenge on a 401. "Bearer" names the
			// scheme without triggering a browser credential dialog the way "Basic"
			// would, so the rejection is self-describing for HTTP clients and the web
			// UI's own 401s stay invisible to the user.
			writer.Header().Set("WWW-Authenticate", "Bearer")
			jsonError(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(writer, request)
	})
}

// authenticated reports whether the request presents the expected token, comparing
// in constant time. An "Authorization: Bearer <value>" header is the credential when
// one is usable; a header that is not (wrong scheme, empty bearer value) falls
// through to the cookies rather than short-circuiting, so a valid cookie still
// authenticates alongside an unrelated Authorization header.
//
// EVERY hs_api_token cookie on the request is checked, not just the first. Cookies
// are not port-scoped, so a page on another loopback port can create a *second*
// hs_api_token — HttpOnly stops it overwriting the daemon's Path=/ cookie but not
// adding its own at Path=/api, which RFC 6265 orders first — and a first-match-only
// check would let it shadow the real value and lock the UI out at 401 permanently,
// self-heal included, with nothing in the request to explain why.
func authenticated(request *http.Request, expectedToken string) bool {
	if header := request.Header.Get("Authorization"); header != "" {
		const scheme = "bearer "
		if len(header) >= len(scheme) && strings.EqualFold(header[:len(scheme)], scheme) {
			if token := strings.TrimSpace(header[len(scheme):]); token != "" {
				return tokenEqual(token, expectedToken)
			}
		}
		// otherwise: not a usable Bearer token — fall through to the cookies.
	}
	for _, value := range tokenCookieValues(request) {
		if tokenEqual(value, expectedToken) {
			return true
		}
	}
	return false
}

// tokenEqual compares a presented credential with the expected one in constant time.
// An empty presented value never matches, so a missing credential cannot pass when
// auth is enabled (requireToken already short-circuits on an empty expected token).
func tokenEqual(presented, expected string) bool {
	if presented == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(presented), []byte(expected)) == 1
}

// tokenCookieValues returns every non-empty hs_api_token value on the request. See
// authenticated for why all of them matter rather than just the first.
func tokenCookieValues(request *http.Request) []string {
	var values []string
	for _, cookie := range request.Cookies() {
		if cookie.Name == cookieName && cookie.Value != "" {
			values = append(values, cookie.Value)
		}
	}
	return values
}

// hasTokenCookie reports whether the request carries an hs_api_token cookie with a
// non-empty value. The value check is the point: http.Request.Cookie returns a nil
// error for "Cookie: hs_api_token=", so testing only for the cookie's presence would
// let a caller unlock the 401 self-heal with an empty value it did not have to know —
// exactly the zero-credential handout the self-heal is narrowed to avoid.
func hasTokenCookie(request *http.Request) bool {
	return len(tokenCookieValues(request)) > 0
}

// issueTokenCookie sets the hs_api_token cookie on responses served by the
// wrapped handler (the embedded web UI in production), so a browser loading the
// document from the daemon then authenticates its /api calls transparently. The
// cookie is HttpOnly (JS cannot read it) and SameSite=Strict (not sent on
// cross-site requests); it carries no Secure attribute because the daemon serves
// plain http over loopback.
//
// A Set-Cookie header is readable by ANY caller (HttpOnly stops browser
// JavaScript, not a plain HTTP client such as curl, and it never stops the browser
// from *sending* the cookie onward), so the bootstrap is withheld unless all four
// hold: a token is configured, the bind is not public (publicBind is false), the peer is
// loopback, and the request's Host is a loopback literal. The reasoning behind
// each, and what the loopback path deliberately gives up, is in docs/API.md, "Authentication".
//
//   - publicBind: withheld for everyone on a network bind, local peers included —
//     a loopback peer there may be a reverse proxy that relays the header onward.
//   - peerIsLoopback: the property that makes a handout acceptable at all is that
//     the caller is local; this states it rather than inferring it from the bind.
//   - hostAllowed: unlike /api this handler sits outside guardLoopback, so without
//     it a DNS-rebound page (Host: evil.com resolved to 127.0.0.1) collects the
//     cookie under *its own* hostname and the browser then sends the token to the
//     attacker's real server. Reusing the guard's own predicate keeps it
//     port-agnostic, so a loopback port-forward still bootstraps.
func issueTokenCookie(token string, publicBind bool, next http.Handler) http.Handler {
	if token == "" || publicBind {
		return next
	}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		// Only bootstrap on a top-level document navigation: a GET whose Accept mentions
		// text/html and which, if it says so at all, says it is fetching a document.
		// Setting the cookie on every static asset (JS/CSS/images) is redundant since
		// the navigation already seeds it, and the /api 401 self-heal covers a later
		// token rotation.
		//
		// Sec-Fetch-Dest is what actually distinguishes a navigation from a script's
		// fetch(): Accept is CORS-safelisted, so a hostile page can send
		// "Accept: text/html" on a no-cors GET and — with only the Accept and method
		// checks — have the token planted as a cookie for 127.0.0.1 in a visitor who
		// never opened HomeShell. Every browser that can run such a fetch also sends
		// Sec-Fetch-Dest, and cannot forge it. It is absent for non-browser clients
		// (curl, the CLI), which is why an absent value still passes: on loopback that
		// handout is already accepted, deliberately (see below).
		if request.Method == http.MethodGet &&
			strings.Contains(request.Header.Get("Accept"), "text/html") &&
			navigationFetchDest(request) &&
			peerIsLoopback(request) && hostAllowed(request.Host) {
			setTokenCookie(writer, token)
		}
		next.ServeHTTP(writer, request)
	})
}

// sameOriginFetchSite reports whether the request's Sec-Fetch-Site metadata says it came
// from the daemon's own origin. An absent header passes — in practice a non-browser
// client, though see the fail-open note on requireToken — and so does "same-origin";
// "same-site" (another port on the same host), "cross-site" and "none" do not. Ports are
// part of an origin but not of a site, which is precisely the distinction a cookie cannot
// make and this one can.
func sameOriginFetchSite(request *http.Request) bool {
	site := request.Header.Get("Sec-Fetch-Site")
	return site == "" || site == "same-origin"
}

// navigationFetchDest reports whether the request's Sec-Fetch-Dest metadata is
// consistent with a top-level document navigation. An absent header passes — in practice
// a non-browser client, though a browser predating Fetch Metadata omits it too, so this
// fails open there; "document" passes; anything else — "empty" for fetch/XHR, "iframe",
// "image", "script" — does not.
func navigationFetchDest(request *http.Request) bool {
	dest := request.Header.Get("Sec-Fetch-Dest")
	return dest == "" || dest == "document"
}

// peerIsLoopback reports whether the request's transport peer is a loopback
// address. It reads request.RemoteAddr (the real TCP peer), never a forwarded-for
// header — a client-supplied header must not be able to claim locality.
//
// This assumes a TCP listener, which is what serve.go creates: RemoteAddr is then
// an ip:port. Were the daemon ever to also listen on a unix socket, RemoteAddr
// would be a socket path, ParseIP would fail and this would report false — safe
// (the cookie is withheld) but wrong, so teach it about that case rather than
// wondering why the browser stopped authenticating. Note also that
// httptest.NewRequest defaults RemoteAddr to a non-loopback documentation
// address, so handler tests that expect the cookie must set it explicitly.
func peerIsLoopback(request *http.Request) bool {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		host = request.RemoteAddr
	}
	ip := net.ParseIP(unbracket(host))
	return ip != nil && ip.IsLoopback()
}

// setTokenCookie writes the hs_api_token cookie hardening it identically wherever
// it is issued (the document bootstrap and the /api 401 self-heal): HttpOnly (JS
// cannot read it), SameSite=Strict (not sent cross-site), Path "/" (covers /api),
// and no Secure attribute (the daemon serves plain http over loopback). It must
// be called before WriteHeader, as Set-Cookie is a response header.
func setTokenCookie(writer http.ResponseWriter, token string) {
	http.SetCookie(writer, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}
