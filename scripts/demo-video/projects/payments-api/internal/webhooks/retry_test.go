package webhooks

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNotifyRetriesUntilTheEndpointAnswers(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(http.StatusBadGateway)
		}
	}))
	defer srv.Close()
	d := NewDeliverer(srv.Client())
	d.Endpoint = srv.URL
	if err := d.Notify("refund.created", map[string]int{"cents": 100}); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
}
