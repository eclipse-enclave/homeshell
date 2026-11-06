package api

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/logging"
)

// maxLoggedBody caps how many bytes of a request/response body are captured for
// logging, so large or streaming payloads cannot exhaust memory.
const maxLoggedBody = 4 << 10 // 4 KiB

// requestCounter assigns a monotonic per-process request ID for log
// correlation.
var requestCounter atomic.Uint64

// logRequests logs one slog record per REST API request: Debug for <400, Warn
// for 4xx, Error for 5xx. When the verbose level is enabled, the record also
// carries the query string and the request and response bodies, each capped and
// with secret-looking fields redacted; below verbose no bodies are logged, so a
// rejected secret payload never reaches a routine info- or debug-level run. That
// level is checked once up front: below verbose the request tee and the response
// body copy are skipped entirely, so the common path captures nothing. The
// record is emitted from a defer so a panicking handler still produces a
// correlated 5xx line; the panic is then re-raised for the http.Server to
// handle. The wrapped ResponseWriter preserves http.Flusher so SSE streaming
// keeps working.
func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := requestCounter.Add(1)
		w.Header().Set("X-Request-Id", strconv.FormatUint(id, 10))

		verbose := slog.Default().Enabled(r.Context(), logging.LevelVerbose)

		var reqBody *limitedBuffer
		if verbose && r.Body != nil {
			reqBody = &limitedBuffer{limit: maxLoggedBody}
			r.Body = teeReadCloser{Reader: io.TeeReader(r.Body, reqBody), Closer: r.Body}
		}

		rec := &responseRecorder{ResponseWriter: w}
		if verbose {
			rec.body = &limitedBuffer{limit: maxLoggedBody}
		}
		start := time.Now()

		defer func() {
			panicked := recover()
			if rec.status == 0 {
				if panicked != nil {
					rec.status = http.StatusInternalServerError
				} else {
					rec.status = http.StatusOK
				}
			}
			logRequest(id, r, rec, reqBody, time.Since(start), panicked, verbose)
			if panicked != nil {
				panic(panicked)
			}
		}()

		next.ServeHTTP(rec, r)
	})
}

// logRequest emits the access-log record for one request at a level matched to
// its status. On a panic, panicked is non-nil and is included in the record.
// When verbose is set, reqBody and rec.body hold the captured bodies (either may
// be nil when there was none) and the query and bodies are logged.
func logRequest(id uint64, r *http.Request, rec *responseRecorder, reqBody *limitedBuffer, duration time.Duration, panicked any, verbose bool) {
	attrs := []any{
		"request_id", id,
		"method", r.Method,
		"path", r.URL.Path,
		"status", rec.status,
		"duration", duration,
		"bytes", rec.bytes,
		"remote_addr", r.RemoteAddr,
	}
	if panicked != nil {
		attrs = append(attrs, "panic", fmt.Sprint(panicked))
	}
	if verbose {
		if q := scrubQuery(r.URL.Query()); q != "" {
			attrs = append(attrs, "query", q)
		}
		reqStr, reqCapped := reqBody.state()
		respStr, respCapped := rec.body.state()
		attrs = append(attrs,
			"request_body", scrubBody(reqStr, reqCapped),
			"response_body", scrubBody(respStr, respCapped),
		)
		if reqCapped || respCapped {
			attrs = append(attrs, "body_truncated", true)
		}
	}

	const msg = "api request"
	switch {
	case rec.status >= 500:
		slog.Error(msg, attrs...)
	case rec.status >= 400:
		slog.Warn(msg, attrs...)
	default:
		slog.Debug(msg, attrs...)
	}
}

// responseRecorder captures status and byte count while writing through to the
// underlying ResponseWriter. body holds a capped copy of the response only when
// non-nil (set at the verbose level); otherwise the body is not captured.
type responseRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
	body   *limitedBuffer
}

func (r *responseRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *responseRecorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(p)
	r.bytes += n
	if r.body != nil {
		r.body.Write(p[:n])
	}
	return n, err
}

// Flush passes through to the underlying writer so SSE handlers that type-assert
// http.Flusher keep streaming.
func (r *responseRecorder) Flush() {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap exposes the underlying writer so http.ResponseController can reach
// optional interfaces (Flusher, Hijacker, deadline setters) it implements.
func (r *responseRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

// limitedBuffer accumulates bytes up to limit, then drops the rest and records
// that truncation happened. Write always reports the full length so it can back
// an io.TeeReader without disturbing the real read count.
type limitedBuffer struct {
	buf    bytes.Buffer
	limit  int
	capped bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if remaining := b.limit - b.buf.Len(); remaining > 0 {
		if len(p) > remaining {
			b.buf.Write(p[:remaining])
			b.capped = true
		} else {
			b.buf.Write(p)
		}
	} else if len(p) > 0 {
		b.capped = true
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string { return b.buf.String() }

// state returns the captured bytes and whether they were truncated, treating a
// nil buffer (no body captured) as empty and untruncated.
func (b *limitedBuffer) state() (string, bool) {
	if b == nil {
		return "", false
	}
	return b.buf.String(), b.capped
}

// teeReadCloser reads through an io.Reader (the tee) while closing the original
// body, so capturing the request body does not drop its Close.
type teeReadCloser struct {
	io.Reader
	io.Closer
}
