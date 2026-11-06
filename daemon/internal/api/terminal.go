package api

import (
	"net/http"

	"github.com/eclipsesource/homeshell/daemon/internal/terminal"
)

func init() {
	register("GET /api/terminal-emulators", func(_ Deps) http.HandlerFunc {
		return listTerminalEmulatorsHandler
	})
}

// listTerminalEmulatorsHandler serves the terminal emulators this host's
// platform supports, each marked installed or not, and what Auto resolves to,
// for the terminal setting to offer. Detection runs per request, so an emulator
// installed since shows up.
func listTerminalEmulatorsHandler(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, terminal.Emulators())
}
