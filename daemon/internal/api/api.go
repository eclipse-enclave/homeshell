// Package api is the daemon's public HTTP surface. Each endpoint lives in its
// own file and self-registers into the endpoint registry from an init(); this
// package assembles the registered routes into the daemon's HTTP handler.
package api

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/agentcatalog"
	"github.com/eclipsesource/homeshell/daemon/internal/filedialog"
	"github.com/eclipsesource/homeshell/daemon/internal/logging"
	"github.com/eclipsesource/homeshell/daemon/internal/notify"
	"github.com/eclipsesource/homeshell/daemon/internal/opener"
	"github.com/eclipsesource/homeshell/daemon/internal/pathutil"
	"github.com/eclipsesource/homeshell/daemon/internal/provider"
	"github.com/eclipsesource/homeshell/daemon/internal/settings"
	"github.com/eclipsesource/homeshell/daemon/internal/store"
	"github.com/eclipsesource/homeshell/daemon/internal/updater"
	"github.com/eclipsesource/homeshell/daemon/internal/webui"
)

// DefaultHeartbeat is how long an SSE stream may stay silent before the daemon
// emits an "i am still alive" heartbeat event.
const DefaultHeartbeat = 10 * time.Second

// Deps is the injection surface every endpoint draws from.
type Deps struct {
	Version string
	// LogLevel is the daemon's live log level, reported by GET /api/config and
	// changed by PATCH /api/config. May be nil in tests.
	LogLevel *logging.Level
	// Logs is the hub GET /api/logs subscribes to for live log records. May be
	// nil; the endpoint then reports that streaming is unavailable.
	Logs *logging.Hub
	// Settings is the settings store backing GET/PUT /api/settings. May be nil;
	// the endpoints then report that settings are unavailable.
	Settings *settings.Store
	// AgentCatalog backs the agent descriptions (GET/POST /api/agents and
	// /api/agent-settings) and the model catalog (/api/agent-models): which agents
	// can be launched, which settings a new session of each offers, and which
	// models each offers. Its own file rather than part of Settings above, because
	// it is a collection of addressable rows rather than a document of known
	// fields; the providers read the very same rows through their ports. May be
	// nil in tests that do not exercise it; the endpoints then report 503.
	AgentCatalog *agentcatalog.Store
	Store        *store.Store
	Heartbeat    time.Duration
	// Updater backs the /api/version and /api/update endpoints. May be nil; they
	// then report that self-update is unavailable.
	Updater *updater.Service
	// Providers backs the provider and workstream-lifecycle endpoints. May be
	// nil when those endpoints are not exercised.
	Providers *provider.Service
	// FileDialog opens the host desktop's own folder chooser for
	// POST /api/browse/native. It may be nil (tests, and any future headless
	// deployment); the endpoint then reports 501 and clients fall back to the
	// in-app picker at GET /api/browse.
	FileDialog filedialog.Chooser
	// Opener hands a URL to the host's default browser for POST /api/open-url,
	// the fallback for clients whose window.open is blocked (the desktop shell's
	// webview, and a browser's popup blocker). May be nil in tests; the endpoint
	// then reports 503.
	Opener opener.Opener
	// Notifications is the hub the SSE stream relays in-app notifications from.
	// Required: GET /api/events subscribes to it.
	Notifications *notify.Hub
	// NotifyTester delivers the settings page's test notification
	// (POST /api/notifications/test) through the same channels a real one uses,
	// so a user can see a style before an agent needs them. May be nil in tests
	// that do not exercise it; the endpoint then reports 503.
	NotifyTester *notify.Tester
	// ListenAddr is the daemon's resolved TCP listen address, known only after
	// net.Listen. Required: an empty value disables the Host/Origin guard, so
	// Handler panics rather than accept it.
	ListenAddr string
	// APIToken is the shared secret every /api request must present, via an
	// "Authorization: Bearer <token>" header or the hs_api_token cookie.
	// Required: an empty value disables authentication, so Handler panics.
	APIToken string
	// PublicBind reports that the daemon is actually listening on a
	// network-reachable address, which relaxes the loopback guard and withholds
	// both token-cookie handouts. serve.go derives it as `allowPublic &&
	// !loopback`; do not wire a raw flag value in here.
	PublicBind bool
	// unsecuredForTest acknowledges that APIToken and/or ListenAddr are empty and
	// so that authentication and/or the loopback guard are off. Handler refuses
	// to build without it. Unexported so only this package's tests can set it.
	unsecuredForTest bool
}

// endpoint is one registered route: an HTTP method+pattern and a builder that
// produces its handler once the daemon's dependencies are known.
type endpoint struct {
	pattern string
	build   func(Deps) http.HandlerFunc
}

var registry []endpoint

// register adds an endpoint to the registry. Endpoint files call it from their
// init(), so adding an endpoint means adding one file.
func register(pattern string, build func(Deps) http.HandlerFunc) {
	registry = append(registry, endpoint{pattern, build})
}

