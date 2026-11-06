package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/eclipsesource/homeshell/daemon/internal/settings"
)

func init() {
	register("POST /api/notifications/test", func(deps Deps) http.HandlerFunc {
		return notificationTestHandler(deps)
	})
}

type notificationTestRequest struct {
	// Style is the delivery style to demonstrate. Empty means "the configured
	// one", so a client can ask for a test of the current setting without
	// reading it first.
	Style string `json:"style"`
}

// notificationTestHandler delivers a sample notification so a user can see what
// a delivery style looks like before an agent ever needs them.
//
// It has to be the daemon's job rather than the client's: the OS-native channel
// is a platform command the daemon shells out to (the browser never sees it),
// and the in-app channel is an SSE push the daemon owns. A client that faked
// either one would be demonstrating itself rather than the thing being
// configured — the point of the test is that it travels the same path a real
// notification will.
func notificationTestHandler(deps Deps) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if deps.NotifyTester == nil {
			jsonError(writer, "notifications are unavailable", http.StatusServiceUnavailable)
			return
		}

		var body notificationTestRequest
		// An absent body is not an error: it means "test the configured style".
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
			jsonError(writer, "invalid JSON body", http.StatusBadRequest)
			return
		}
		if body.Style != "" && !settings.ValidNotifyStyle(body.Style) {
			jsonError(writer, `style must be "none", "in-app", "native", or "both"`, http.StatusBadRequest)
			return
		}

		// WithoutCancel, not the request context: the native channel spawns
		// `notify-send`/`osascript` in the background, and the request
		// context is cancelled the moment this handler returns — which would kill
		// the command before the desktop had shown anything. The values (and any
		// future tracing) still ride along.
		writeJSON(writer, http.StatusOK, deps.NotifyTester.Send(context.WithoutCancel(request.Context()), body.Style))
	}
}
