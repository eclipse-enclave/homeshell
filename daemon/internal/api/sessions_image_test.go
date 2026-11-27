package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/eclipsesource/homeshell/daemon/internal/provider"
)

// postImage posts body to a session's image endpoint as contentType.
func postImage(t *testing.T, handler http.Handler, path, contentType string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestSessionImageEndpoint(t *testing.T) {
	handler, projectStore := newProviderHandler(t)
	workstream := createTestWorkstream(t, handler)
	session := createTestSession(t, handler, projectStore, workstream.ID)
	path := "/api/projects/proj-test/workstreams/" + workstream.ID + "/sessions/" + session.ID + "/image"

	png := []byte("\x89PNG\r\n\x1a\nrest")
	jpeg := []byte("\xff\xd8\xffrest")
	tests := []struct {
		name        string
		contentType string
		body        []byte
		wantStatus  int
	}{
		{name: "png", contentType: "image/png", body: png, wantStatus: http.StatusOK},
		{name: "jpeg", contentType: "image/jpeg", body: jpeg, wantStatus: http.StatusOK},
		{name: "media type with a parameter", contentType: "image/png; name=shot.png", body: png, wantStatus: http.StatusOK},
		{name: "media type in upper case", contentType: "IMAGE/PNG", body: png, wantStatus: http.StatusOK},
		{name: "gif", contentType: "image/gif", body: []byte("GIF89a"), wantStatus: http.StatusBadRequest},
		{name: "no content type", contentType: "", body: png, wantStatus: http.StatusBadRequest},
		{name: "malformed content type", contentType: "image/png; =", body: png, wantStatus: http.StatusBadRequest},
		{name: "bytes that are not what they claim", contentType: "image/png", body: jpeg, wantStatus: http.StatusBadRequest},
		{name: "empty body", contentType: "image/png", body: nil, wantStatus: http.StatusBadRequest},
		{name: "exactly the limit", contentType: "image/png",
			body: append(png, make([]byte, provider.MaxImageBytes-len(png))...), wantStatus: http.StatusOK},
		{name: "past the limit without a content type", contentType: "",
			body: make([]byte, provider.MaxImageBytes+1), wantStatus: http.StatusRequestEntityTooLarge},
		{name: "past the limit", contentType: "image/png",
			body: append(png, make([]byte, provider.MaxImageBytes)...), wantStatus: http.StatusRequestEntityTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := postImage(t, handler, path, tt.contentType, tt.body)
			if recorder.Code != tt.wantStatus {
				t.Fatalf("status = %d (%s), want %d", recorder.Code, recorder.Body.String(), tt.wantStatus)
			}
			var response struct {
				Path  string `json:"path"`
				Error string `json:"error"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatalf("body %q is not JSON: %v", recorder.Body.String(), err)
			}
			if tt.wantStatus == http.StatusOK && !strings.HasPrefix(response.Path, "/mnt/host-images/") {
				t.Errorf("path = %q, want one under /mnt/host-images/", response.Path)
			}
			if tt.wantStatus != http.StatusOK && response.Error == "" {
				t.Error("error is empty, want a message")
			}
		})
	}
}

func TestSessionImageStoppedReturns409(t *testing.T) {
	handler, projectStore := newProviderHandler(t)
	workstream := createTestWorkstream(t, handler)
	session := createTestSession(t, handler, projectStore, workstream.ID)
	base := "/api/projects/proj-test/workstreams/" + workstream.ID + "/sessions/" + session.ID

	if recorder := do(t, handler, http.MethodPost, base+"/stop", ""); recorder.Code != http.StatusNoContent {
		t.Fatalf("stop status = %d (%s)", recorder.Code, recorder.Body.String())
	}
	if recorder := postImage(t, handler, base+"/image", "image/png", []byte("\x89PNG\r\n\x1a\n")); recorder.Code != http.StatusConflict {
		t.Fatalf("image on stopped session = %d, want 409 (%s)", recorder.Code, recorder.Body.String())
	}
}

func TestSessionImageUnknownSessionReturns404(t *testing.T) {
	handler, _ := newProviderHandler(t)
	workstream := createTestWorkstream(t, handler)
	path := "/api/projects/proj-test/workstreams/" + workstream.ID + "/sessions/nope/image"
	if recorder := postImage(t, handler, path, "image/png", []byte("\x89PNG\r\n\x1a\n")); recorder.Code != http.StatusNotFound {
		t.Fatalf("image on unknown session = %d, want 404 (%s)", recorder.Code, recorder.Body.String())
	}
}
