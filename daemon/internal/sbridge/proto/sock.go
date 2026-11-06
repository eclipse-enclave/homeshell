package proto

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
)

// Listen binds a unix socket at path with 0600 permissions, working around
// the sun_path length limit by binding the basename from the socket's
// directory. The chdir fallback changes the process working directory —
// callers that later spawn children must set their working directory
// explicitly (the bridge always does, via the agent command's Dir).
func Listen(path string) (net.Listener, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	bindPath := abs
	if len(abs) > maxSunPath {
		if err := os.Chdir(filepath.Dir(abs)); err != nil {
			return nil, fmt.Errorf("socket path exceeds %d bytes and its directory is not enterable: %w", maxSunPath, err)
		}
		bindPath = filepath.Base(abs)
	}
	// The socket must be 0600 from the moment it exists: a bind-then-chmod
	// would leave a window where any local user could connect. The umask
	// swap is process-wide but momentary, and the bridge creates no other
	// files concurrently (the agent child has its own copy of the umask).
	old := syscall.Umask(0o177)
	ln, err := net.Listen("unix", bindPath)
	syscall.Umask(old)
	if err != nil {
		return nil, err
	}
	return ln, nil
}
