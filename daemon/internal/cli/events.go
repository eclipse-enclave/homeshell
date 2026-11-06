package cli

// The `events` verb: the daemon's Server-Sent-Events feed relayed as
// newline-delimited JSON, one object per line, so `homeshell events | jq` is a
// live view of everything the web UI reacts to.
//
// It is a translation and nothing more: each line carries the event name, its id
// and the daemon's own `data` bytes untouched, so a client of this stream sees the
// same payloads the UI does.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/eclipsesource/homeshell/daemon/internal/apiclient"
)

func init() {
	register(Command{
		Name:    "events",
		Summary: "Stream the daemon's event feed as newline-delimited JSON",
		Run:     runEvents,
	})
}

// ndjsonEvent is one line of output.
type ndjsonEvent struct {
	Event string          `json:"event"`
	ID    string          `json:"id,omitempty"`
	Data  json.RawMessage `json:"data"`
}

// runEvents streams until interrupted, so it does not go through
// clientCommand.dispatch: that bounds every call with --timeout, which is exactly
// wrong for a feed meant to stay open.
func runEvents(env Env, args []string) int {
	fs := flag.NewFlagSet("events", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	fs.Usage = func() {
		fmt.Fprint(env.Stderr, "Usage: homeshell events [flags]\n\n"+
			"Relay the daemon's SSE feed as newline-delimited JSON until interrupted.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	var options clientOptions
	options.register(fs)
	heartbeats := fs.Bool("heartbeats", false, "also emit the idle heartbeat events (they carry no state)")
	positional, err := parseInterspersed(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if len(positional) > 0 {
		return reportError(env, fs, surplusArgsError(positional))
	}

	client, err := options.connect()
	if err != nil {
		return reportError(env, fs, err)
	}

	// Ctrl-C (or SIGTERM from a supervisor) is how this verb is meant to end, so
	// it is a clean exit rather than a failure.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := streamEvents(ctx, client, env.Stdout, *heartbeats); err != nil {
		if ctx.Err() != nil {
			return exitOK
		}
		return reportError(env, fs, err)
	}
	return exitOK
}

// streamEvents reads the SSE feed and writes one JSON object per event. It
// returns when the stream ends — the daemon restarting or going away — which is a
// failure for the caller to retry, not something to paper over by reconnecting
// behind their back.
func streamEvents(ctx context.Context, client *apiclient.Client, out io.Writer, heartbeats bool) error {
	res, err := client.Do(ctx, http.MethodGet, "/api/events", nil)
	if err != nil {
		return transportError(ctx, client, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 8*1024))
		return newAPIError(res.StatusCode, body)
	}

	// A bufio.Reader rather than a Scanner: a full-state event grows with the
	// number of projects and would eventually exceed a Scanner's token limit,
	// which surfaces as the stream simply stopping.
	reader := bufio.NewReader(res.Body)
	var event, id string
	var data []string
	for {
		line, err := reader.ReadString('\n')
		if line != "" {
			trimmed := strings.TrimRight(line, "\r\n")
			if trimmed == "" {
				// Blank line: the event is complete.
				if err := emitEvent(out, event, id, data, heartbeats); err != nil {
					return err
				}
				event, id, data = "", "", nil
			} else if name, value, found := strings.Cut(trimmed, ":"); found {
				// SSE allows one optional space after the colon.
				value = strings.TrimPrefix(value, " ")
				switch name {
				case "event":
					event = value
				case "id":
					id = value
				case "data":
					data = append(data, value)
				}
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return errors.New("the daemon's event stream ended")
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("reading the event stream: %w", err)
		}
	}
}

// emitEvent writes one completed SSE event as a JSON line. Heartbeats are dropped
// unless asked for: they exist to keep the connection warm and carry no state, so
// they would be pure noise in a piped stream.
func emitEvent(out io.Writer, event, id string, data []string, heartbeats bool) error {
	if event == "" && len(data) == 0 {
		return nil
	}
	if event == "" {
		// SSE's default event name for a nameless event. The daemon names all of
		// its events, so this only keeps a stray one identifiable rather than
		// emitting an empty name.
		event = "message"
	}
	if event == "heartbeat" && !heartbeats {
		return nil
	}
	joined := strings.Join(data, "\n")
	payload := json.RawMessage(joined)
	if json.Valid(payload) {
		// Compact it: SSE allows an event's data to span several lines, and a raw
		// newline inside the object would split one event across two output lines
		// and break the one-object-per-line contract. Compacting only removes
		// whitespace between tokens, so the payload stays the daemon's.
		var compacted bytes.Buffer
		if err := json.Compact(&compacted, payload); err != nil {
			return err
		}
		payload = compacted.Bytes()
	} else {
		// Not JSON (nothing the daemon sends today, but this stream is a contract
		// the CLI does not own): pass it through as a JSON string so every line
		// stays parseable.
		encoded, err := json.Marshal(joined)
		if err != nil {
			return err
		}
		payload = encoded
	}
	line, err := json.Marshal(ndjsonEvent{Event: event, ID: id, Data: payload})
	if err != nil {
		return err
	}
	// One Write per event, and stdout is unbuffered, so a piped consumer sees each
	// event as it happens. Do not wrap this writer in a bufio.Writer without
	// flushing per event — a live feed that arrives 4 KiB at a time is not live.
	_, err = out.Write(append(line, '\n'))
	return err
}
