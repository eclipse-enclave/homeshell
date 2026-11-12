package store

import (
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
)

// The workstream the demo simulator (the fake provider under `serve -demo`)
// cycles a session through so the UI shows live SSE updates.
const (
	DemoProjectID    = "proj-release-tools"
	DemoWorkstreamID = "ws-rel-notes"
	// DemoWorkstreamDir is the working directory of the demo workstream. The
	// fake provider's simulator reports its sessions against this directory so
	// the daemon matches them to the demo workstream (the public join key).
	DemoWorkstreamDir = "~/Git/release-tools/.ws/release-notes-1.4"
	// DemoProviderID is the fake provider's ID, which every seeded workstream
	// names so its card carries the provider's label.
	DemoProviderID = "fake"
)

// Seed returns the placeholder dataset served until real projects and
// providers exist (Milestones 3–6). Only session states and runtime states
// are authored here; workstream and project aggregate states are derived
// (Store.New recomputes them). The projects cover the full range of visual
// states — including runtime error and stopped — so every state of the UI
// is exercised; tests rely on that.
func Seed() []domain.Project {
	return []domain.Project{
		{
			ID:        "proj-homeshell",
			Name:      "homeshell",
			Path:      "~/Git/homeshell",
			GitBacked: true,
			Workstreams: []domain.Workstream{
				{
					ID:           "ws-hs-self-update",
					Name:         "feature/self-update",
					Provider:     DemoProviderID,
					StrategyID:   "worktree",
					Branch:       "feature/self-update",
					WorkingDir:   "~/Git/homeshell/.ws/self-update",
					RuntimeState: domain.RuntimeRunning,
					Sessions: []domain.Session{
						{ID: "sess-hs-implement", Title: "Implement self-update", Agent: "Claude Code", State: domain.SessionRunning},
						{ID: "sess-hs-verify", Title: "Verify update flow end-to-end", Agent: "Claude Code", State: domain.SessionWaiting},
					},
				},
				{
					ID:           "ws-hs-docs",
					Name:         "docs pass",
					Provider:     DemoProviderID,
					StrategyID:   "in-place",
					WorkingDir:   "~/Git/homeshell",
					RuntimeState: domain.RuntimeRunning,
					Sessions: []domain.Session{
						{ID: "sess-hs-docs-live", Title: "Update the API reference", Agent: "Claude Code", State: domain.SessionRunning},
						{ID: "sess-hs-docs-ask", Title: "Review glossary changes", Agent: "Claude Code", State: domain.SessionWaiting},
						{ID: "sess-hs-docs", Title: "Review architecture document", Agent: "Codex", State: domain.SessionDone, EndedAt: agoAt(12 * time.Minute)},
						{ID: "sess-hs-docs-lint", Title: "Fix docs lint", Agent: "Claude Code", State: domain.SessionStopped, EndedAt: agoAt(time.Hour)},
						{ID: "sess-hs-docs-links", Title: "Check broken links", Agent: "Claude Code", State: domain.SessionStopped, EndedAt: agoAt(26 * time.Hour)},
						{ID: "sess-hs-docs-toc", Title: "Rebuild the table of contents", Agent: "Claude Code", State: domain.SessionDone, EndedAt: agoAt(3 * 24 * time.Hour)},
						{ID: "sess-hs-docs-typos", Title: "Proofread the README", Agent: "Claude Code", State: domain.SessionDone, EndedAt: agoAt(9 * 24 * time.Hour)},
					},
				},
			},
		},
		{
			ID:        "proj-webshop",
			Name:      "acme-webshop",
			Path:      "~/Git/acme-webshop",
			GitBacked: true,
			Workstreams: []domain.Workstream{
				{
					ID:           "ws-shop-tax",
					Name:         "bugfix/checkout-tax",
					Provider:     DemoProviderID,
					StrategyID:   "copy",
					Branch:       "bugfix/checkout-tax",
					WorkingDir:   "~/Git/acme-webshop/.ws/checkout-tax",
					RuntimeState: domain.RuntimeRunning,
					Sessions: []domain.Session{
						{ID: "sess-shop-fix", Title: "Fix rounding in tax calculation", Agent: "Claude Code", State: domain.SessionRunning},
						{ID: "sess-shop-tests", Title: "Add regression tests", Agent: "Codex", State: domain.SessionIdle, OutdatedSettings: []string{"allow_domains", "features"}},
					},
				},
			},
		},
		{
			ID:        DemoProjectID,
			Name:      "release-tools",
			Path:      "~/Git/release-tools",
			GitBacked: true,
			Workstreams: []domain.Workstream{
				{
					ID:           DemoWorkstreamID,
					Name:         "release-notes-1.4",
					Provider:     DemoProviderID,
					StrategyID:   "worktree",
					Branch:       "release/1.4",
					WorkingDir:   DemoWorkstreamDir,
					RuntimeState: domain.RuntimeRunning,
					Sessions: []domain.Session{
						{ID: "sess-rel-draft", Title: "Draft 1.4 release notes", Agent: "Claude Code", State: domain.SessionDone},
					},
				},
				{
					ID:           "ws-rel-1.3",
					Name:         "release-notes-1.3",
					Provider:     DemoProviderID,
					StrategyID:   "worktree",
					Branch:       "release/1.3",
					WorkingDir:   "~/Git/release-tools/.ws/release-notes-1.3",
					RuntimeState: domain.RuntimeStopped,
					Sessions: []domain.Session{
						{ID: "sess-rel-1.3-draft", Title: "Draft 1.3 release notes", Agent: "Claude Code", State: domain.SessionDone},
					},
				},
			},
		},
		{
			ID:        "proj-infra",
			Name:      "infra-tools",
			Path:      "~/Git/infra-tools",
			GitBacked: true,
			Workstreams: []domain.Workstream{
				{
					ID:           "ws-infra-deploy",
					Name:         "deploy-pipeline",
					Provider:     DemoProviderID,
					StrategyID:   "worktree",
					Branch:       "main",
					WorkingDir:   "~/Git/infra-tools/.ws/deploy",
					RuntimeState: domain.RuntimeError,
					Sessions: []domain.Session{
						{ID: "sess-infra-fix", Title: "Fix deployment script", Agent: "Claude Code", State: domain.SessionRunning},
					},
				},
			},
		},
		{
			ID:          "proj-notes",
			Name:        "notes",
			Path:        "~/Documents/notes",
			GitBacked:   false,
			Workstreams: []domain.Workstream{},
		},
	}
}

// agoAt is the time elapsed before now, for the demo's ended sessions.
func agoAt(elapsed time.Duration) *time.Time {
	endedAt := time.Now().Add(-elapsed)
	return &endedAt
}
