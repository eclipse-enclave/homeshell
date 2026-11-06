package api

import "net/http"

func init() {
	register("POST /api/projects/{id}/refresh", func(deps Deps) http.HandlerFunc {
		return refreshProjectHandler(deps)
	})
}

// refreshProjectHandler re-detects a project's git-backing (issue #5). It
// exists so a project added before `git init` was run in its directory can be
// brought up to date without recreating it; the store broadcasts the refreshed
// state to all clients.
func refreshProjectHandler(deps Deps) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		id := request.PathValue("id")
		if err := deps.Store.RefreshGitBacked(id); err != nil {
			jsonError(writer, err.Error(), http.StatusNotFound)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	}
}
