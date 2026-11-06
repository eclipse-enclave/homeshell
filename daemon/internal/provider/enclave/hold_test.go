package enclave

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/detect"
	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/sbridge"
)

// Enclave publishes a screen like a local session: held idle, the agent's
// title and the detection evidence. Only an unmatched screen differs, keeping
// the live container's runtime state (#133).
func TestBridgeObservationMatchesLocalPublication(t *testing.T) {
	p, _, bridge, project := bridgeProvider(t)
	created := createBridge(t, p, project)
	now := time.Unix(100, 0)
	p.now = func() time.Time { return now }

	bridge.snap = sbridge.Snapshot{Screen: "unclassified output", OSCTitle: "✳ Fix the parser"}
	p.bridges.mu.Lock()
	r := p.bridges.records[created.ID]
	r.Container.Tool = "unclassified"
	p.bridges.records[created.ID] = r
	p.bridges.mu.Unlock()
	got := discoverBridge(t, p, project)
	if got.State != domain.SessionRunning || got.Title != "Fix the parser" || got.Preview != "unclassified output" {
		t.Fatalf("unclassified observation=%+v", got)
	}
	var detail detect.Detail
	if err := json.Unmarshal(got.ObservationData, &detail); err != nil || detail.Confident {
		t.Fatalf("detail=%s err=%v; want unconfident", got.ObservationData, err)
	}

	p.bridges.mu.Lock()
	g := p.bridges.guard(created.ID)
	g.hold = detect.Hold{State: domain.SessionRunning, RuleSet: "unclassified"}
	p.bridges.mu.Unlock()
	r.Container.Tool = "codex"
	p.bridges.mu.Lock()
	p.bridges.records[created.ID] = r
	p.bridges.mu.Unlock()
	bridge.snap = sbridge.Snapshot{Screen: "Ready", OSCTitle: "✳ Codex"}
	if got := discoverBridge(t, p, project); got.State != domain.SessionIdle {
		t.Fatalf("idle under a new rule set=%+v; want idle without a hold", got)
	}

	bridge.snap.OSCTitle = "⠋ Codex"
	if got := discoverBridge(t, p, project); got.State != domain.SessionRunning {
		t.Fatalf("working title=%+v", got)
	}
	// Codex's idle title is visible idle evidence, which needs no hold.
	bridge.snap.OSCTitle = "✳ Codex"
	if got := discoverBridge(t, p, project); got.State != domain.SessionIdle {
		t.Fatalf("visible idle title=%+v; want idle at once", got)
	}
}
