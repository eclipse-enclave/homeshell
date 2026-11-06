package enclave

import "testing"

func TestExited(t *testing.T) {
	for status, want := range map[string]bool{
		"running": false, "exited": true, "dead": true, "removing": true,
		"created": false, "paused": false, "restarting": false, "": false,
	} {
		if got := exited(status); got != want {
			t.Errorf("exited(%q) = %v, want %v", status, got, want)
		}
	}
}
