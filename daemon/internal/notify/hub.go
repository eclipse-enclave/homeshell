package notify

import (
	"context"
	"sync"
)

// hubBuffer is how many notifications a subscriber may fall behind before
// further ones are dropped. Notifications are ephemeral events (unlike the
// full-state stream), so a slow subscriber loses individual messages rather
// than being disconnected.
const hubBuffer = 16

// Hub fans notifications out to the connected SSE clients. It is the browser
// delivery channel: the events endpoint subscribes to it and relays each
// notification as an SSE "notification" event.
type Hub struct {
	mu   sync.Mutex
	subs map[chan Notification]struct{}
}

// NewHub creates an empty Hub.
func NewHub() *Hub {
	return &Hub{subs: map[chan Notification]struct{}{}}
}

// Subscribe registers for notifications. The returned cancel function removes
// and closes the channel and must be called when the subscriber goes away.
func (h *Hub) Subscribe() (<-chan Notification, func()) {
	subscriber := make(chan Notification, hubBuffer)
	h.mu.Lock()
	h.subs[subscriber] = struct{}{}
	h.mu.Unlock()
	cancel := func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if _, ok := h.subs[subscriber]; ok {
			delete(h.subs, subscriber)
			close(subscriber)
		}
	}
	return subscriber, cancel
}

// Publish delivers the notification to every subscriber, non-blocking. A
// subscriber whose buffer is full simply misses this notification (it is not
// disconnected): missing an ephemeral event is harmless, unlike falling behind
// on state.
func (h *Hub) Publish(notification Notification) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for subscriber := range h.subs {
		select {
		case subscriber <- notification:
		default:
		}
	}
}

// Deliver implements Delivery by publishing to subscribers.
func (h *Hub) Deliver(_ context.Context, notification Notification) {
	h.Publish(notification)
}
