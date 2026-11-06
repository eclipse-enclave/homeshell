//go:build dev

package enclave

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/agentspec"
	"github.com/eclipsesource/homeshell/daemon/internal/provider"
)

type blockingAgentSource struct {
	agentspec.Source
	entered, release chan struct{}
	once             sync.Once
	calls            atomic.Int32
}

func (s *blockingAgentSource) Agents() []agentspec.Agent {
	s.calls.Add(1)
	s.once.Do(func() { close(s.entered); <-s.release })
	return s.Source.Agents()
}

func TestWatchReadsBindingsOnceOutsideMetadataLock(t *testing.T) {
	p, cli, _, project := bridgeProvider(t)
	first := createBridge(t, p, project)
	cli.runResult = "enclave-codex-0123456789ab-hs-second"
	createBridge(t, p, project)
	source := &blockingAgentSource{Source: p.specs, entered: make(chan struct{}), release: make(chan struct{})}
	p.SetAgentSpecs(source)
	finished := make(chan struct{})
	poll := p.changeDetector()
	go func() { poll(context.Background()); close(finished) }()
	<-source.entered
	controls := make(chan error, 1)
	go func() {
		ref := provider.SessionRef{ProjectID: project.ID, SessionID: first.ID}
		if err := p.Prompt(context.Background(), ref, "still responsive"); err != nil {
			controls <- err
			return
		}
		if err := p.SendKey(context.Background(), ref, "up"); err != nil {
			controls <- err
			return
		}
		_, err := p.DiscoverWatchedSessions(context.Background(), project)
		controls <- err
	}()
	select {
	case err := <-controls:
		if err != nil {
			close(source.release)
			<-finished
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		close(source.release)
		<-finished
		<-controls
		t.Fatal("agent catalog read blocked session controls")
	}
	close(source.release)
	<-finished
	if source.calls.Load() != 1 {
		t.Fatalf("bindings queried %d times for a two-session pass", source.calls.Load())
	}
}
