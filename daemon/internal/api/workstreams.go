package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/eclipsesource/homeshell/daemon/internal/provider"
)

func init() {
	register("POST /api/projects/{projectId}/workstreams", func(d Deps) http.HandlerFunc {
		return createWorkstreamHandler(d)
	})
	register("POST /api/projects/{projectId}/default-workstream", func(deps Deps) http.HandlerFunc {
		return addDefaultWorkstreamHandler(deps)
	})
	register("POST /api/projects/{projectId}/workstream-draft", func(deps Deps) http.HandlerFunc {
		return workstreamDraftHandler(deps)
	})
	register("PATCH /api/projects/{projectId}/workstreams/{workstreamId}", func(d Deps) http.HandlerFunc {
		return updateWorkstreamHandler(d)
	})
	register("DELETE /api/projects/{projectId}/workstreams/{workstreamId}", func(d Deps) http.HandlerFunc {
		return removeWorkstreamHandler(d)
	})
	register("POST /api/projects/{projectId}/workstreams/{workstreamId}/open", func(d Deps) http.HandlerFunc {
		return openWorkstreamHandler(d)
	})
	register("POST /api/projects/{projectId}/workstreams/{workstreamId}/open-with", func(d Deps) http.HandlerFunc {
		return openWorkstreamWithHandler(d)
	})
	register("POST /api/projects/{projectId}/workstreams/{workstreamId}/note", func(d Deps) http.HandlerFunc {
		return workstreamNoteHandler(d)
	})
	register("POST /api/projects/{projectId}/workstreams/{workstreamId}/sessions/{sessionId}/open", func(d Deps) http.HandlerFunc {
		return openSessionHandler(d)
	})
}

// createWorkstreamHandler instantiates a workstream on a project via a chosen
// provider and workstream type, with user-supplied config.
func createWorkstreamHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.Providers == nil {
			jsonError(w, "no providers are available", http.StatusServiceUnavailable)
			return
		}
		input, ok := decodeCreateInput(w, r)
		if !ok {
			return
		}
		created, err := d.Providers.Create(r.Context(), input)
		if err != nil {
			writeProviderError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, created)
	}
}

// addDefaultWorkstreamHandler gives a project the workstream a new project comes
// with, in place in the project folder, and answers with it.
func addDefaultWorkstreamHandler(deps Deps) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if deps.Providers == nil {
			jsonError(writer, "no providers are available", http.StatusServiceUnavailable)
			return
		}
		created, err := deps.Providers.AddDefaultWorkstream(request.Context(), request.PathValue("projectId"))
		if err != nil {
			writeProviderError(writer, err)
			return
		}
		writeJSON(writer, http.StatusCreated, created)
	}
}

// workstreamDraftHandler answers a create's body with what the create would
// derive for the values it leaves blank (name, branch, path), creating nothing.
func workstreamDraftHandler(deps Deps) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if deps.Providers == nil {
			jsonError(writer, "no providers are available", http.StatusServiceUnavailable)
			return
		}
		input, ok := decodeCreateInput(writer, request)
		if !ok {
			return
		}
		draft, err := deps.Providers.ResolveDraft(request.Context(), input)
		if err != nil {
			writeProviderError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, draft)
	}
}

// decodeCreateInput reads a create's body, answering a malformed one itself.
func decodeCreateInput(writer http.ResponseWriter, request *http.Request) (provider.CreateInput, bool) {
	var body struct {
		ProviderID string          `json:"providerId"`
		Name       string          `json:"name"`
		Config     json.RawMessage `json:"config"`
	}
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		jsonError(writer, "invalid JSON", http.StatusBadRequest)
		return provider.CreateInput{}, false
	}
	return provider.CreateInput{
		ProjectID:  request.PathValue("projectId"),
		ProviderID: body.ProviderID,
		Name:       strings.TrimSpace(body.Name),
		Config:     body.Config,
	}, true
}

