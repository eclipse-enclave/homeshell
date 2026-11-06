package api

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/store"
)

// sseEvent is one parsed server-sent event.
type sseEvent struct {
	Type string
	ID   string
	Data string
}

// readEvent parses the next event block from an SSE stream.
func readEvent(t *testing.T, r *bufio.Reader) sseEvent {
	t.Helper()
	ev := sseEvent{Type: "message"}
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("reading SSE stream: %v", err)
		}
		line = strings.TrimRight(line, "\n")
		switch {
		case line == "":
			return ev
		case strings.HasPrefix(line, "event: "):
			ev.Type = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "id: "):
			ev.ID = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "data: "):
			ev.Data += strings.TrimPrefix(line, "data: ")
		}
	}
}

func openStream(t *testing.T, url string) (*bufio.Reader, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatalf("connecting to SSE stream: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		cancel()
		t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusOK)
	}
	if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
	return bufio.NewReader(res.Body), func() {
		cancel()
		res.Body.Close()
	}
}

func TestEventsStreamSnapshotAndUpdates(t *testing.T) {
	handler, st := testHandler(t, time.Minute)
	server := httptest.NewServer(handler)
	defer server.Close()

	stream, closeStream := openStream(t, server.URL+"/api/events")
	defer closeStream()

	// The stream must open with a full-state snapshot.
	snapshot := readEvent(t, stream)
	if snapshot.Type != "state" {
		t.Fatalf("first event type = %q, want state", snapshot.Type)
	}
	if !strings.Contains(snapshot.Data, `"projects"`) {
		t.Errorf("snapshot payload lacks projects: %s", snapshot.Data)
	}

	// A mutation must be pushed as a new full-state event.
	session := domain.Session{ID: "sess-x", Title: "X", Agent: "Demo", State: domain.SessionRunning}
	if err := st.AddSession(store.DemoProjectID, store.DemoWorkstreamID, session); err != nil {
		t.Fatal(err)
	}
	update := readEvent(t, stream)
	if update.Type != "state" {
		t.Fatalf("update event type = %q, want state", update.Type)
	}
	if update.ID != "1" {
		t.Errorf("update event id = %q, want 1", update.ID)
	}
	if !strings.Contains(update.Data, `"sess-x"`) {
		t.Errorf("update payload lacks the new session: %s", update.Data)
	}
}

func TestEventsStreamHeartbeatsWhenIdle(t *testing.T) {
	handler, _ := testHandler(t, 30*time.Millisecond)
	server := httptest.NewServer(handler)
	defer server.Close()

	stream, closeStream := openStream(t, server.URL+"/api/events")
	defer closeStream()

	if ev := readEvent(t, stream); ev.Type != "state" {
		t.Fatalf("first event type = %q, want state", ev.Type)
	}
	for i := 0; i < 2; i++ {
		if ev := readEvent(t, stream); ev.Type != "heartbeat" {
			t.Fatalf("idle event type = %q, want heartbeat", ev.Type)
		}
	}
}
