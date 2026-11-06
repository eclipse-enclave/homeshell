package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/logging"
)

// captureHandler records the slog records emitted during a test. min is the
// lowest level it reports as enabled, so the middleware's verbose gate can be
// exercised without changing which records the test captures.
type captureHandler struct {
	mu      sync.Mutex
	min     slog.Level
	records []slog.Record
}

func (h *captureHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.min }
func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}
func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }

func (h *captureHandler) only(t *testing.T) (slog.Record, map[string]any) {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.records) != 1 {
		t.Fatalf("got %d records, want 1", len(h.records))
	}
	r := h.records[0]
	m := map[string]any{}
	r.Attrs(func(a slog.Attr) bool { m[a.Key] = a.Value.Any(); return true })
	return r, m
}

// withCapture installs a capturing slog default at Debug for the duration of the
// test: Debug and coarser records are captured, but the verbose body gate is
// off, matching a routine debug-level run.
func withCapture(t *testing.T) *captureHandler {
	return withCaptureLevel(t, slog.LevelDebug)
}

// withCaptureVerbose installs a capturing slog default at the verbose level, so
// the middleware attaches query and bodies.
func withCaptureVerbose(t *testing.T) *captureHandler {
	return withCaptureLevel(t, logging.LevelVerbose)
}

func withCaptureLevel(t *testing.T, min slog.Level) *captureHandler {
	t.Helper()
	cap := &captureHandler{min: min}
	orig := slog.Default()
	slog.SetDefault(slog.New(cap))
	t.Cleanup(func() { slog.SetDefault(orig) })
	return cap
}

