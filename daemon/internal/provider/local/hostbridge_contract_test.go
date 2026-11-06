package local

import "github.com/eclipsesource/homeshell/daemon/internal/hostbridge"

// LocationRecorder is optional for test runtimes, but mandatory in production:
// losing it silently regresses relocated-socket recovery.
var (
	_ Runtime          = hostbridge.Runtime{}
	_ LocationRecorder = hostbridge.Runtime{}
)
