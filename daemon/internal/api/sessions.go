package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/eclipsesource/homeshell/daemon/internal/provider"
)

func init() {
	register("GET /api/projects/{projectId}/workstreams/{workstreamId}/session-schemas", func(d Deps) http.HandlerFunc {
		return sessionSchemasHandler(d)
	})
	register("POST /api/projects/{projectId}/workstreams/{workstreamId}/sessions", func(d Deps) http.HandlerFunc {
		return createSessionHandler(d)
	})
	register("POST /api/projects/{projectId}/workstreams/{workstreamId}/sessions/{sessionId}/stop", func(d Deps) http.HandlerFunc {
		return sessionActionHandler(d, func(s *provider.Service, r *http.Request) error {
			return s.StopSession(r.Context(), sessionRef(r))
		})
	})
	register("POST /api/projects/{projectId}/workstreams/{workstreamId}/sessions/{sessionId}/start", func(d Deps) http.HandlerFunc {
		return sessionActionHandler(d, func(s *provider.Service, r *http.Request) error {
			return s.StartSession(r.Context(), sessionRef(r))
		})
	})
	register("POST /api/projects/{projectId}/workstreams/{workstreamId}/sessions/{sessionId}/restart", func(d Deps) http.HandlerFunc {
		return restartSessionHandler(d)
	})
	register("GET /api/projects/{projectId}/workstreams/{workstreamId}/sessions/{sessionId}/launch-config", func(d Deps) http.HandlerFunc {
		return launchConfigHandler(d)
	})
	register("POST /api/projects/{projectId}/workstreams/{workstreamId}/sessions/{sessionId}/prompt", func(d Deps) http.HandlerFunc {
		return promptHandler(d)
	})
	register("POST /api/projects/{projectId}/workstreams/{workstreamId}/sessions/{sessionId}/key", func(d Deps) http.HandlerFunc {
		return keyHandler(d)
	})
	register("PATCH /api/projects/{projectId}/workstreams/{workstreamId}/sessions/{sessionId}", func(d Deps) http.HandlerFunc {
		return renameSessionHandler(d)
	})
	register("DELETE /api/projects/{projectId}/workstreams/{workstreamId}/sessions/{sessionId}/title", func(d Deps) http.HandlerFunc {
		return sessionActionHandler(d, func(s *provider.Service, r *http.Request) error {
			return s.ResetSessionTitle(r.Context(), sessionRef(r))
		})
	})
	register("DELETE /api/projects/{projectId}/workstreams/{workstreamId}/sessions/{sessionId}", func(d Deps) http.HandlerFunc {
		return sessionActionHandler(d, func(s *provider.Service, r *http.Request) error {
			return s.RemoveSession(r.Context(), sessionRef(r))
		})
	})
}

func workstreamRef(r *http.Request) provider.WorkstreamRef {
	return provider.WorkstreamRef{
		ProjectID:    r.PathValue("projectId"),
		WorkstreamID: r.PathValue("workstreamId"),
	}
}

func sessionRef(r *http.Request) provider.SessionRef {
	return provider.SessionRef{
		ProjectID:    r.PathValue("projectId"),
		WorkstreamID: r.PathValue("workstreamId"),
		SessionID:    r.PathValue("sessionId"),
	}
}

// sessionSchemasHandler returns the session-creation options for a workstream:
// the ordered list of session types its provider can create, each with a label
// and a JSON Schema the UI renders as a form.
func sessionSchemasHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.Providers == nil {
			writeJSON(w, http.StatusOK, []provider.SessionProviderSchemas{})
			return
		}
		schemas, err := d.Providers.SessionSchemasFor(r.Context(), workstreamRef(r))
		if err != nil {
			writeProviderError(w, err)
			return
		}
		if schemas == nil {
			schemas = []provider.SessionProviderSchemas{}
		}
		writeJSON(w, http.StatusOK, schemas)
	}
}