func TestLogRequestsSuccessLogsDebugNoBodies(t *testing.T) {
	cap := withCapture(t)
	h := logRequests(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/hello", nil))

	r, m := cap.only(t)
	if r.Level != slog.LevelDebug {
		t.Errorf("level = %v, want Debug", r.Level)
	}
	if m["status"] != int64(200) {
		t.Errorf("status = %v, want 200", m["status"])
	}
	if _, ok := m["response_body"]; ok {
		t.Error("response_body must be absent on success")
	}
	if _, ok := m["request_body"]; ok {
		t.Error("request_body must be absent on success")
	}
	if rec.Header().Get("X-Request-Id") == "" {
		t.Error("X-Request-Id header not set")
	}
}

func TestLogRequestsBelowVerbosePassesBodyThroughWithoutCapture(t *testing.T) {
	cap := withCapture(t)
	var got string
	h := logRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = string(b)
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/projects", strings.NewReader(`{"path":"x"}`))
	h.ServeHTTP(httptest.NewRecorder(), req)

	// Skipping the tee below verbose must not disturb what the handler reads.
	if got != `{"path":"x"}` {
		t.Errorf("handler read body = %q, want the full request body", got)
	}
	_, m := cap.only(t)
	if _, ok := m["request_body"]; ok {
		t.Error("request_body must be absent below the verbose level")
	}
}

func TestLogRequestsClientErrorLogsWarnNoBodiesBelowVerbose(t *testing.T) {
	cap := withCapture(t)
	h := logRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"bad"}`))
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/projects", strings.NewReader(`{"path":"x"}`))
	h.ServeHTTP(httptest.NewRecorder(), req)

	r, m := cap.only(t)
	if r.Level != slog.LevelWarn {
		t.Errorf("level = %v, want Warn", r.Level)
	}
	if _, ok := m["request_body"]; ok {
		t.Error("request_body must be absent below the verbose level")
	}
	if _, ok := m["response_body"]; ok {
		t.Error("response_body must be absent below the verbose level")
	}
}

func TestLogRequestsVerboseLogsQueryAndBodies(t *testing.T) {
	cap := withCaptureVerbose(t)
	h := logRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/projects?filter=active", strings.NewReader(`{"path":"x"}`))
	h.ServeHTTP(httptest.NewRecorder(), req)

	r, m := cap.only(t)
	if r.Level != slog.LevelDebug {
		t.Errorf("level = %v, want Debug", r.Level)
	}
	if got, _ := m["query"].(string); !strings.Contains(got, "filter=active") {
		t.Errorf("query = %q, want it to contain the query string", got)
	}
	if got, _ := m["request_body"].(string); !strings.Contains(got, `"path":"x"`) {
		t.Errorf("request_body = %q, want it to contain the sent body", got)
	}
	if got, _ := m["response_body"].(string); !strings.Contains(got, `"ok":true`) {
		t.Errorf("response_body = %q, want it to contain the response body", got)
	}
}

func TestLogRequestsVerboseRedactsQuerySecrets(t *testing.T) {
	cap := withCaptureVerbose(t)
	h := logRequests(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/x?token=ghp_secret&page=2", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)

	_, m := cap.only(t)
	q, _ := m["query"].(string)
	if strings.Contains(q, "ghp_secret") {
		t.Errorf("secret leaked into query: %q", q)
	}
	if !strings.Contains(q, "page=2") {
		t.Errorf("query = %q, want the non-secret page field preserved", q)
	}
}

func TestLogRequestsServerErrorLogsError(t *testing.T) {
	cap := withCapture(t)
	h := logRequests(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/x", nil))

	r, _ := cap.only(t)
	if r.Level != slog.LevelError {
		t.Errorf("level = %v, want Error", r.Level)
	}
}

func TestLogRequestsPreservesFlusher(t *testing.T) {
	withCapture(t)
	flushed := false
	h := logRequests(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		f, ok := w.(http.Flusher)
		if !ok {
			t.Error("http.Flusher lost through the middleware")
			return
		}
		w.WriteHeader(http.StatusOK)
		f.Flush()
		flushed = true
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/events", nil))
	if !flushed {
		t.Error("handler did not run to completion")
	}
}

func TestLogRequestsBodyCappedDropsUnscrubbablePrefix(t *testing.T) {
	cap := withCaptureVerbose(t)
	// A JSON body carrying a secret, padded past the cap so it is truncated
	// before the closing brace and no longer parses as JSON.
	big := `{"token":"ghp_supersecret","pad":"` + strings.Repeat("a", maxLoggedBody*2) + `"}`
	h := logRequests(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(big))
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/x", nil))

	_, m := cap.only(t)
	body, _ := m["response_body"].(string)
	if len(body) > maxLoggedBody {
		t.Errorf("logged response_body length = %d, want <= %d", len(body), maxLoggedBody)
	}
	if strings.Contains(body, "ghp_supersecret") {
		t.Errorf("secret leaked from a truncated body: %q", body)
	}
	if m["body_truncated"] != true {
		t.Errorf("body_truncated = %v, want true", m["body_truncated"])
	}
}

func TestLogRequestsIDIncrements(t *testing.T) {
	withCapture(t)
	h := logRequests(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))

	first := httptest.NewRecorder()
	h.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/api/a", nil))
	second := httptest.NewRecorder()
	h.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/api/b", nil))

	id1, _ := strconv.Atoi(first.Header().Get("X-Request-Id"))
	id2, _ := strconv.Atoi(second.Header().Get("X-Request-Id"))
	if id2 != id1+1 {
		t.Errorf("ids not sequential: %d then %d", id1, id2)
	}
}

func TestHandlerSetsRequestIDOnAPIRoutes(t *testing.T) {
	handler, _ := testHandler(t, time.Minute)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/hello", nil))

	if rec.Header().Get("X-Request-Id") == "" {
		t.Error("X-Request-Id header missing on /api response")
	}
}

func TestHandlerDoesNotSetRequestIDOnWebUIRoutes(t *testing.T) {
	handler, _ := testHandler(t, time.Minute)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if got := rec.Header().Get("X-Request-Id"); got != "" {
		t.Errorf("X-Request-Id = %q, want empty: logging middleware must wrap /api only", got)
	}
}

func TestLogRequestsLogsAndRePanicsOnPanic(t *testing.T) {
	cap := withCapture(t)
	h := logRequests(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))

	defer func() {
		if recover() == nil {
			t.Error("panic did not propagate through the middleware")
		}
		r, m := cap.only(t)
		if r.Level != slog.LevelError {
			t.Errorf("level = %v, want Error", r.Level)
		}
		if m["status"] != int64(500) {
			t.Errorf("status = %v, want 500", m["status"])
		}
		if s, _ := m["panic"].(string); !strings.Contains(s, "boom") {
			t.Errorf("panic attr = %q, want it to contain the panic value", s)
		}
	}()

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/x", nil))
}

func TestLogRequestsRedactsSecretsInErrorBodies(t *testing.T) {
	cap := withCaptureVerbose(t)
	h := logRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		w.WriteHeader(http.StatusBadRequest)
	}))

	body := `{"branch":"main","token":"ghp_supersecret"}`
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/x", strings.NewReader(body)))

	_, m := cap.only(t)
	rb, _ := m["request_body"].(string)
	if strings.Contains(rb, "ghp_supersecret") {
		t.Errorf("secret leaked into request_body: %q", rb)
	}
	if !strings.Contains(rb, "[REDACTED]") {
		t.Errorf("request_body = %q, want a redaction marker", rb)
	}
	if !strings.Contains(rb, "main") {
		t.Errorf("request_body = %q, want the non-secret branch field preserved", rb)
	}
}

func TestResponseRecorderUnwrap(t *testing.T) {
	inner := httptest.NewRecorder()
	rec := &responseRecorder{ResponseWriter: inner}
	if rec.Unwrap() != inner {
		t.Error("Unwrap did not return the underlying ResponseWriter")
	}
}
