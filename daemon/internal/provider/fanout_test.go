package provider

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestFanoutBoundsConcurrencyAndRunsEveryIndex(t *testing.T) {
	var running, peak atomic.Int32
	seen := make([]atomic.Bool, 20)
	Fanout(len(seen), 3, func(i int) {
		now := running.Add(1)
		for {
			old := peak.Load()
			if now <= old || peak.CompareAndSwap(old, now) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		seen[i].Store(true)
		running.Add(-1)
	})
	if peak.Load() > 3 || peak.Load() < 2 {
		t.Fatalf("peak concurrency=%d; want 2..3", peak.Load())
	}
	for i := range seen {
		if !seen[i].Load() {
			t.Fatalf("index %d not run", i)
		}
	}
}
