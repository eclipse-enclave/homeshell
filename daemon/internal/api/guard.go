package api

import (
	"net"
	"net/http"
	"net/url"
	"strings"
)

// guardLoopback defends the loopback /api surface against DNS-rebinding and
// cross-origin/CSRF abuse without any client cooperation: it rejects a request
// whose Host header is not a loopback literal, and a request that carries an
// Origin that is not a loopback origin on the port the request itself was
// addressed to. It is not authentication (no token) — see docs/API.md, "Origin guard".
//
// When publicBind is set — the daemon really is listening on a network-reachable
// address, which serve.go derives rather than reading off the --allow-public flag —
// the Host allow-list is skipped and the Origin check is generalized to same-origin:
// a present Origin must match the request's own Host, but the daemon may live on any
// address. The API token then carries the access-control weight; this only trades
// away the rebinding defense the strict mode provides on loopback.
//
// An empty listenAddr disables the guard entirely, which is how the api unit tests
// (driving httptest requests with a synthetic Host) pass unchanged; production always
// supplies the resolved listener address. Nothing else is inspected — the Host and
// Origin rules are per-request and derive nothing from the listen address — so this
// deliberately does not also disable on an unparseable value: a ListenAddr of
// "127.0.0.1" (no port) would then silently switch off both the rebinding and the
// CSRF check.
func guardLoopback(listenAddr string, publicBind bool, next http.Handler) http.Handler {
	if listenAddr == "" {
		return next // disabled: no resolved listen address to guard
	}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if publicBind {
			if origin := request.Header.Get("Origin"); origin != "" && !sameOrigin(origin, request.Host) {
				jsonError(writer, "forbidden: cross-origin request rejected", http.StatusForbidden)
				return
			}
			next.ServeHTTP(writer, request)
			return
		}
		if !hostAllowed(request.Host) {
			jsonError(writer, "forbidden: host not allowed", http.StatusForbidden)
			return
		}
		if origin := request.Header.Get("Origin"); origin != "" && !originAllowed(origin, request.Host) {
			jsonError(writer, "forbidden: cross-origin request rejected", http.StatusForbidden)
			return
		}
		next.ServeHTTP(writer, request)
	})
}

// sameOrigin reports whether a present Origin belongs to the same host and port the
// request was addressed to (request.Host). Only http/https origins that parse and match
// are accepted; anything else (a genuinely foreign origin, an opaque "null", a non-http
// scheme) is rejected. Used only in the publicBind relaxation, where the daemon may bind
// any address.
//
// Host and port are compared through samePortAsRequest and requestHostname, shared with
// the loopback path so the *port* rule cannot drift between the two modes — the hostname
// rules differ by design. The hostname comparison is what refuses a genuinely foreign
// origin, and it is deliberately case-insensitive and port-free; comparing Origin.Host
// against request.Host as one string would over-restrict a scheme-default port
// ("Origin: https://example.com" against "Host: example.com:443").
func sameOrigin(origin, requestHost string) bool {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return false
	}
	if !strings.EqualFold(parsed.Hostname(), requestHostname(requestHost)) {
		return false
	}
	return samePortAsRequest(parsed, requestHost)
}

// unbracket strips exactly one surrounding pair of square brackets from a
// bracketed IPv6 literal (e.g. "[::1]" -> "::1"). It leaves a header that is not
// a full bracketed pair untouched, so a malformed value like "127.0.0.1]" is not
// silently normalized to a loopback literal.
func unbracket(host string) string {
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		return strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	}
	return host
}