// Handler assembles the daemon's public HTTP surface: the registered /api
// routes on their own mux, so unknown or mis-methoded /api requests get 404/405
// instead of the SPA fallback, and the embedded web UI on everything else.
func Handler(deps Deps) http.Handler {
	// Both security middlewares switch themselves off on a zero value, so a
	// caller that forgets a field would get an unauthenticated /api that starts
	// clean and logs nothing. Refuse to build instead.
	if !deps.unsecuredForTest {
		if deps.APIToken == "" {
			panic("api.Handler: Deps.APIToken is empty, which disables authentication — pass the daemon's token")
		}
		if deps.ListenAddr == "" {
			panic("api.Handler: Deps.ListenAddr is empty, which disables the Host/Origin guard — pass listener.Addr().String()")
		}
	}

	// Register in a stable order regardless of init() ordering across files.
	routes := make([]endpoint, len(registry))
	copy(routes, registry)
	sort.Slice(routes, func(i, j int) bool { return routes[i].pattern < routes[j].pattern })

	api := http.NewServeMux()
	for _, e := range routes {
		api.HandleFunc(e.pattern, e.build(deps))
	}

	mux := http.NewServeMux()
	// Order, outermost first: logRequests so 401/403 rejections are still
	// logged; guardLoopback outside requireToken; the /api routes innermost. The
	// web UI is wrapped so the daemon hands the browser the token cookie on the
	// served document. Both cookie handouts are withheld on a network bind and
	// from a non-loopback peer; see requireToken and issueTokenCookie.
	mux.Handle("/api/", logRequests(guardLoopback(deps.ListenAddr, deps.PublicBind, requireToken(deps.APIToken, !deps.PublicBind, api))))
	mux.Handle("/", issueTokenCookie(deps.APIToken, deps.PublicBind, webui.Handler()))
	return mux
}

// --- shared helpers used by endpoint handlers ---

// ReasonSessionsRunning is the machine-readable `reason` on the 409 a workstream
// removal gets while its sessions are running. Clients branch on this rather than
// on the human message, which is free to change.
const ReasonSessionsRunning = "sessionsRunning"

// jsonError writes a JSON error response with the given message and status.
func jsonError(w http.ResponseWriter, message string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// jsonErrorFields writes a JSON error response with per-field detail, used for
// workstream-config validation failures.
func jsonErrorFields(w http.ResponseWriter, message string, fields map[string]string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(struct {
		Error  string            `json:"error"`
		Fields map[string]string `json:"fields,omitempty"`
	}{Error: message, Fields: fields})
}

// writeJSON encodes v as a JSON response with the given status.
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

// writeProviderError maps a provider.Service error to the right HTTP status.
func writeProviderError(w http.ResponseWriter, err error) {
	if fields := provider.FieldErrors(err); fields != nil {
		code := http.StatusBadRequest
		if errors.Is(err, store.ErrDuplicateName) {
			code = http.StatusConflict
		}
		jsonErrorFields(w, err.Error(), fields, code)
		return
	}
	// A removal refused for running sessions is a 409 a client is expected to
	// *act* on (confirm, then retry with force), so it carries a machine-readable
	// reason and the sessions themselves rather than only a human message.
	if busy := provider.BusySessions(err); busy != nil {
		writeJSON(w, http.StatusConflict, struct {
			Error    string                 `json:"error"`
			Reason   string                 `json:"reason"`
			Sessions []provider.BusySession `json:"sessions"`
		}{Error: err.Error(), Reason: ReasonSessionsRunning, Sessions: busy})
		return
	}
	switch {
	case errors.Is(err, provider.ErrProjectNotFound), errors.Is(err, provider.ErrWorkstreamNotFound), errors.Is(err, provider.ErrSessionNotFound):
		jsonError(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, provider.ErrProviderNotFound), errors.Is(err, provider.ErrCannotCreate),
		errors.Is(err, provider.ErrInvalidKey), errors.Is(err, provider.ErrUnknownOpenMode),
		errors.Is(err, provider.ErrEmptySessionTitle):
		jsonError(w, err.Error(), http.StatusBadRequest)
	// A name already in use is a conflict with the project's current contents,
	// like the concurrency limit below: nothing about the request is malformed,
	// it just cannot be granted while that other workstream holds the name.
	case errors.Is(err, store.ErrDuplicateName), errors.Is(err, store.ErrDuplicateWorkingDir),
		errors.Is(err, store.ErrDuplicateSessionTitle),
		// A placeholder has no session behind it to name yet. Like the duplicates
		// above, the request is well formed and the same call succeeds once the
		// create lands, so it is a conflict rather than a fault.
		errors.Is(err, provider.ErrSessionStarting),
		// A failed create's row: a conflict with what the session is, as above.
		errors.Is(err, provider.ErrSessionNeverStarted),
		errors.Is(err, provider.ErrSessionNotRestartable),
		errors.Is(err, provider.ErrCannotContinue),
		errors.Is(err, provider.ErrOptionNotOffered):
		jsonError(w, err.Error(), http.StatusConflict)
	case errors.Is(err, provider.ErrConcurrencyLimit), errors.Is(err, provider.ErrSessionStopped),
		errors.Is(err, provider.ErrSessionGone),
		errors.Is(err, provider.ErrNoWorkingDir), errors.Is(err, provider.ErrNoLink),
		errors.Is(err, provider.ErrNoShellEnvironment),
		errors.Is(err, provider.ErrOpenWithFailed), errors.Is(err, provider.ErrHandoffFailed),
		// A refusal that names no sessions misses the branch above; it is still a
		// conflict, not a server fault.
		errors.Is(err, provider.ErrWorkstreamBusy):
		jsonError(w, err.Error(), http.StatusConflict)
	case errors.Is(err, provider.ErrCapabilityUnsupported):
		jsonError(w, err.Error(), http.StatusNotImplemented)
	case errors.Is(err, provider.ErrProviderUnavailable):
		jsonError(w, err.Error(), http.StatusServiceUnavailable)
	default:
		jsonError(w, err.Error(), http.StatusInternalServerError)
	}
}

// resolvePath is a thin wrapper around pathutil.Resolve so the API handlers
// can call it without qualifying the package name.
func resolvePath(raw string) (string, error) {
	return pathutil.Resolve(raw)
}

// generateID produces an ID with the given prefix and a random suffix.
func generateID(prefix string) string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return fmt.Sprintf("%s-%x", prefix, b)
}
