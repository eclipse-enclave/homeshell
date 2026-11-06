package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/updater"
)

func init() {
	register("GET /api/version", func(d Deps) http.HandlerFunc {
		return versionHandler(d.Version, d.Updater)
	})
}

// versionHandler reports the running version and, when self-update is
// configured, whether a newer release is available. It never fails the request
// on a release-check error: the daemon is still healthy, so it returns the
// current version with updateAvailable=false and surfaces the reason in a
// non-fatal field.
func versionHandler(version string, upd *updater.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		resp := struct {
			updater.Status
			Error string `json:"error,omitempty"`
		}{Status: updater.Status{Current: version, Latest: version}}

		if upd != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
			defer cancel()
			if status, err := upd.Check(ctx); err != nil {
				resp.Error = "release check failed"
				slog.Warn("release check failed", "error", err)
			} else {
				resp.Status = status
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}
