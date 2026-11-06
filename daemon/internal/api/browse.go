package api

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func init() {
	register("GET /api/browse", func(_ Deps) http.HandlerFunc {
		return browseHandler()
	})
}

type browseEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"isDir"`
}

type browseResponse struct {
	Path    string        `json:"path"`
	Parent  string        `json:"parent"`
	Entries []browseEntry `json:"entries"`
}

func browseHandler() http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		raw := request.URL.Query().Get("path")
		if raw == "" {
			raw = "~"
		}
		showHidden := request.URL.Query().Get("showHidden") == "true"
		resolved, err := resolvePath(raw)
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

		dirEntries, err := os.ReadDir(resolved)
		if err != nil {
			if os.IsPermission(err) {
				jsonError(writer, "cannot read directory", http.StatusForbidden)
			} else {
				jsonError(writer, "cannot read directory", http.StatusInternalServerError)
			}
			return
		}

		entries := make([]browseEntry, 0)
		for _, entry := range dirEntries {
			name := entry.Name()
			if !showHidden && strings.HasPrefix(name, ".") {
				continue
			}
			isDir := entry.IsDir()
			if !isDir && entry.Type()&os.ModeSymlink != 0 {
				if target, err := os.Stat(filepath.Join(resolved, name)); err == nil {
					isDir = target.IsDir()
				}
			}
			if !isDir {
				continue
			}
			entries = append(entries, browseEntry{
				Name:  name,
				Path:  filepath.Join(resolved, name),
				IsDir: true,
			})
		}
		sort.Slice(entries, func(i, j int) bool {
			return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
		})

		parent := filepath.Dir(resolved)
		if parent == resolved {
			parent = ""
		}

		writeJSON(writer, http.StatusOK, browseResponse{
			Path:    resolved,
			Parent:  parent,
			Entries: entries,
		})
	}
}
