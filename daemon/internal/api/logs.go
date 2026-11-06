package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/logging"
)

func init() {
	register("GET /api/logs", func(d Deps) http.HandlerFunc {
		return logsHandler(d.Logs, d.Heartbeat)
	})
}

// logsHandler streams the daemon's log records to a client as they are emitted,
// for the web log viewer. Unlike the state stream it sends no initial snapshot:
// the viewer shows only entries logged from the moment it connects. It opens
// with a heartbeat, each record is one SSE "log" event, and heartbeats keep the
// connection alive while idle. What
// reaches the stream follows the daemon's live log level.
func logsHandler(hub *logging.Hub, heartbeat time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if hub == nil {
			jsonError(w, "log streaming is not available", http.StatusServiceUnavailable)
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-store")

		records, cancel := hub.Subscribe()
		defer cancel()

		// Open with a heartbeat rather than bare headers: with no snapshot to
		// send, a proxy that holds headers until the first body byte (the vite
		// dev server) would leave a quiet daemon's viewer "connecting".
		if _, err := io.WriteString(w, "event: heartbeat\ndata: {}\n\n"); err != nil {
			return
		}
		flusher.Flush()

		timer := time.NewTimer(heartbeat)
		defer timer.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case rec, ok := <-records:
				if !ok {
					// Dropped as too slow; the client reconnects.
					return
				}
				if err := writeLog(w, rec); err != nil {
					return
				}
				flusher.Flush()
				resetTimer(timer, heartbeat)
			case <-timer.C:
				if _, err := io.WriteString(w, "event: heartbeat\ndata: {}\n\n"); err != nil {
					return
				}
				flusher.Flush()
				timer.Reset(heartbeat)
			}
		}
	}
}

// writeLog frames one log record as an SSE "log" event carrying its sequence
// number as the event id and its JSON as the data.
func writeLog(w io.Writer, rec logging.Record) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "event: log\nid: %d\ndata: %s\n\n", rec.Seq, data)
	return err
}
