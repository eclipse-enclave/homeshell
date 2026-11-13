// Package envutil reads the environment knobs the test harness sets, which stay off the flag surface.
package envutil

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// Milliseconds reads the environment variable name as a duration in milliseconds. Anything missing, unparseable
// or non-positive is 0, which callers take as off or their default.
func Milliseconds(name string) time.Duration {
	milliseconds, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name)))
	if err != nil || milliseconds <= 0 {
		return 0
	}
	return time.Duration(milliseconds) * time.Millisecond
}
