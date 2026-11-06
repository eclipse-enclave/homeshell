package provider

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

type countingSink struct{ n atomic.Int64 }

func (s *countingSink) Changed()     { s.n.Add(1) }
func (s *countingSink) count() int64 { return s.n.Load() }

type noopSink struct{}

func (noopSink) Changed() {}

func waitUntil(t *testing.T, cond func() bool, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("condition not met within %s", within)
}

func TestWatchLoopPollsAtIntervalAndNotifiesOnChange(t *testing.T) {
	var polls atomic.Int64
	sink := &countingSink{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go WatchLoop(ctx, sink, func() time.Duration { return 10 * time.Millisecond }, time.Hour, func(context.Context) bool {
		polls.Add(1)
		return true // always "changed"
	})
	// Several polls happen at the configured interval, each notifying the sink.
	waitUntil(t, func() bool { return polls.Load() >= 3 && sink.count() >= 3 }, time.Second)
}

func TestWatchLoopFallsBackWhenIntervalUnset(t *testing.T) {
	var polls atomic.Int64
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// A nil interval uses the fallback period.
	go WatchLoop(ctx, noopSink{}, nil, 10*time.Millisecond, func(context.Context) bool {
		polls.Add(1)
		return false
	})
	waitUntil(t, func() bool { return polls.Load() >= 3 }, time.Second)
}

func TestWatchLoopReReadsIntervalEachCycle(t *testing.T) {
	var d atomic.Int64
	d.Store(int64(10 * time.Millisecond))
	var polls atomic.Int64
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go WatchLoop(ctx, noopSink{}, func() time.Duration { return time.Duration(d.Load()) }, time.Hour, func(context.Context) bool {
		polls.Add(1)
		return false
	})
	waitUntil(t, func() bool { return polls.Load() >= 2 }, time.Second) // polling fast

	// Grow the interval; the loop re-reads it before the next wait and stops.
	d.Store(int64(time.Hour))
	time.Sleep(50 * time.Millisecond) // let the in-flight short wait finish
	settled := polls.Load()
	time.Sleep(100 * time.Millisecond)
	if polls.Load() != settled {
		t.Fatalf("kept polling after the interval grew (%d then %d): did not re-read", settled, polls.Load())
	}
}
