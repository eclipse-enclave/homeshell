// Package logging configures the daemon's slog default handler and parses
// human-supplied log levels.
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// LevelVerbose is a level finer than Debug for diagnostic output that is too
// voluminous or too sensitive for routine Debug runs, such as full request and
// response bodies on the API access log.
const LevelVerbose = slog.LevelDebug - 4

// ParseLevel maps a case-insensitive level name to a slog.Level.
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "verbose":
		return LevelVerbose, nil
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("invalid log level %q (want verbose, debug, info, warn, or error)", s)
	}
}

// LevelName maps a slog.Level to its canonical HomeShell level name, the
// inverse of ParseLevel for the levels the daemon uses. Any other value falls
// back to slog's lower-cased label.
func LevelName(level slog.Level) string {
	switch level {
	case LevelVerbose:
		return "verbose"
	case slog.LevelDebug:
		return "debug"
	case slog.LevelInfo:
		return "info"
	case slog.LevelWarn:
		return "warn"
	case slog.LevelError:
		return "error"
	default:
		return strings.ToLower(level.String())
	}
}

// Level is a live, settable log level: a thin wrapper over slog.LevelVar so the
// handler's threshold can be changed at runtime (e.g. from the API) without
// rebuilding the logger. It satisfies slog.Leveler.
type Level struct {
	v slog.LevelVar
}

// NewLevel returns a Level initialized to l.
func NewLevel(l slog.Level) *Level {
	lvl := &Level{}
	lvl.v.Set(l)
	return lvl
}

// Level reports the current threshold, satisfying slog.Leveler.
func (l *Level) Level() slog.Level { return l.v.Level() }

// Set changes the threshold; it takes effect immediately for the handler that
// was Configured with this Level.
func (l *Level) Set(x slog.Level) { l.v.Set(x) }

// Configure installs the slog default handler at the given level and returns a
// Hub that fans emitted records out to subscribers (the log-stream endpoint).
// Passing a *Level (rather than a plain slog.Level) keeps the threshold live,
// so the stream carries exactly what is being logged. The default handler is a
// fanoutHandler wrapping a text handler that writes to w (stderr).
func Configure(w io.Writer, level slog.Leveler) *Hub {
	opts := &slog.HandlerOptions{Level: level, ReplaceAttr: nameCustomLevels}
	hub := NewHub()
	slog.SetDefault(slog.New(&fanoutHandler{inner: slog.NewTextHandler(w, opts), hub: hub}))
	return hub
}

// fanoutHandler writes each record through an inner handler (the text handler
// to stderr) and also publishes it to a Hub for live streaming. Only records
// the inner handler admits reach Handle (slog gates on Enabled first), so the
// stream follows the configured level. Attributes attached via a With-derived
// logger are not published, only those on the record itself; the daemon logs
// through the top-level slog functions with inline attrs, so this loses
// nothing in practice.
type fanoutHandler struct {
	inner slog.Handler
	hub   *Hub
}

func (h *fanoutHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *fanoutHandler) Handle(ctx context.Context, r slog.Record) error {
	err := h.inner.Handle(ctx, r)
	h.hub.broadcast(recordFrom(r))
	return err
}

func (h *fanoutHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &fanoutHandler{inner: h.inner.WithAttrs(attrs), hub: h.hub}
}

func (h *fanoutHandler) WithGroup(name string) slog.Handler {
	return &fanoutHandler{inner: h.inner.WithGroup(name), hub: h.hub}
}

// recordFrom converts an slog.Record into a streamable Record, naming the level
// with LevelName and flattening its attributes into a map.
func recordFrom(r slog.Record) Record {
	rec := Record{Time: r.Time, Level: LevelName(r.Level), Msg: r.Message}
	if r.NumAttrs() > 0 {
		rec.Attrs = make(map[string]any, r.NumAttrs())
		r.Attrs(func(a slog.Attr) bool {
			rec.Attrs[a.Key] = attrValue(a.Value)
			return true
		})
	}
	return rec
}

// attrValue resolves an slog value to a JSON-friendly Go value, recursing into
// groups so nested attributes stream as nested objects.
func attrValue(v slog.Value) any {
	v = v.Resolve()
	if v.Kind() == slog.KindGroup {
		group := v.Group()
		m := make(map[string]any, len(group))
		for _, a := range group {
			m[a.Key] = attrValue(a.Value)
		}
		return m
	}
	return v.Any()
}

// nameCustomLevels labels the daemon's non-standard levels so records read as
// their name rather than slog's default offset form (e.g. "DEBUG-4").
func nameCustomLevels(_ []string, a slog.Attr) slog.Attr {
	if a.Key == slog.LevelKey {
		if lvl, ok := a.Value.Any().(slog.Level); ok && lvl == LevelVerbose {
			a.Value = slog.StringValue("VERBOSE")
		}
	}
	return a
}
