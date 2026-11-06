package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/updater"
)

func init() {
	register("POST /api/update", func(d Deps) http.HandlerFunc {
		return updateHandler(d.Updater)
	})
}

// updateHandler triggers a self-update. On success it flushes a JSON response
// confirming the target version *before* the process restarts, so the client
// sees the outcome just before the SSE stream drops and EventSource reconnects
// to the new daemon (docs/ARCHITECTURE.md, "Self-update").
func updateHandler(upd *updater.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if upd == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, `{"error":"self-update is not available"}`)
			return
		}

		// Not bound to r.Context(): the download must not be cancelled if the
		// client disconnects mid-update.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		flush := func() {
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}

		newVersion, updated, err := upd.Apply(ctx, func(v string) {
			// The binary is swapped and durable; report success before the
			// restart tears down this connection.
			fmt.Fprintf(w, `{"updated":true,"version":%q}`, v)
			flush()
		})
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, `{"error":%q}`, "update failed")
			slog.Error("self-update failed", "error", err)
			return
		}
		if !updated {
			fmt.Fprintf(w, `{"updated":false,"version":%q}`, newVersion)
		}
	}
}
