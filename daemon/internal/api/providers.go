package api

import (
	"net/http"

	"github.com/eclipsesource/homeshell/daemon/internal/provider"
)

func init() {
	register("GET /api/providers", func(d Deps) http.HandlerFunc {
		return listProvidersHandler(d)
	})
	register("GET /api/projects/{projectId}/workstream-providers", func(d Deps) http.HandlerFunc {
		return workstreamProvidersHandler(d)
	})
}

// listProvidersHandler serves the registered providers with their availability
// and declared capabilities.
func listProvidersHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		descriptors := []provider.ProviderDescriptor{}
		if d.Providers != nil {
			descriptors = d.Providers.Describe(r.Context())
		}
		writeJSON(w, http.StatusOK, descriptors)
	}
}

// workstreamProvidersHandler serves the create pick-list for a project: the
// available providers that can create a workstream there, each with its schema.
func workstreamProvidersHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.Providers == nil {
			writeJSON(w, http.StatusOK, []provider.ProviderDescriptor{})
			return
		}
		providers, err := d.Providers.WorkstreamProvidersFor(r.Context(), r.PathValue("projectId"))
		if err != nil {
			writeProviderError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, providers)
	}
}
