// Package apiclient is a thin client for the daemon's public HTTP API. It wraps
// that API once so every client verb (and later the MCP surface) reaches the
// daemon the same way: over the public contract only.
package apiclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/daemonstate"
)

// Client talks to one daemon at a fixed base URL.
type Client struct {
	BaseURL string
	// token is the API secret presented as "Authorization: Bearer <token>" on
	// every request when non-empty. It is a constructor parameter rather than a
	// settable field so a call site cannot build a client and forget it — every
	// /api request needs it, and forgetting produces a 401 that looks like a
	// daemon problem.
	token string
	hc    *http.Client
	// untimed serves Do, which must not impose a timeout of its own (the caller's ctx
	// deadline bounds the request). It shares this Client's own *http.Transport with
	// hc, so a Client keeps its connections to itself rather than borrowing
	// http.DefaultTransport's process-wide pool — note that a bare &http.Client{}
	// would NOT achieve that: a nil Transport falls back to http.DefaultTransport.
	untimed *http.Client
}

// New returns a client aimed at the given listen address (host:port), presenting
// the given API token. The token comes from the daemon's state file (see Discover
// and daemonstate); pass "" only for a daemon known to run without auth.
func New(addr, token string) *Client {
	// Clone rather than &http.Transport{}: a zero-value transport keeps none of
	// DefaultTransport's settings, so it would dial with no timeout and no keep-alive
	// and never reap an idle connection — and Do is the long-running path.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	return &Client{
		BaseURL: "http://" + addr,
		token:   token,
		hc:      &http.Client{Timeout: 2 * time.Second, Transport: transport},
		untimed: &http.Client{Transport: transport},
	}
}

// Discover reads the discovery state file and returns a client aimed at the
// recorded address and carrying its token, along with the state itself. When no
// daemon has published a state file the error wraps fs.ErrNotExist.
func Discover() (*Client, daemonstate.State, error) {
	st, err := daemonstate.Read()
	if err != nil {
		return nil, daemonstate.State{}, err
	}
	return New(st.Address, st.Token), st, nil
}

// newRequest builds a request against the client's base URL, attaching the
// bearer token when the client carries one, so token handling lives in one place.
func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	return req, nil
}

// Do issues an authenticated request against the given path, centralizing bearer
// token attachment for callers beyond the typed helpers. Unlike those helpers it
// applies no client-side timeout: the caller's ctx deadline bounds the request,
// which is what a long-running verb such as POST /api/update (download, swap,
// restart — minutes, not seconds) needs.
func (c *Client) Do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	req, err := c.newRequest(ctx, method, path, body)
	if err != nil {
		return nil, err
	}
	return c.untimedClient().Do(req)
}

// untimedClient returns the client Do uses. A Client built as a struct literal rather
// than through New — which the package's own tests do, to inject httptest's client —
// has no untimed field; deriving one from hc's transport keeps that idiom working
// (including its TLS config) instead of panicking.
//
// It mirrors hc rather than improving on it: a struct-literal Client that leaves
// Transport nil gets a nil Transport here too, and net/http then falls back to
// http.DefaultTransport for both. The connection isolation the untimed field documents
// is a property of New, which supplies a real transport, not of this fallback.
func (c *Client) untimedClient() *http.Client {
	if c.untimed != nil {
		return c.untimed
	}
	return &http.Client{Transport: c.hc.Transport}
}

// Hello is the parsed payload of GET /api/hello.
type Hello struct {
	Message string `json:"message"`
	Version string `json:"version"`
}

// Hello probes GET /api/hello; a nil error means the daemon is reachable and
// answering. Client verbs use it to tell a live daemon from a stale state file.
func (c *Client) Hello(ctx context.Context) (Hello, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/api/hello", nil)
	if err != nil {
		return Hello{}, err
	}
	res, err := c.hc.Do(req)
	if err != nil {
		return Hello{}, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return Hello{}, fmt.Errorf("GET /api/hello: status %d", res.StatusCode)
	}
	var h Hello
	if err := json.NewDecoder(res.Body).Decode(&h); err != nil {
		return Hello{}, fmt.Errorf("decoding /api/hello: %w", err)
	}
	return h, nil
}
