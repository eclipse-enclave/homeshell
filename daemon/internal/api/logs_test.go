package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/logging"
	"github.com/eclipsesource/homeshell/daemon/internal/notify"
)

func TestLogsStreamDeliversRecords(t *testing.T) {
	orig := slog.Default()
	t.Cleanup(func() { slog.SetDefault(orig) })

	hub := logging.Configure(io.Discard, slog.LevelInfo)
	server := httptest.NewServer(handlerForTest(Deps{Logs: hub, Heartbeat: time.Minute, Notifications: notify.NewHub()}))
	defer server.Close()

	stream, closeStream := openStream(t, server.URL+"/api/logs")
	defer closeStream()

	// The subscription is registered by the time the stream's headers flush,
	// so a record logged now reaches this client.
	slog.Info("stream-me", "detail", "yes")

	ev := readEvent(t, stream)
	if ev.Type != "log" {
		t.Fatalf("event type = %q, want log", ev.Type)
	}
	if ev.ID != "1" {
		t.Errorf("event id = %q, want 1", ev.ID)
	}
	if !strings.Contains(ev.Data, "stream-me") || !strings.Contains(ev.Data, "detail") {
		t.Errorf("payload lacks the record: %s", ev.Data)
	}
}

func TestLogsStreamHeartbeatsWhenIdle(t *testing.T) {
	orig := slog.Default()
	t.Cleanup(func() { slog.SetDefault(orig) })

	hub := logging.Configure(io.Discard, slog.LevelError)
	server := httptest.NewServer(handlerForTest(Deps{Logs: hub, Heartbeat: 30 * time.Millisecond, Notifications: notify.NewHub()}))
	defer server.Close()

	stream, closeStream := openStream(t, server.URL+"/api/logs")
	defer closeStream()

	// Nothing is logged at this level, so the stream sends only heartbeats.
	for i := 0; i < 2; i++ {
		if ev := readEvent(t, stream); ev.Type != "heartbeat" {
			t.Fatalf("idle event type = %q, want heartbeat", ev.Type)
		}
	}
}

func TestLogsStreamUnavailableWithoutHub(t *testing.T) {
	server := httptest.NewServer(handlerForTest(Deps{Heartbeat: time.Minute, Notifications: notify.NewHub()}))
	defer server.Close()

	res, err := http.Get(server.URL + "/api/logs")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", res.StatusCode, http.StatusServiceUnavailable)
	}
}
