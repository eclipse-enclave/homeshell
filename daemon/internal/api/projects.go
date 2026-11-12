package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
)

func init() {
	register("GET /api/projects", func(deps Deps) http.HandlerFunc {
		return func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "application/json")
			writer.Write(deps.Store.Snapshot().Data)
		}
	})
	register("POST /api/projects", func(deps Deps) http.HandlerFunc {
		return addProjectHandler(deps)
	})
	register("PATCH /api/projects/{id}", func(deps Deps) http.HandlerFunc {
		return renameProjectHandler(deps)
	})
	register("DELETE /api/projects/{id}", func(deps Deps) http.HandlerFunc {
		return removeProjectHandler(deps)
	})
}

func addProjectHandler(deps Deps) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		var req struct {
			Path string `json:"path"`
			Name string `json:"name"`
		}
		if err := json.NewDecoder(request.Body).Decode(&req); err != nil {
			jsonError(writer, "invalid JSON", http.StatusBadRequest)
			return
		}
		req.Path = strings.TrimSpace(req.Path)
		if req.Path == "" {
			jsonError(writer, "path is required", http.StatusBadRequest)
			return
		}
		resolved, err := resolvePath(req.Path)
		if err != nil {
			jsonError(writer, err.Error(), http.StatusBadRequest)
			return
		}
		info, err := os.Stat(resolved)
		if err != nil {
			jsonError(writer, "path does not exist", http.StatusBadRequest)
			return
		}
		if !info.IsDir() {
			jsonError(writer, "path is not a directory", http.StatusBadRequest)
			return
		}
		name := strings.TrimSpace(req.Name)
		if name == "" {
			name = filepath.Base(resolved)
		}

		project := domain.Project{
			ID:          generateID("proj"),
			Name:        name,
			Path:        resolved,
			State:       domain.SessionIdle,
			Workstreams: []domain.Workstream{},
		}
		// The provider service, when there is one, gives the project its default
		// workstream.
		if deps.Providers != nil {
			err = deps.Providers.AddProject(request.Context(), &project)
		} else {
			err = deps.Store.AddProject(&project)
		}
		if err != nil {
			jsonError(writer, err.Error(), http.StatusConflict)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusCreated)
		json.NewEncoder(writer).Encode(project)
	}
}

func renameProjectHandler(deps Deps) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		id := request.PathValue("id")
		var req struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(request.Body).Decode(&req); err != nil {
			jsonError(writer, "invalid JSON", http.StatusBadRequest)
			return
		}
		name := strings.TrimSpace(req.Name)
		if name == "" {
			jsonError(writer, "name must not be empty", http.StatusBadRequest)
			return
		}
		if err := deps.Store.RenameProject(id, name); err != nil {
			jsonError(writer, err.Error(), http.StatusNotFound)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	}
}

func removeProjectHandler(deps Deps) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if deps.Providers == nil {
			jsonError(writer, "no providers are available", http.StatusServiceUnavailable)
			return
		}
		if err := deps.Providers.RemoveProject(request.Context(), request.PathValue("id")); err != nil {
			writeProviderError(writer, err)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	}
}
