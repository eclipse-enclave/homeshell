package provider_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/provider"
)

// perSessionLinkProvider resolves a link for some of its sessions and not
// others — the shape a workstream really has once a create has failed or one is
// still building beside a live session.
type perSessionLinkProvider struct {
	containerProvider
	urls map[string]string
}

func (p *perSessionLinkProvider) Links(domain.Session) []provider.Link {
	return []provider.Link{{ID: "web-ide", Label: "Web IDE"}}
}

func (p *perSessionLinkProvider) LinkURL(_ context.Context, ref provider.SessionRef, _ string) (string, error) {
	if url, ok := p.urls[ref.SessionID]; ok {
		return url, nil
	}
	return "", fmt.Errorf("session %q not found", ref.SessionID)
}

// A placeholder is not offered to the provider at all: its ID is the daemon's
// own, never issued by anyone, so asking can only produce a misleading "session
// not found" from a provider that is working perfectly well.
func TestOpenWorkstreamLinkIgnoresAStartingPlaceholder(t *testing.T) {
	web := &perSessionLinkProvider{
		containerProvider: containerProvider{id: "enclave"},
		urls:              map[string]string{"sess-live": "http://127.0.0.1:3000"},
	}
	svc, _ := newOpenService(t, workstreamAt(t.TempDir(),
		session(provider.StartingIDPrefix+"abc", "enclave", domain.SessionStarting),
		session("sess-live", "enclave", domain.SessionRunning),
	), web)

	assertLinkURL(t, svc, "http://127.0.0.1:3000")
}

func assertLinkURL(t *testing.T, svc *provider.Service, want string) {
	t.Helper()
	res, err := svc.OpenWorkstream(context.Background(),
		provider.WorkstreamRef{ProjectID: "p1", WorkstreamID: "ws-1"}, "link:web-ide")
	if err != nil {
		t.Fatalf("OpenWorkstream: %v", err)
	}
	var data struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(res.Data, &data); err != nil {
		t.Fatalf("unmarshal result data: %v", err)
	}
	if data.URL != want {
		t.Errorf("url = %q; want the live session's link %q", data.URL, want)
	}
}
