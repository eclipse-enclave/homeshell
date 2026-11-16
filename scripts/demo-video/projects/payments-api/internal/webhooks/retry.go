// Package webhooks delivers events to the merchant's webhook endpoint.
package webhooks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
)

// MaxAttempts is how often a delivery is tried before it is given up.
const MaxAttempts = 5

type Deliverer struct {
	client   *http.Client
	Endpoint string
}

func NewDeliverer(client *http.Client) *Deliverer { return &Deliverer{client: client} }

// Notify posts the event and retries right away when the endpoint fails.
func (d *Deliverer) Notify(event string, payload any) error {
	body, err := json.Marshal(map[string]any{"event": event, "data": payload})
	if err != nil {
		return err
	}
	var last error
	for attempt := 1; attempt <= MaxAttempts; attempt++ {
		resp, err := d.client.Post(d.Endpoint, "application/json", bytes.NewReader(body))
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode < 300 {
				return nil
			}
			err = fmt.Errorf("endpoint answered %d", resp.StatusCode)
		}
		last = err
	}
	return fmt.Errorf("gave up after %d attempts: %w", MaxAttempts, last)
}
