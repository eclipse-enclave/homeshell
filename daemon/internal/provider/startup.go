package provider

import (
	"strings"
	"sync"
	"time"
)

// StartupSink turns a provider's startup output into a coalesced tail.
//
// It sits between a provider writing build output line by line and the store,
// which broadcasts a full state snapshot on every mutation. The provider may
// call Line as fast as `docker build` produces text; the sink keeps only the
// last `keep` lines and publishes at most once per `interval`, so the user sees
// a live tail without the daemon fanning out a frame per line.
//
// Line is safe to call from any goroutine (Enclave calls it from the two
// goroutines draining stdout and stderr). Close stops the publisher and flushes
// whatever the last lines were, so the final state of a failed build is not lost
// to the timer.
type StartupSink struct {
	mu      sync.Mutex
	lines   []string
	dirty   bool
	closed  bool
	publish func(tail string)

	done chan struct{}
	wg   sync.WaitGroup
}

// NewStartupSink starts a sink with the tail length and pace a create's startup
// output gets, for a provider that shows startup output on a session of its own
// rather than handing it to the service — an Enclave restart, whose session the
// provider reports as starting while it comes back. Close it when the output ends.
func NewStartupSink(publish func(tail string)) *StartupSink {
	return newStartupSink(startupTailLines, startupFlushInterval, publish)
}

// newStartupSink starts a sink publishing through publish. keep is how many
// trailing lines to retain, interval the minimum gap between publishes.
func newStartupSink(keep int, interval time.Duration, publish func(tail string)) *StartupSink {
	s := &StartupSink{
		lines:   make([]string, 0, keep),
		publish: publish,
		done:    make(chan struct{}),
	}
	s.wg.Add(1)
	go s.run(interval)
	return s
}

// Line records one line of output. It never blocks on the publisher.
func (s *StartupSink) Line(line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	// Fixed-capacity ring: shift down once full, so a build of any length costs
	// the same memory.
	if len(s.lines) == cap(s.lines) {
		copy(s.lines, s.lines[1:])
		s.lines = s.lines[:len(s.lines)-1]
	}
	s.lines = append(s.lines, line)
	s.dirty = true
}

// Close stops publishing and flushes any pending tail.
func (s *StartupSink) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()

	close(s.done)
	s.wg.Wait()
	s.flush()
}

// run publishes on a ticker until Close.
func (s *StartupSink) run(interval time.Duration) {
	defer s.wg.Done()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			s.flush()
		}
	}
}

// flush publishes the current tail if it changed since the last publish.
func (s *StartupSink) flush() {
	s.mu.Lock()
	if !s.dirty {
		s.mu.Unlock()
		return
	}
	s.dirty = false
	tail := strings.Join(s.lines, "\n")
	s.mu.Unlock()
	// Published outside the lock: the store takes its own, and a slow subscriber
	// must not stall the provider's output goroutines.
	s.publish(tail)
}
