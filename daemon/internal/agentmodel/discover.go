package agentmodel

import (
	"strings"
	"sync"
	"time"
)

// Asking an agent what models it has, and remembering the answer.
//
// The catalog in this package is the fallback for an agent whose CLI cannot be
// asked. Where one can — `opencode models`, `pi --list-models` — the answer is
// better than any list shipped in a release, because what those agents can run
// depends on which providers that particular installation has authenticated. Both
// providers ask (the Local one runs the CLI on the host, the Enclave one runs it
// inside a container it finds already running), so the parsing and the caching
// live here rather than in either of them.

const (
	// TTL is how long an answer is reused. Long enough that opening the create form
	// repeatedly does not re-ask each time, short enough that authenticating a new
	// provider shows up without a daemon restart.
	TTL = time.Minute
	// MaxList caps how many models are taken from one answer, so a misidentified
	// output cannot turn into an unusable selector.
	MaxList = 200
)

// ParseList picks the model IDs out of a CLI's listing. It takes the first field
// of each line and keeps it only if it looks like a fully-qualified model ID —
// `provider/model`, the form both OpenCode and pi document — which is what
// separates a list from prose: an unauthenticated pi prints "No models available.
// Use /login …", and none of those words survive. Trailing `\r` (an answer read
// through a pty, as the Enclave provider must) is trimmed with the rest.
func ParseList(out []byte) []string {
	var models []string
	seen := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		id, _, _ := strings.Cut(strings.TrimSpace(line), " ")
		id = strings.TrimSpace(id)
		if !isModelID(id) || seen[id] {
			continue
		}
		seen[id] = true
		if models = append(models, id); len(models) == MaxList {
			break
		}
	}
	return models
}

// isModelID reports whether a token is a fully-qualified model ID: one slash
// separating two non-empty halves of identifier characters, not starting with "-"
// so it cannot pass as an option where it lands in a tool's argv.
func isModelID(s string) bool {
	provider, model, ok := strings.Cut(s, "/")
	if !ok || provider == "" || model == "" || strings.Contains(model, "/") || strings.HasPrefix(s, "-") {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '/' || r == '-' || r == '_' || r == '.' || r == ':':
		default:
			return false
		}
	}
	return true
}

// Cache remembers each agent's answer for TTL, keyed however the caller
// identifies the thing it asked (a binary on the host, a tool in a container).
// A zero Cache is ready to use, and safe for concurrent use.
type Cache struct {
	mu      sync.Mutex
	entries map[string]cacheEntry
}

type cacheEntry struct {
	at     time.Time
	models []string
}

// Get returns a remembered answer, or false when there is none or it has expired.
// An answer of "none" is remembered too — that is what stops a CLI which cannot
// answer (or is not logged in) from being re-run on every form open.
func (c *Cache) Get(key string, now time.Time) ([]string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || now.Sub(e.at) > TTL {
		return nil, false
	}
	return e.models, true
}

func (c *Cache) Put(key string, now time.Time, models []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]cacheEntry{}
	}
	c.entries[key] = cacheEntry{at: now, models: models}
}
