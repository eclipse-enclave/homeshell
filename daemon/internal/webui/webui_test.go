package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

var builtUI = fstest.MapFS{
	"index.html":    {Data: []byte("<html>HomeShell UI</html>")},
	"assets/app.js": {Data: []byte("console.log('hi')")},
}

func get(t *testing.T, handler http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestServesIndex(t *testing.T) {
	rec := get(t, handlerFor(builtUI), "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), "HomeShell UI") {
		t.Errorf("body = %q, want the index page", rec.Body.String())
	}
}

func TestServesStaticAssets(t *testing.T) {
	rec := get(t, handlerFor(builtUI), "/assets/app.js")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), "console.log") {
		t.Errorf("body = %q, want the asset content", rec.Body.String())
	}
}

func TestFallsBackToIndexForClientRoutes(t *testing.T) {
	rec := get(t, handlerFor(builtUI), "/projects/some-id")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), "HomeShell UI") {
		t.Errorf("body = %q, want the index page as SPA fallback", rec.Body.String())
	}
}

func TestHintsWhenUINotEmbedded(t *testing.T) {
	rec := get(t, handlerFor(fstest.MapFS{}), "/")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if !strings.Contains(rec.Body.String(), "make build") {
		t.Errorf("body = %q, want a hint pointing at `make build`", rec.Body.String())
	}
}
