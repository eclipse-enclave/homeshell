package api

import (
	"net/http"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
)

func init() {
	register("DELETE /api/projects/{projectId}/workstreams/{workstreamId}/status",
		func(deps Deps) http.HandlerFunc {
			return clearWorkstreamStatusHandler(deps)
		})
}

// clearWorkstreamStatusHandler discards a workstream's transient status message.
//
// A daemon mutation rather than a client-side hide because the message is daemon
// state: hiding it in one browser would leave it in the projection, so it would
// return on reload and stay on everyone else's screen. Clearing it broadcasts.
// The message is never persisted, so this touches memory only — hence a DELETE on
// the subresource rather than a PATCH of the workstream, which is unchanged.
//
// A workstream in error state is refused. There the message is not a note on a
// working workstream but the whole account of a creation that failed, and nothing
// will ever write another (see Service.clearStaleStatus for the other half of this
// rule): dismissing it would leave a permanently red card that cannot say why. The
// way to be rid of a failed creation is to remove it.
func clearWorkstreamStatusHandler(deps Deps) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		projectID := request.PathValue("projectId")
		workstreamID := request.PathValue("workstreamId")
		workstream, err := deps.Store.Workstream(projectID, workstreamID)
		if err != nil {
			jsonError(writer, err.Error(), http.StatusNotFound)
			return
		}
		if workstream.RuntimeState == domain.RuntimeError {
			jsonError(writer,
				"a failed workstream's message cannot be dismissed; remove the workstream instead",
				http.StatusConflict)
			return
		}
		if err := deps.Store.SetStatusMessage(projectID, workstreamID, ""); err != nil {
			jsonError(writer, err.Error(), http.StatusNotFound)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	}
}
