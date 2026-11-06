package api

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/eclipsesource/homeshell/daemon/internal/logging"
	"github.com/eclipsesource/homeshell/daemon/internal/settings"
)

func init() {
	register("GET /api/config", func(d Deps) http.HandlerFunc {
		return getConfigHandler(d.LogLevel)
	})
	register("PATCH /api/config", func(d Deps) http.HandlerFunc {
		return patchConfigHandler(d.LogLevel, d.Settings)
	})
}

// /api/config is the daemon's **live runtime configuration** — the effective
// value of a runtime knob (today just the log level), as opposed to the durable
// preference document served by /api/settings. The distinction is deliberate:
// clients always read the live value here (it can never drift from what the
// daemon is actually doing), while settings.json stores only the startup
// default. See docs/API.md, "Daemon".

// getConfigHandler reports the daemon's effective runtime configuration that
// clients render and can change. Today that is the live log level; more
// daemon-owned config joins this payload as it lands.
func getConfigHandler(level *logging.Level) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeConfig(w, level)
	}
}

// patchConfigHandler changes the daemon's log level at runtime. It takes effect
// immediately for all clients (the level is process-global daemon state) and is
// persisted to settings.json as the startup default so the change survives a
// restart. Persistence is best-effort: the live change is the primary effect,
// so a failed write is logged but not surfaced as an error.
func patchConfigHandler(level *logging.Level, store *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if level == nil {
			jsonError(w, "log level is not configurable", http.StatusServiceUnavailable)
			return
		}
		var body struct {
			LogLevel string `json:"logLevel"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}
		parsed, err := logging.ParseLevel(body.LogLevel)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		level.Set(parsed)
		name := logging.LevelName(parsed)
		if store != nil {
			if err := store.SetLogLevel(name); err != nil {
				slog.Warn("log level applied but not persisted", "error", err)
			}
		}
		slog.Info("log level changed", "level", name)
		writeConfig(w, level)
	}
}

func writeConfig(w http.ResponseWriter, level *logging.Level) {
	name := ""
	if level != nil {
		name = logging.LevelName(level.Level())
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"logLevel": name})
}