// createSessionHandler creates a session in a workstream from a chosen schema
// option and user-supplied config.
func createSessionHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.Providers == nil {
			jsonError(w, "no providers are available", http.StatusServiceUnavailable)
			return
		}
		var req struct {
			ProviderID string          `json:"providerId"`
			OptionID   string          `json:"optionId"`
			Config     json.RawMessage `json:"config"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		ref := workstreamRef(r)
		placeholder, err := d.Providers.CreateSession(r.Context(), provider.CreateSessionInput{
			ProjectID:    ref.ProjectID,
			WorkstreamID: ref.WorkstreamID,
			ProviderID:   req.ProviderID,
			OptionID:     req.OptionID,
			Config:       req.Config,
		})
		if err != nil {
			writeProviderError(w, err)
			return
		}
		// Session creation is async: this is the starting placeholder, and the
		// real session replaces it via SSE.
		writeJSON(w, http.StatusAccepted, placeholder)
	}
}

// restartSessionHandler starts a session again with new settings: the same config
// a create takes, plus whether to start a fresh conversation instead of continuing
// the last one. Async like a create — the session goes starting and comes back
// up via SSE — so the answer is 202 with the session as it stands.
func restartSessionHandler(d Deps) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if d.Providers == nil {
			jsonError(writer, "no providers are available", http.StatusServiceUnavailable)
			return
		}
		var req struct {
			Config json.RawMessage `json:"config"`
			Fresh  bool            `json:"fresh"`
		}
		// An absent body is not an error: it restarts the session with what it was
		// last started with.
		if err := json.NewDecoder(request.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			jsonError(writer, "invalid JSON", http.StatusBadRequest)
			return
		}
		sess, err := d.Providers.RestartSession(request.Context(), provider.RestartSessionInput{
			Session: sessionRef(request),
			Config:  req.Config,
			Fresh:   req.Fresh,
		})
		if err != nil {
			writeProviderError(writer, err)
			return
		}
		writeJSON(writer, http.StatusAccepted, sess)
	}
}

// launchConfigHandler reports what a session was last started with — its
// provider, option and config — and that option's schema, so a restart form can
// open pre-filled, and why a restart cannot continue its conversation, when it
// cannot. A session HomeShell did not start reports the option it would restart
// as, and no config; one that cannot be restarted is refused.
func launchConfigHandler(d Deps) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if d.Providers == nil {
			jsonError(writer, "no providers are available", http.StatusServiceUnavailable)
			return
		}
		launch, err := d.Providers.SessionLaunchConfig(request.Context(), sessionRef(request))
		if err != nil {
			writeProviderError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, launch)
	}
}

// maxTitleBytes bounds the rename body. A display name is a handful of words, and
// the title rides in every full-state broadcast to every connected client, so an
// oversized one is a refusal rather than something to store.
const maxTitleBytes = 4 * 1024

// renameSessionHandler gives a session the display title the user chose,
// overriding the one its provider reports (the daemon re-applies it over every
// projection, so it is not undone by the next poll).
//
// A `PATCH` of the session rather than a subresource, because the session's own
// display name is what changes. Clearing it is the *other* operation —
// `DELETE …/title` restores the provider's title — so an empty title here is a
// 400 rather than a reset: a client that meant to reset should say so, and one
// that submitted an empty form should hear about it.
func renameSessionHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.Providers == nil {
			jsonError(w, "no providers are available", http.StatusServiceUnavailable)
			return
		}
		// One byte past the limit, like promptHandler below: reading exactly the
		// limit truncates an oversized body mid-string, and the client hears
		// "invalid JSON" about a title that was merely too long.
		body, err := io.ReadAll(io.LimitReader(r.Body, maxTitleBytes+1))
		if err != nil {
			jsonError(w, "could not read request body", http.StatusBadRequest)
			return
		}
		if len(body) > maxTitleBytes {
			jsonError(w, "title is too large", http.StatusRequestEntityTooLarge)
			return
		}
		var req struct {
			Title string `json:"title"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			jsonError(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if err := d.Providers.RenameSession(r.Context(), sessionRef(r), req.Title); err != nil {
			writeProviderError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// maxPromptBytes bounds a one-off prompt so a client cannot post an unbounded
// body to a session.
const maxPromptBytes = 64 * 1024

// promptHandler delivers a one-off line of text to a session via its provider.
func promptHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.Providers == nil {
			jsonError(w, "no providers are available", http.StatusServiceUnavailable)
			return
		}
		// Read at most one byte past the limit so an oversized body is
		// distinguishable from a body that exactly fills it, and report it as
		// such instead of letting the truncated JSON fail as "invalid JSON".
		body, err := io.ReadAll(io.LimitReader(r.Body, maxPromptBytes+1))
		if err != nil {
			jsonError(w, "could not read request body", http.StatusBadRequest)
			return
		}
		if len(body) > maxPromptBytes {
			jsonError(w, "text is too large", http.StatusRequestEntityTooLarge)
			return
		}
		var req struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			jsonError(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(req.Text) == "" {
			jsonError(w, "text must not be empty", http.StatusBadRequest)
			return
		}
		if err := d.Providers.Prompt(r.Context(), sessionRef(r), req.Text); err != nil {
			writeProviderError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// maxKeyBytes bounds the tiny key-request body; a normalized key name is only a
// few characters, so anything larger is malformed.
const maxKeyBytes = 256

// keyHandler delivers a single named key event to a session via its provider,
// for navigating an agent's interactive menu. The key is validated by the
// service (unknown key → 400).
func keyHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.Providers == nil {
			jsonError(w, "no providers are available", http.StatusServiceUnavailable)
			return
		}
		var req struct {
			Key string `json:"key"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, maxKeyBytes)).Decode(&req); err != nil {
			jsonError(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if err := d.Providers.SendKey(r.Context(), sessionRef(r), req.Key); err != nil {
			writeProviderError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// sessionActionHandler adapts a session lifecycle action (stop/start/remove)
// that returns only an error into a 204-on-success handler.
func sessionActionHandler(d Deps, action func(*provider.Service, *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.Providers == nil {
			jsonError(w, "no providers are available", http.StatusServiceUnavailable)
			return
		}
		if err := action(d.Providers, r); err != nil {
			writeProviderError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
