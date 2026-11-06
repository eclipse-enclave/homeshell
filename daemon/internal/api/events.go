package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/notify"
	"github.com/eclipsesource/homeshell/daemon/internal/store"
)

func init() {
	register("GET /api/events", func(d Deps) http.HandlerFunc {
		return eventsHandler(d.Store, d.Notifications, d.Heartbeat)
	})
}

// eventsHandler streams state to a client as docs/ARCHITECTURE.md, "Pushing
// state to clients", describes: a full-state snapshot event on connect, a
// full-state event on every change, and heartbeat events while idle. It also
// multiplexes ephemeral "notification" events (issue #66) onto the same
// stream: those are fire-once alerts, not state, so they carry no snapshot and
// are never replayed on reconnect. The hub is a required dependency (whoever
// builds Deps supplies it), so there is no nil case to guard.
func eventsHandler(st *store.Store, hub *notify.Hub, heartbeat time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-store")

		// Subscribe before snapshotting so no change slips between the two;
		// a duplicated event is harmless since every event carries the full
		// state.
		events, cancel := st.Subscribe()
		defer cancel()

		notifications, cancelNotifications := hub.Subscribe()
		defer cancelNotifications()

		if err := writeEvent(w, st.Snapshot()); err != nil {
			return
		}
		flusher.Flush()

		timer := time.NewTimer(heartbeat)
		defer timer.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case ev, ok := <-events:
				if !ok {
					// Dropped as too slow; the client reconnects.
					return
				}
				if err := writeEvent(w, ev); err != nil {
					return
				}
				flusher.Flush()
				resetTimer(timer, heartbeat)
			case notification, ok := <-notifications:
				if !ok {
					// The hub closed this subscriber (only on cancel); stop the
					// stream — the client reconnects.
					return
				}
				if err := writeNotification(w, notification); err != nil {
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

func writeEvent(w io.Writer, ev store.Event) error {
	_, err := fmt.Fprintf(w, "event: %s\nid: %d\ndata: %s\n\n", ev.Type, ev.ID, ev.Data)
	return err
}

// writeNotification serializes a notification as an SSE "notification" event.
// It deliberately omits the SSE `id:` field: notification IDs live in a
// different namespace from the monotonic state-event ids, and leaving them out
// keeps the browser's Last-Event-ID a clean state cursor (a notification can't
// become the last id before a reconnect).
func writeNotification(writer io.Writer, notification notify.Notification) error {
	data, err := json.Marshal(notification)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(writer, "event: notification\ndata: %s\n\n", data)
	return err
}

// resetTimer restarts a possibly-fired timer (stop-drain-reset pattern).
func resetTimer(t *time.Timer, d time.Duration) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	t.Reset(d)
}
