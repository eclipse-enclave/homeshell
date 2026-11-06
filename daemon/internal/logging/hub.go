package logging

import (
	"sync"
	"time"
)

// Record is one log line published to subscribers: a monotonic sequence number
// for ordering and de-duplication, the emitting time and level, the message,
// and the record's attributes flattened into a map. It is what the log-stream
// endpoint marshals to JSON for the web log viewer.
type Record struct {
	Seq   uint64         `json:"seq"`
	Time  time.Time      `json:"time"`
	Level string         `json:"level"`
	Msg   string         `json:"msg"`
	Attrs map[string]any `json:"attrs,omitempty"`
}

// hubBuffer is how far a subscriber may fall behind before it is dropped, so a
// slow reader never blocks logging. A dropped subscriber's channel is closed;
// the client reconnects and resumes from live.
const hubBuffer = 256

// Hub fans emitted log records out to subscribers (the log-stream SSE handler).
// It mirrors the store's subscribe/broadcast design: bounded per-subscriber
// channels and a non-blocking send that drops a subscriber that cannot keep up.
// A Hub is safe for concurrent use and is written to from the logging handler.
type Hub struct {
	mu   sync.Mutex
	subs map[chan Record]struct{}
	seq  uint64
}

// NewHub returns an empty Hub ready for subscribers.
func NewHub() *Hub {
	return &Hub{subs: map[chan Record]struct{}{}}
}

// Subscribe registers for log records. The returned cancel function must be
// called when the subscriber goes away; the channel is closed either by cancel
// or by the Hub when the subscriber falls too far behind.
func (h *Hub) Subscribe() (<-chan Record, func()) {
	ch := make(chan Record, hubBuffer)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	cancel := func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if _, ok := h.subs[ch]; ok {
			delete(h.subs, ch)
			close(ch)
		}
	}
	return ch, cancel
}

// broadcast assigns the next sequence number and pushes the record to every
// subscriber. A subscriber whose buffer is full is dropped (channel closed)
// rather than blocked, so emitting a log never stalls on a slow client.
func (h *Hub) broadcast(rec Record) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seq++
	rec.Seq = h.seq
	for ch := range h.subs {
		select {
		case ch <- rec:
		default:
			delete(h.subs, ch)
			close(ch)
		}
	}
}
