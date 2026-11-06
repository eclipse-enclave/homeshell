//go:build dev

package enclave

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/provider"
	"github.com/eclipsesource/homeshell/daemon/internal/sbridge"
)

func recoveryClock(p *Provider) func(time.Duration) {
	now := time.Unix(100, 0)
	p.now = func() time.Time { return now }
	return func(d time.Duration) { now = now.Add(d) }
}

func TestRecoveryFailuresBackOffAndHealthySocketsResetRetries(t *testing.T) {
	for _, failure := range []string{"Docker unavailable", "paused", "attach fails", "bridge dies after attach", "exit cleanup fails"} {
		t.Run(failure, func(t *testing.T) {
			p, cli, runtime, project := bridgeProvider(t)
			created := createBridge(t, p, project)
			advance := recoveryClock(p)
			runtime.live = false
			cli.all = map[string][]Container{project.Path: {{Name: created.ID, Status: "running"}}}
			switch failure {
			case "Docker unavailable":
				cli.allErr = errors.New("Docker unavailable")
			case "paused":
				cli.all[project.Path][0].Status = "paused"
			case "attach fails":
				runtime.startErr = errors.New("cannot attach")
			case "bridge dies after attach":
				runtime.err = sbridge.ErrNotServing
			case "exit cleanup fails":
				pastStartup(p, created.ID)
				cli.all[project.Path][0].Status = "exited"
				cli.stopErr = errors.New("cannot stop")
			}
			delays := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second}
			for attempt, delay := range delays {
				if got := discoverBridge(t, p, project); got.State != domain.SessionUnknown || got.Error != "" || bridgeReadError(got) == "" {
					t.Fatalf("uncertain recovery=%+v", got)
				}
				if cli.allCalls != attempt+1 {
					t.Fatalf("attempts=%d want %d", cli.allCalls, attempt+1)
				}
				g := p.bridges.guardFor(created.ID)
				if g.retryDelay != delay {
					t.Fatalf("retry delay=%s want %s", g.retryDelay, delay)
				}
				starts, stops := len(runtime.starts), len(cli.stopped)
				advance(500 * time.Millisecond)
				for range 10 {
					discoverBridge(t, p, project)
				}
				if cli.allCalls != attempt+1 || len(runtime.starts) != starts || len(cli.stopped) != stops {
					t.Fatal("cooldown spawned inventory, attach or cleanup")
				}
				if attempt < len(delays)-1 {
					advance(delay - 500*time.Millisecond)
				}
			}
			// A bridge can become reachable independently of our recovery process.
			// Its next cheap probe must be observed, not hidden by a long cooldown.
			runtime.err, runtime.live = nil, true
			if got := discoverBridge(t, p, project); got.State != domain.SessionIdle {
				t.Fatalf("healthy bridge remained unknown: %+v", got)
			}
			g := p.bridges.guardFor(created.ID)
			if g.retryDelay != 0 {
				t.Fatal("healthy observation did not reset backoff")
			}
			runtime.live = false
			cli.allErr = errors.New("Docker unavailable again")
			discoverBridge(t, p, project)
			if g.retryDelay != 2*time.Second {
				t.Fatalf("fresh loss inherited previous backoff: %s", g.retryDelay)
			}
			cli.stopErr = nil
			if err := p.StopSession(context.Background(), provider.SessionRef{SessionID: created.ID}); err != nil {
				t.Fatal(err)
			}
			if g.retryDelay != 0 || !g.retryAt.IsZero() {
				t.Fatal("explicit lifecycle change retained recovery cooldown")
			}
		})
	}
}