// isLoopbackHostname reports whether host is a loopback IP literal or the
// "localhost" name, case-insensitively.
//
// On IP literals it must keep agreeing with what cli.classifyBind accepts as a
// loopback bind (net.IP.IsLoopback); anything it rejects that classifyBind accepts
// is a daemon that starts clean and then 403s every request. On *names* the two
// deliberately differ — classifyBind resolves them, this must not, because
// re-resolving a name is precisely what the rebinding defense refuses. Do not
// "fix" that by widening this; the gap is reported at startup instead, see
// GuardAcceptsHost. Rationale in docs/API.md, "Origin guard".
func isLoopbackHostname(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// GuardAcceptsHost reports whether the /api guard will accept requests addressed
// to hostHeader (a Host header value, with or without a port). It exists so
// serve.go can check its own bind address against the guard's rule instead of
// keeping a second copy of it: a bind host that resolves to loopback but is not a
// literal — an /etc/hosts alias such as "ip6-localhost" — starts fine and is then
// rejected on every request, CLI included, so the daemon needs to say so at startup.
func GuardAcceptsHost(hostHeader string) bool {
	return hostAllowed(hostHeader)
}

// hostAllowed accepts a Host that is a loopback literal, on any port. The
// hostname literal alone is what defeats DNS-rebinding: a rebound name arrives
// as "Host: attacker.example" and no re-resolution can turn it into "localhost".
// The port deliberately does not have to be the daemon's own, so the UI keeps
// working when the daemon is reached through a loopback port-forward or proxy
// (e.g. "ssh -L 9999:localhost:8420" sends "Host: localhost:9999"). Guarding
// against a *local* page on another loopback port is the Origin check's job, and
// it does that per-request rather than against the daemon's port — see
// originAllowed.
func hostAllowed(hostHeader string) bool {
	return isLoopbackHostname(requestHostname(hostHeader))
}

// requestHostname returns the host a Host header addresses, without its port and
// without the brackets around an IPv6 literal. Both guard modes derive the hostname
// through it, so the strict and the same-origin path cannot disagree about what a
// given Host header means.
func requestHostname(hostHeader string) string {
	host, _, err := net.SplitHostPort(hostHeader)
	if err != nil { // no port present
		return unbracket(hostHeader)
	}
	return host
}

// originAllowed accepts a loopback Origin that addresses the same port the request
// itself was addressed to. Requiring the ports to agree — rather than both to equal
// the daemon's — is what makes the guard survive a port-forward while still refusing
// a page served from another loopback port: "hs_api_token" is SameSite=Strict but
// localhost:3000 and localhost:8420 are the same *site*, so the cookie rides along
// on such a request and this check is the CSRF defense.
//
// Both http and https schemes are accepted: the daemon serves plain http, but a
// request may legitimately carry an https Origin when fronted by a TLS-terminating
// proxy on loopback. A userinfo component is rejected outright — url.Parse puts
// "evil.com" in User for "http://evil.com@127.0.0.1:8420" and reports the loopback
// literal as the Hostname, so a Hostname check alone would accept it.
//
// The port rule is samePortAsRequest, shared with the same-origin path.
func originAllowed(origin, requestHost string) bool {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return false
	}
	if !isLoopbackHostname(parsed.Hostname()) {
		return false
	}
	return samePortAsRequest(parsed, requestHost)
}

// samePortAsRequest reports whether an already-parsed Origin addresses the same port the
// request itself was addressed to. There are two cases, because an omitted port is
// scheme-relative:
//
//   - Host carries a port: compare it with the Origin's, substituting the Origin
//     scheme's default when that is omitted (443 for https, 80 for http), so
//     "Origin: https://localhost" matches "Host: localhost:443" and not ":8420".
//   - Host carries no port: something in front stripped it, so the client's port is
//     whatever that indirection listens on and the daemon cannot know it. Accept an
//     Origin that likewise omits its port — either scheme, this is the
//     TLS-proxy-on-443 case — and reject an explicit one.
//
// The second case leaves one pair undecidable: on a portless Host an http and an https
// Origin are equally plausible, so both are accepted. Narrowing that by the daemon's own
// port was tried and reverted twice: a daemon on 80 can sit behind a 443 proxy as easily.
// What covers it instead is schemeful same-site (the SameSite=Strict cookie is not sent
// across schemes), which Chrome enforces and Firefox by default does not.
func samePortAsRequest(parsed *url.URL, requestHost string) bool {
	if _, requestPort, err := net.SplitHostPort(requestHost); err == nil {
		return originPort(parsed) == requestPort
	}
	return parsed.Port() == ""
}

// originPort returns the port an Origin addresses, substituting the scheme's default
// when it is omitted. Its result is compared as a string by samePortAsRequest, and that
// is deliberate: a numeric comparison would make a non-canonical *Host* port match
// ("Host: localhost:08420" or ":+8420" against "Origin: .../:8420", since
// strconv.Atoi accepts both spellings), i.e. it only ever widens the predicate. No
// browser can benefit, because a browser derives both the Host header and the Origin
// from a parsed URL, and URL parsing canonicalizes the port in it (WHATWG:
// new URL("http://localhost:08420").host is "localhost:8420").
func originPort(parsed *url.URL) string {
	if port := parsed.Port(); port != "" {
		return port
	}
	if parsed.Scheme == "https" {
		return "443"
	}
	return "80"
}
