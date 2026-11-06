package provider

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// The sink exists to bound how often the store is written, so the first thing to
// pin is that a burst of lines does not become a burst of publishes.
func TestStartupSinkCoalescesABurst(t *testing.T) {
	var (
		mu       sync.Mutex
		publish  []string
		interval = 40 * time.Millisecond
	)
	sink := newStartupSink(10, interval, func(tail string) {
		mu.Lock()
		publish = append(publish, tail)
		mu.Unlock()
	})

	for i := 0; i < 500; i++ {
		sink.Line("line")
	}
	sink.Close()

	mu.Lock()
	defer mu.Unlock()
	if len(publish) == 0 {
		t.Fatal("500 lines produced no publish at all")
	}
	// The exact count depends on scheduling; what matters is that it is nothing
	// like one per line.
	if len(publish) > 10 {
		t.Errorf("500 lines produced %d publishes, want a coalesced handful", len(publish))
	}
}

// The tail is the last `keep` lines, whatever the volume before them.
func TestStartupSinkKeepsOnlyTheTail(t *testing.T) {
	var (
		mu   sync.Mutex
		last string
	)
	sink := newStartupSink(3, time.Millisecond, func(tail string) {
		mu.Lock()
		last = tail
		mu.Unlock()
	})
	for _, line := range []string{"one", "two", "three", "four", "five"} {
		sink.Line(line)
	}
	sink.Close()

	mu.Lock()
	defer mu.Unlock()
	if last != "three\nfour\nfive" {
		t.Errorf("tail = %q, want the last three lines", last)
	}
}

// Close must flush: a build that fails immediately would otherwise lose the
// output explaining why, because the ticker never came round.
func TestStartupSinkFlushesOnClose(t *testing.T) {
	var (
		mu   sync.Mutex
		last string
	)
	// An interval long enough that the ticker cannot be what publishes.
	sink := newStartupSink(10, time.Hour, func(tail string) {
		mu.Lock()
		last = tail
		mu.Unlock()
	})
	sink.Line("the error that matters")
	sink.Close()

	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(last, "the error that matters") {
		t.Errorf("tail after Close = %q, want the pending line", last)
	}
}

// Nothing written means nothing published — an empty preview must not overwrite
// whatever the session already had.
func TestStartupSinkPublishesNothingWhenIdle(t *testing.T) {
	var calls int
	sink := newStartupSink(10, time.Millisecond, func(string) { calls++ })
	time.Sleep(10 * time.Millisecond)
	sink.Close()

	if calls != 0 {
		t.Errorf("publishes = %d, want 0 for a sink that saw no lines", calls)
	}
}

// Enclave feeds the sink from two goroutines (stdout and stderr), and Close
// races with them at the end of a build; -race is what makes this meaningful.
func TestStartupSinkIsConcurrencySafe(t *testing.T) {
	sink := newStartupSink(5, time.Millisecond, func(string) {})

	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				sink.Line("concurrent")
			}
		}()
	}
	wg.Wait()
	sink.Close()
	// Close is idempotent: finishCreateSession may unwind more than one way.
	sink.Close()
	// A line after Close is dropped rather than panicking on a closed sink.
	sink.Line("late")
}
