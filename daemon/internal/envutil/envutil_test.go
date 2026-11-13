package envutil

import (
	"testing"
	"time"
)

func TestMilliseconds(t *testing.T) {
	tests := []struct {
		value string
		want  time.Duration
	}{
		{"250", 250 * time.Millisecond},
		{" 250\n", 250 * time.Millisecond},
		{"", 0},
		{"x", 0},
		{"0", 0},
		{"-5", 0},
	}
	for _, tt := range tests {
		t.Setenv("HOMESHELL_TEST_MS", tt.value)
		if got := Milliseconds("HOMESHELL_TEST_MS"); got != tt.want {
			t.Errorf("Milliseconds(%q) = %v, want %v", tt.value, got, tt.want)
		}
	}
}
