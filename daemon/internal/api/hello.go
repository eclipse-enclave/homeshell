package api

import (
	"fmt"
	"net/http"
)

func init() {
	register("GET /api/hello", func(d Deps) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"message":"Hello from the HomeShell daemon","version":%q}`, d.Version)
		}
	})
}
