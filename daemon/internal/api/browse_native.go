package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"

	"github.com/eclipsesource/homeshell/daemon/internal/filedialog"
)

func init() {
	register("POST /api/browse/native", func(deps Deps) http.HandlerFunc {
		return browseNativeHandler(deps)
	})
}

// browseNativeRequest optionally names where the dialog should open. It is a
// hint: a host whose chooser cannot honor it opens wherever it likes, and an
// unusable value is dropped rather than refused, because the point of the
// endpoint is to get a dialog in front of the user.
type browseNativeRequest struct {
	Path string `json:"path"`
}

// browseNativeResponse carries the chosen directory, or reports that the user
// dismissed the dialog. Cancelling is a normal outcome, so it is a 200 with
// `cancelled` set rather than an error status the client would have to
// distinguish from a real failure.
type browseNativeResponse struct {
	Path      string `json:"path,omitempty"`
	Cancelled bool   `json:"cancelled,omitempty"`
}

// browseNativeHandler opens the host desktop's own folder chooser and returns
// what the user picked (issue: native file explorer for project Browse).
//
// It is a POST because it is an action with a side effect the user sees — a
// window opens on their screen — not a lookup, and because it must never be
// retried by a cache or a link prefetch.
//
// The request stays open for as long as the dialog is up. That is deliberate
// and safe here: the daemon's HTTP server bounds header reads only, precisely
// so long-lived responses work (docs/API.md, "Host helpers"), and the request context
// kills the dialog if the client goes away.
func browseNativeHandler(deps Deps) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if deps.FileDialog == nil {
			jsonError(writer, "native folder chooser is unavailable", http.StatusNotImplemented)
			return
		}
		// The chooser opens on the *daemon's* screen. On a loopback bind that is
		// the same screen as the UI, but a public bind means the person clicking
		// Browse may be on another machine entirely — they would wait on a dialog
		// nobody can see. Refuse instead, and let the client fall back to the
		// in-app picker, which works over the network because it is just JSON.
		if deps.PublicBind {
			jsonError(writer, "the native folder chooser is only available to a client on the daemon's own host", http.StatusConflict)
			return
		}

		var body browseNativeRequest
		if request.Body != nil {
			// A missing or malformed body only costs the start-directory hint.
			_ = json.NewDecoder(request.Body).Decode(&body)
		}

		path, err := deps.FileDialog.ChooseDirectory(request.Context(), startDirectory(body.Path))
		switch {
		case errors.Is(err, filedialog.ErrCancelled):
			writeJSON(writer, http.StatusOK, browseNativeResponse{Cancelled: true})
		case errors.Is(err, filedialog.ErrUnavailable):
			jsonError(writer, err.Error(), http.StatusNotImplemented)
		case err != nil:
			jsonError(writer, err.Error(), http.StatusInternalServerError)
		default:
			writeJSON(writer, http.StatusOK, browseNativeResponse{Path: path})
		}
	}
}

// startDirectory turns a requested start path into one the dialog can open at,
// or "" to let the platform choose. Anything that is not an existing directory
// — a typo half-typed into the path box, a file, a path that has been deleted —
// is dropped rather than passed on, since a chooser handed a bad location can
// refuse to open at all.
func startDirectory(raw string) string {
	if raw == "" {
		return ""
	}
	resolved, err := resolvePath(raw)
	if err != nil {
		return ""
	}
	if info, err := os.Stat(resolved); err != nil || !info.IsDir() {
		return ""
	}
	return resolved
}
