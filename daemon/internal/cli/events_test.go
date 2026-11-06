package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/eclipsesource/homeshell/daemon/internal/apiclient"
)

// sseServer serves one canned SSE body and returns a client aimed at it.
func sseServer(t *testing.T, body string) *apiclient.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		writer.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return apiclient.New(strings.TrimPrefix(server.URL, "http://"), "")
}

func TestStreamEvents(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		heartbeats bool
		wantLines  []string
	}{
		{
			name: "one line per event, with the daemon's data untouched",
			body: "event: state\nid: 7\ndata: {\"projects\":[],\"unknownToTheCLI\":1}\n\n" +
				"event: notification\ndata: {\"kind\":\"waiting\"}\n\n",
			wantLines: []string{
				`{"event":"state","id":"7","data":{"projects":[],"unknownToTheCLI":1}}`,
				`{"event":"notification","data":{"kind":"waiting"}}`,
			},
		},
		{
			name:      "heartbeats are dropped by default",
			body:      "event: heartbeat\ndata: {}\n\nevent: state\ndata: {}\n\n",
			wantLines: []string{`{"event":"state","data":{}}`},
		},
		{
			name:       "--heartbeats keeps them",
			body:       "event: heartbeat\ndata: {}\n\n",
			heartbeats: true,
			wantLines:  []string{`{"event":"heartbeat","data":{}}`},
		},
		{
			// SSE lets one event's data span several lines; the output must still be
			// a single line, or a consumer reading line-by-line sees half an event.
			name:      "a multi-line data field becomes one line",
			body:      "event: state\ndata: {\"a\":\ndata: 1}\n\n",
			wantLines: []string{`{"event":"state","data":{"a":1}}`},
		},
		{
			name: "data that is not JSON still yields a parseable line",
			body: "event: odd\ndata: plain text\n\n",
			// json.Marshal of the string, so every line stays machine-readable.
			wantLines: []string{`{"event":"odd","data":"plain text"}`},
		},
		{
			name:      "a comment line is ignored",
			body:      ": keep-alive\nevent: state\ndata: {}\n\n",
			wantLines: []string{`{"event":"state","data":{}}`},
		},
		{
			name:      "a nameless event gets SSE's default name",
			body:      "data: {\"a\":1}\n\n",
			wantLines: []string{`{"event":"message","data":{"a":1}}`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			err := streamEvents(context.Background(), sseServer(t, tt.body), &out, tt.heartbeats)
			// The canned body always ends, which is reported rather than hidden.
			if err == nil || !strings.Contains(err.Error(), "event stream ended") {
				t.Fatalf("error = %v, want the stream-ended report", err)
			}
			lines := splitLines(out.String())
			if len(lines) != len(tt.wantLines) {
				t.Fatalf("got %d lines, want %d:\n%s", len(lines), len(tt.wantLines), out.String())
			}
			for i, want := range tt.wantLines {
				if lines[i] != want {
					t.Errorf("line %d = %s, want %s", i, lines[i], want)
				}
			}
			for _, line := range lines {
				if !json.Valid([]byte(line)) {
					t.Errorf("line is not valid JSON: %s", line)
				}
			}
		})
	}
}

func splitLines(out string) []string {
	trimmed := strings.TrimRight(out, "\n")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

// A refused stream must not look like an empty feed.
func TestStreamEventsReportsARefusal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
		writer.Write([]byte(`{"error":"unauthorized"}`))
	}))
	t.Cleanup(server.Close)

	var out bytes.Buffer
	err := streamEvents(context.Background(), apiclient.New(strings.TrimPrefix(server.URL, "http://"), ""), &out, false)
	if err == nil || !strings.Contains(err.Error(), "unauthorized") {
		t.Fatalf("error = %v, want the daemon's refusal", err)
	}
	if out.Len() != 0 {
		t.Errorf("output = %q, want nothing", out.String())
	}
}

// An interrupt is how this verb is meant to end, so the cancelled context has to
// surface as ctx.Err() (which runEvents turns into exit 0) and not as a stream
// fault.
func TestStreamEventsHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var out bytes.Buffer
	err := streamEvents(ctx, sseServer(t, "event: state\ndata: {}\n\n"), &out, false)
	if err == nil {
		t.Fatal("expected an error from a cancelled context")
	}
	if !strings.Contains(err.Error(), context.Canceled.Error()) {
		t.Errorf("error = %v, want it to report the cancellation", err)
	}
}

func TestEventsVerbFlagHandling(t *testing.T) {
	t.Run("-h succeeds", func(t *testing.T) {
		code, _, _ := runVerb(runEvents, []string{"-h"})
		if code != exitOK {
			t.Errorf("exit code = %d, want 0", code)
		}
	})

	t.Run("an unknown flag is a usage error", func(t *testing.T) {
		code, _, _ := runVerb(runEvents, []string{"-nope"})
		if code != exitUsage {
			t.Errorf("exit code = %d, want %d", code, exitUsage)
		}
	})

	t.Run("a positional argument is a usage error", func(t *testing.T) {
		isolateStateFile(t)
		code, _, stderr := runVerb(runEvents, []string{"oops"})
		if code != exitUsage {
			t.Errorf("exit code = %d, want %d", code, exitUsage)
		}
		if !strings.Contains(stderr, "unexpected argument") {
			t.Errorf("stderr = %q, want it to reject the argument", stderr)
		}
	})

	t.Run("no daemon exits with the no-daemon code", func(t *testing.T) {
		isolateStateFile(t)
		code, _, stderr := runVerb(runEvents, nil)
		if code != exitNoDaemon {
			t.Errorf("exit code = %d, want %d", code, exitNoDaemon)
		}
		if !strings.Contains(stderr, "no daemon is running") {
			t.Errorf("stderr = %q, want it to report no daemon", stderr)
		}
	})
}
