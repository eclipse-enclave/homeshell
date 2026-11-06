package proto

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

// maxSunPath is a conservative bound on unix socket path length (Linux
// allows 108 bytes including the NUL; BSDs 104).
const maxSunPath = 100

// Dial connects to a bridge socket within timeout.
func Dial(path string, timeout time.Duration) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return DialContext(ctx, path)
}

// DialContext connects to a bridge socket. A path past sun_path is dialed
// through a short symlink to its directory rather than a chdir, which would
// move the working directory of the whole daemon.
func DialContext(ctx context.Context, path string) (net.Conn, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if len(abs) <= maxSunPath {
		return (&net.Dialer{}).DialContext(ctx, "unix", abs)
	}
	// Not os.TempDir: macOS's TMPDIR is about 50 bytes on its own.
	dir, err := os.MkdirTemp("/tmp", "hs-dial-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }() // a connected socket no longer needs its path
	alias := filepath.Join(dir, "socket-dir")
	if err := os.Symlink(filepath.Dir(abs), alias); err != nil {
		return nil, err
	}
	short := filepath.Join(alias, filepath.Base(abs))
	if len(short) > maxSunPath {
		return nil, fmt.Errorf("socket name %q is too long to dial", filepath.Base(abs))
	}
	return (&net.Dialer{}).DialContext(ctx, "unix", short)
}
