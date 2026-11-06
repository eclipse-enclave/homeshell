package notify

import (
	"testing"
)

func TestHubFansOutToSubscribers(t *testing.T) {
	hub := NewHub()
	subA, cancelA := hub.Subscribe()
	subB, cancelB := hub.Subscribe()
	defer cancelA()
	defer cancelB()

	hub.Publish(Notification{ID: "n1", Kind: KindWaiting})

	for _, subscriber := range []<-chan Notification{subA, subB} {
		select {
		case got := <-subscriber:
			if got.ID != "n1" {
				t.Errorf("got %q, want n1", got.ID)
			}
		default:
			t.Error("subscriber did not receive the notification")
		}
	}
}

func TestHubCancelStopsDelivery(t *testing.T) {
	hub := NewHub()
	subscriber, cancel := hub.Subscribe()
	cancel()

	// The channel is closed by cancel; a receive returns the zero value with
	// ok=false rather than a published notification.
	if _, ok := <-subscriber; ok {
		t.Error("expected the cancelled subscriber channel to be closed")
	}
	// Publishing after cancel must not panic (no send on a closed channel).
	hub.Publish(Notification{ID: "n2"})
}

func TestHubDropsWhenFullWithoutClosing(t *testing.T) {
	hub := NewHub()
	subscriber, cancel := hub.Subscribe()
	defer cancel()

	// Overflow the buffer; excess publishes are dropped, not fatal, and the
	// subscription stays open (unlike the state store, which disconnects slow
	// clients).
	for i := 0; i < hubBuffer+5; i++ {
		hub.Publish(Notification{ID: "flood"})
	}

	drained := 0
drain:
	for {
		select {
		case _, ok := <-subscriber:
			if !ok {
				t.Fatal("subscriber channel was closed on overflow; it should stay open")
			}
			drained++
		default:
			break drain
		}
	}
	if drained != hubBuffer {
		t.Errorf("drained %d, want the buffered %d (rest dropped)", drained, hubBuffer)
	}
}