// updateWorkstreamHandler renames a workstream.
func updateWorkstreamHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.Providers == nil {
			jsonError(w, "no providers are available", http.StatusServiceUnavailable)
			return
		}
		var req struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		name := strings.TrimSpace(req.Name)
		if name == "" {
			jsonError(w, "name must not be empty", http.StatusBadRequest)
			return
		}
		ref := provider.WorkstreamRef{ProjectID: r.PathValue("projectId"), WorkstreamID: r.PathValue("workstreamId")}
		if err := d.Providers.Rename(r.Context(), ref, name); err != nil {
			writeProviderError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// openSessionHandler opens a session via its owning provider (e.g. the local
// provider launches a terminal attached to it). The request body is optional;
// "mode" defaults to "terminal".
func openSessionHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.Providers == nil {
			jsonError(w, "no providers are available", http.StatusServiceUnavailable)
			return
		}
		var req struct {
			Mode string `json:"mode"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req) // body optional; mode defaults below
		mode := strings.TrimSpace(req.Mode)
		if mode == "" {
			mode = "terminal"
		}
		ref := provider.SessionRef{
			ProjectID:    r.PathValue("projectId"),
			WorkstreamID: r.PathValue("workstreamId"),
			SessionID:    r.PathValue("sessionId"),
		}
		res, err := d.Providers.Open(r.Context(), ref, mode)
		if err != nil {
			writeProviderError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, res)
	}
}

// openWorkstreamHandler is the default open, which runs the user's default-open
// row. It takes no body; choosing a specific hand-off is the open-with
// endpoint's job.
func openWorkstreamHandler(d Deps) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if d.Providers == nil {
			jsonError(writer, "no providers are available", http.StatusServiceUnavailable)
			return
		}
		ref := provider.WorkstreamRef{ProjectID: request.PathValue("projectId"), WorkstreamID: request.PathValue("workstreamId")}
		res, err := d.Providers.OpenWorkstream(request.Context(), ref, "")
		if err != nil {
			writeProviderError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, res)
	}
}

// openWorkstreamWithHandler hands a workstream to the user in a chosen mode,
// one of the IDs advertised on the workstream (`openModes`). The mode is
// required; the modeless default lives on the open endpoint. A link answers
// with a URL for the client to open.
func openWorkstreamWithHandler(d Deps) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if d.Providers == nil {
			jsonError(writer, "no providers are available", http.StatusServiceUnavailable)
			return
		}
		var req struct {
			Mode string `json:"mode"`
		}
		if err := json.NewDecoder(request.Body).Decode(&req); err != nil {
			jsonError(writer, "invalid JSON", http.StatusBadRequest)
			return
		}
		mode := strings.TrimSpace(req.Mode)
		if mode == "" {
			jsonError(writer, "mode must not be empty", http.StatusBadRequest)
			return
		}
		ref := provider.WorkstreamRef{ProjectID: request.PathValue("projectId"), WorkstreamID: request.PathValue("workstreamId")}
		res, err := d.Providers.OpenWorkstream(request.Context(), ref, mode)
		if err != nil {
			writeProviderError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, res)
	}
}

// workstreamNoteHandler opens the workstream's note (NOTES.md in its working
// directory) in the desktop's default application, creating the file on the
// first call. It takes no body: there is one note per workstream, and its
// location is the daemon's to decide.
func workstreamNoteHandler(d Deps) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if d.Providers == nil {
			jsonError(writer, "no providers are available", http.StatusServiceUnavailable)
			return
		}
		ref := provider.WorkstreamRef{ProjectID: request.PathValue("projectId"), WorkstreamID: request.PathValue("workstreamId")}
		res, err := d.Providers.OpenNote(request.Context(), ref)
		if err != nil {
			writeProviderError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, res)
	}
}

// removeWorkstreamHandler tears down a workstream via its owning provider. An
// optional JSON body ({"keepFiles": true, "force": true}) controls cleanup;
// keepFiles defaults to false. Sessions are always stopped.
//
// force overrides the daemon's running-session guard: without it, removing a
// workstream whose sessions are running (or waiting for input) is refused with
// 409 and a `reason` of "sessionsRunning" plus the offending sessions, so a
// client can confirm and retry. It is accepted as `?force=true` as well as in
// the body — either turns it on — so a plain `curl -X DELETE …?force=true`
// needs no body at all.
func removeWorkstreamHandler(d Deps) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if d.Providers == nil {
			jsonError(writer, "no providers are available", http.StatusServiceUnavailable)
			return
		}
		var req struct {
			KeepFiles bool `json:"keepFiles"`
			Force     bool `json:"force"`
		}
		// Body is optional — ignore decode errors (empty body, EOF, etc.).
		_ = json.NewDecoder(request.Body).Decode(&req)

		ref := provider.WorkstreamRef{ProjectID: request.PathValue("projectId"), WorkstreamID: request.PathValue("workstreamId")}
		opts := provider.RemoveOptions{
			KeepFiles: req.KeepFiles,
			Force:     req.Force || request.URL.Query().Get("force") == "true",
		}
		if err := d.Providers.Remove(request.Context(), ref, opts); err != nil {
			writeProviderError(writer, err)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	}
}
