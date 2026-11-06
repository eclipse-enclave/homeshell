package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"reflect"

	"github.com/eclipsesource/homeshell/daemon/internal/settings"
)

func init() {
	register("GET /api/settings", func(d Deps) http.HandlerFunc {
		return getSettingsHandler(d.Settings)
	})
	register("PUT /api/settings", func(d Deps) http.HandlerFunc {
		return putSettingsHandler(d)
	})
}

// getSettingsHandler serves the daemon-persisted client settings.
func getSettingsHandler(store *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			jsonError(w, "settings are not available", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, http.StatusOK, store.Get())
	}
}

// putSettingsHandler replaces the stored settings, validating the body and
// persisting it atomically.
//
// A key the body omits takes its default, not the zero value — the body is
// decoded onto a defaulted document, the same rule settings.Load applies to a
// file on disk. Decoding onto a zero value instead makes every absent key a
// silent instruction: a client written against an older schema (or a script
// sending the two fields it cares about) would turn off every setting it had
// never heard of, and a `false` that means "not mentioned" is indistinguishable
// from one the user chose, so Validate cannot catch it.
func putSettingsHandler(d Deps) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if d.Settings == nil {
			jsonError(writer, "settings are not available", http.StatusServiceUnavailable)
			return
		}
		next := settings.Defaults()
		// Decoding into a non-empty slice reuses its elements, so a first entry sent
		// without an id would inherit the default terminal row's id and flags and
		// overwrite it. Start the list empty; normalization restores the terminal row.
		next.OpenWith.Entries = nil
		if err := json.NewDecoder(request.Body).Decode(&next); err != nil {
			jsonError(writer, "invalid request body", http.StatusBadRequest)
			return
		}
		previous := d.Settings.Get()
		stored, err := d.Settings.Set(next)
		if err != nil {
			// A validation error is the client's fault (400); anything else is a
			// persistence failure on the daemon (500).
			var invalid *settings.ValidationError
			if errors.As(err, &invalid) {
				jsonError(writer, err.Error(), http.StatusBadRequest)
			} else {
				jsonError(writer, "could not persist settings", http.StatusInternalServerError)
			}
			return
		}
		// The "open with" entries become open modes on every workstream, so
		// re-project when they change and every client is pushed an Open menu
		// matching what was just saved.
		if d.Providers != nil && !reflect.DeepEqual(previous.OpenWith, stored.OpenWith) {
			d.Providers.Refresh()
		}
		// Echo the document this request stored, not a re-read, so concurrent
		// writers cannot change the response. Normalization may have assigned ids.
		writeJSON(writer, http.StatusOK, stored)
	}
}
