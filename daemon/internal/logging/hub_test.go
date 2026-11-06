package logging

import (
	"testing"
	"time"
)

func recv(t *testing.T, ch <-chan Record) Record {
	t.Helper()
	select {
	case rec, ok := <-ch:
		if !ok {
			t.Fatal("channel closed, want a record")
		}
		return rec
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for a record")
		return Record{}
	}
}

func TestHubBroadcastFansOutAndNumbers(t *testing.T) {
	hub := NewHub()
	a, cancelA := hub.Subscribe()
	defer cancelA()
	b, cancelB := hub.Subscribe()
	defer cancelB()

	hub.broadcast(Record{Msg: "first"})
	hub.broadcast(Record{Msg: "second"})

	for _, ch := range []<-chan Record{a, b} {
		if r := recv(t, ch); r.Msg != "first" || r.Seq != 1 {
			t.Errorf("first record = %+v, want msg=first seq=1", r)
		}
		if r := recv(t, ch); r.Msg != "second" || r.Seq != 2 {
			t.Errorf("second record = %+v, want msg=second seq=2", r)
		}
	}
}

func TestHubCancelUnsubscribes(t *testing.T) {
	hub := NewHub()
	ch, cancel := hub.Subscribe()
	cancel()

	if _, ok := <-ch; ok {
		t.Fatal("channel should be closed after cancel")
	}
	// A broadcast after cancel must not panic on the closed channel.
	hub.broadcast(Record{Msg: "after"})
	if got := len(subsSnapshot(hub)); got != 0 {
		t.Errorf("subscriber count = %d, want 0", got)
	}
}

func TestHubDropsSlowSubscriber(t *testing.T) {
	hub := NewHub()
	ch, cancel := hub.Subscribe()
	defer cancel()

	// Never read; fill the buffer and then some so the subscriber is dropped.
	for i := 0; i < hubBuffer+1; i++ {
		hub.broadcast(Record{Msg: "flood"})
	}
	if got := len(subsSnapshot(hub)); got != 0 {
		t.Fatalf("subscriber count = %d, want 0 (dropped)", got)
	}
	// Drain the buffered records; the channel must then be closed.
	for range ch {
	}
}

func subsSnapshot(h *Hub) map[chan Record]struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make(map[chan Record]struct{}, len(h.subs))
	for k, v := range h.subs {
		out[k] = v
	}
	return out
}
