package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/persist"
)

func TestPreviewAndStateProjectionDoesNotRewriteStateFile(t *testing.T) {
	for _, providerID := range []string{"enclave", "local"} {
		t.Run(providerID, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.json")
			s := New([]domain.Project{{ID: "p", Name: "project", Path: "/tmp/p"}}, persist.NewFileSaver(path))
			projection := func(title, preview string, state domain.SessionState) []domain.Workstream {
				detail := json.RawMessage(fmt.Sprintf(`{"matchedRule":%q,"confident":true,"visibleBlocker":%v}`, state, state == domain.SessionWaiting))
				return []domain.Workstream{{
					ID: "ws", Name: "workstream", WorkingDir: "/tmp/p",
					Sessions: []domain.Session{{
						ID: "sess", Title: title, Provider: providerID, Agent: "codex",
						Preview: preview, State: state,
						ProviderData:    json.RawMessage(`{"owner":"stable"}`),
						ObservationData: detail,
					}},
				}}
			}
			if err := s.ReplaceWorkstreams("p", projection("session", "first", domain.SessionRunning)); err != nil {
				t.Fatal(err)
			}
			written, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, state := range []domain.SessionState{domain.SessionRunning, domain.SessionIdle, domain.SessionWaiting} {
				if err := s.ReplaceWorkstreams("p", projection("session", string(state)+" preview", state)); err != nil {
					t.Fatal(err)
				}
			}
			// Saves replace the file by rename, so an unchanged inode means no write.
			after, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if !os.SameFile(written, after) {
				t.Fatal("polling-only fields rewrote the state file")
			}
			if got := s.Projects()[0].Workstreams[0].Sessions[0]; got.State != domain.SessionWaiting || got.Preview != "waiting preview" {
				t.Fatalf("live projection not updated: %+v", got)
			}
			if err := s.ReplaceWorkstreams("p", projection("new title", "latest", domain.SessionIdle)); err != nil {
				t.Fatal(err)
			}
			restored, err := persist.LoadState(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := restored.Projects[0].Workstreams[0].Sessions; len(got) != 1 || got[0].Title != "new title" {
				t.Fatalf("restored sessions=%+v", got)
			}
		})
	}
}

func TestOnlyDurableFieldsRewriteStateFile(t *testing.T) {
	base := func() []domain.Workstream {
		return []domain.Workstream{{
			ID: "ws", Name: "workstream", Provider: "local", StrategyID: "worktree", Branch: "main", WorkingDir: "/tmp/p",
			ProviderData: json.RawMessage(`{"p":1}`), CreatorData: json.RawMessage(`{"c":1}`),
			Sessions: []domain.Session{{
				ID: "sess", Title: "session", Agent: "codex", Provider: "local", ProviderData: json.RawMessage(`{"s":1}`),
				State: domain.SessionRunning, Preview: "preview", ObservationData: json.RawMessage(`{"o":1}`),
			}},
		}}
	}
	for _, tc := range []struct {
		name    string
		change  func(ws *domain.Workstream, sess *domain.Session)
		rewrite bool
	}{
		{"session title", func(_ *domain.Workstream, s *domain.Session) { s.Title = "renamed" }, true},
		{"session agent", func(_ *domain.Workstream, s *domain.Session) { s.Agent = "claude" }, true},
		{"session provider", func(_ *domain.Workstream, s *domain.Session) { s.Provider = "enclave" }, true},
		{"session provider data", func(_ *domain.Workstream, s *domain.Session) { s.ProviderData = json.RawMessage(`{"s":2}`) }, true},
		{"workstream name", func(w *domain.Workstream, _ *domain.Session) { w.Name = "renamed" }, true},
		{"workstream provider", func(w *domain.Workstream, _ *domain.Session) { w.Provider = "enclave" }, true},
		{"workstream strategy", func(w *domain.Workstream, _ *domain.Session) { w.StrategyID = "in-place" }, true},
		{"workstream branch", func(w *domain.Workstream, _ *domain.Session) { w.Branch = "feature" }, true},
		{"workstream working dir", func(w *domain.Workstream, _ *domain.Session) { w.WorkingDir = "/tmp/q" }, true},
		{"workstream provider data", func(w *domain.Workstream, _ *domain.Session) { w.ProviderData = json.RawMessage(`{"p":2}`) }, true},
		{"workstream creator data", func(w *domain.Workstream, _ *domain.Session) { w.CreatorData = json.RawMessage(`{"c":2}`) }, true},
		{"session state", func(_ *domain.Workstream, s *domain.Session) { s.State = domain.SessionWaiting }, false},
		{"session preview", func(_ *domain.Workstream, s *domain.Session) { s.Preview = "later" }, false},
		{"session error", func(_ *domain.Workstream, s *domain.Session) { s.Error = "failed" }, false},
		{"session observation data", func(_ *domain.Workstream, s *domain.Session) { s.ObservationData = json.RawMessage(`{"o":2}`) }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.json")
			s := New([]domain.Project{{ID: "p", Name: "project", Path: "/tmp/p"}}, persist.NewFileSaver(path))
			if err := s.ReplaceWorkstreams("p", base()); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			next := base()
			tc.change(&next[0], &next[0].Sessions[0])
			if err := s.ReplaceWorkstreams("p", next); err != nil {
				t.Fatal(err)
			}
			after, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if rewrote := !os.SameFile(before, after); rewrote != tc.rewrite {
				t.Fatalf("rewrote=%v, want %v", rewrote, tc.rewrite)
			}
		})
	}
}
