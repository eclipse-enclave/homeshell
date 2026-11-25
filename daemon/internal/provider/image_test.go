package provider_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/provider"
)

var (
	pngBytes  = []byte("\x89PNG\r\n\x1a\nrest-of-png")
	jpegBytes = []byte("\xff\xd8\xffrest-of-jpeg")
)

func TestValidateImage(t *testing.T) {
	tests := []struct {
		name      string
		mediaType string
		data      []byte
		wantErr   bool
	}{
		{name: "png", mediaType: "image/png", data: pngBytes},
		{name: "jpeg", mediaType: "image/jpeg", data: jpegBytes},
		{name: "png bytes labelled jpeg", mediaType: "image/jpeg", data: pngBytes, wantErr: true},
		{name: "jpeg bytes labelled png", mediaType: "image/png", data: jpegBytes, wantErr: true},
		{name: "gif", mediaType: "image/gif", data: []byte("GIF89a..."), wantErr: true},
		{name: "no media type", mediaType: "", data: pngBytes, wantErr: true},
		{name: "empty png", mediaType: "image/png", data: nil, wantErr: true},
		{name: "truncated png signature", mediaType: "image/png", data: pngBytes[:4], wantErr: true},
		{name: "text labelled png", mediaType: "image/png", data: []byte("hello"), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := provider.ValidateImage(provider.Image{MediaType: tt.mediaType, Data: tt.data})
			if tt.wantErr && !errors.Is(err, provider.ErrInvalidImage) {
				t.Fatalf("ValidateImage = %v, want ErrInvalidImage", err)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("ValidateImage = %v, want nil", err)
			}
		})
	}
}

func TestCheckAttachable(t *testing.T) {
	attachable := &domain.SessionCapabilities{Live: true, AttachImage: true}
	tests := []struct {
		name    string
		session domain.Session
		wantErr error
	}{
		{name: "idle", session: domain.Session{State: domain.SessionIdle, Capabilities: attachable}},
		{name: "running", session: domain.Session{State: domain.SessionRunning, Capabilities: attachable}},
		{name: "no capabilities", session: domain.Session{State: domain.SessionIdle},
			wantErr: provider.ErrCapabilityUnsupported},
		{name: "capabilities without attachImage",
			session: domain.Session{State: domain.SessionIdle, Capabilities: &domain.SessionCapabilities{Live: true}},
			wantErr: provider.ErrCapabilityUnsupported},
		{name: "stopped", session: domain.Session{State: domain.SessionStopped, Capabilities: attachable},
			wantErr: provider.ErrSessionStopped},
		{name: "waiting", session: domain.Session{State: domain.SessionWaiting, Capabilities: attachable},
			wantErr: provider.ErrSessionNotAttachable},
		{name: "starting", session: domain.Session{State: domain.SessionStarting, Capabilities: attachable},
			wantErr: provider.ErrSessionNotAttachable},
		{name: "unknown", session: domain.Session{State: domain.SessionUnknown, Capabilities: attachable},
			wantErr: provider.ErrSessionNotAttachable},
		{name: "error", session: domain.Session{State: domain.SessionError, Capabilities: attachable},
			wantErr: provider.ErrSessionNotAttachable},
		{name: "starting placeholder",
			session: domain.Session{ID: provider.StartingIDPrefix + "x", State: domain.SessionStarting},
			wantErr: provider.ErrSessionStarting},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := provider.CheckAttachable(tt.session)
			if tt.wantErr == nil && err != nil {
				t.Fatalf("CheckAttachable = %v, want nil", err)
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("CheckAttachable = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

// TestServiceAttachImage drives the fake (an ImageAttacher) through the
// service: a valid image comes back as a path under the image inbox, and the
// session's preview shows it.
func TestServiceAttachImage(t *testing.T) {
	service, projectStore := newService(t)
	workstreamRef := createWS(t, service, projectStore)
	service.CreateSession(context.Background(), provider.CreateSessionInput{
		ProjectID: workstreamRef.ProjectID, WorkstreamID: workstreamRef.WorkstreamID, ProviderID: "fake", OptionID: "claude",
	})
	session := awaitSession(t, projectStore, "p1", workstreamRef.WorkstreamID, "Claude Code")
	sessionRef := provider.SessionRef{ProjectID: workstreamRef.ProjectID, WorkstreamID: workstreamRef.WorkstreamID, SessionID: session.ID}

	path, err := service.AttachImage(context.Background(), sessionRef, provider.Image{MediaType: "image/png", Data: pngBytes})
	if err != nil {
		t.Fatalf("AttachImage = %v, want nil", err)
	}
	if !strings.HasPrefix(path, "/mnt/host-images/") || !strings.HasSuffix(path, ".png") {
		t.Fatalf("path = %q, want a .png under /mnt/host-images/", path)
	}

	// An invalid image is refused before the session is consulted.
	if _, err := service.AttachImage(context.Background(), sessionRef, provider.Image{MediaType: "image/png", Data: jpegBytes}); !errors.Is(err, provider.ErrInvalidImage) {
		t.Fatalf("AttachImage(mismatch) = %v, want ErrInvalidImage", err)
	}

	if err := service.StopSession(context.Background(), sessionRef); err != nil {
		t.Fatalf("StopSession: %v", err)
	}
	if _, err := service.AttachImage(context.Background(), sessionRef, provider.Image{MediaType: "image/png", Data: pngBytes}); !errors.Is(err, provider.ErrSessionStopped) {
		t.Fatalf("AttachImage on stopped = %v, want ErrSessionStopped", err)
	}
}
