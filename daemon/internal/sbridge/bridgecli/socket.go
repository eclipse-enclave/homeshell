package bridgecli

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/sbridge/proto"
	"github.com/eclipsesource/homeshell/daemon/internal/sbridge/session"
)

// socketCheckInterval is how often serve checks that its socket still exists.
// A caller polling the socket reads a missing one as a dead session.
const socketCheckInterval = time.Second

// errSocketInUse means another live bridge already serves the socket path.
var errSocketInUse = errors.New("socket is served by another running bridge")

// socketKeeper owns serve's socket. It recreates the socket when something
// deletes it (a cleaned runtime directory), and never unlinks a socket that is
// not its own, which net.UnixListener's default unlink-on-close would do by path.
type socketKeeper struct {
	path string

	mu  sync.Mutex
	ln  net.Listener
	id  fileID
	srv *session.Server
}

// fileID identifies the socket file this bridge bound.
type fileID struct{ dev, ino uint64 }

// listenSocket binds path, replacing a stale socket but refusing a live one.
func listenSocket(path string) (*socketKeeper, error) {
	if conn, err := proto.Dial(path, time.Second); err == nil {
		_ = conn.Close()
		return nil, errSocketInUse
	}
	_ = os.Remove(path) // stale socket from a bridge that died uncleanly
	k := &socketKeeper{path: path}
	if err := k.bind(); err != nil {
		return nil, err
	}
	return k, nil
}

func (k *socketKeeper) bind() error {
	ln, err := proto.Listen(k.path)
	if err != nil {
		return err
	}
	if ul, ok := ln.(*net.UnixListener); ok {
		ul.SetUnlinkOnClose(false)
	}
	id, err := statID(k.path)
	if err != nil {
		_ = ln.Close()
		return err
	}
	k.ln, k.id = ln, id
	return nil
}

// serve answers connections with srv and keeps the socket in place until done
// is closed.
func (k *socketKeeper) serve(srv *session.Server, done <-chan struct{}) {
	k.mu.Lock()
	k.srv = srv
	srv.Serve(k.ln)
	k.mu.Unlock()
	go func() {
		ticker := time.NewTicker(socketCheckInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				k.restore()
			case <-done:
				return
			}
		}
	}()
}

// restore rebinds the socket when it is gone. A socket another bridge bound at
// the path meanwhile is left alone.
func (k *socketKeeper) restore() {
	if _, err := os.Lstat(k.path); !errors.Is(err, os.ErrNotExist) {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	old := k.ln
	if err := os.MkdirAll(filepath.Dir(k.path), 0o700); err != nil {
		return
	}
	if err := k.bind(); err != nil {
		fmt.Fprintf(os.Stderr, "sbridge: serve: recreating removed socket: %v\n", err)
		return
	}
	_ = old.Close()
	k.srv.Serve(k.ln)
}

// close stops listening and removes the socket if it is still this bridge's.
func (k *socketKeeper) close() {
	k.mu.Lock()
	defer k.mu.Unlock()
	_ = k.ln.Close()
	if id, err := statID(k.path); err == nil && id == k.id {
		_ = os.Remove(k.path)
	}
}

func statID(path string) (fileID, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return fileID{}, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fileID{}, fmt.Errorf("no file identity for %s", path)
	}
	return fileID{dev: uint64(st.Dev), ino: st.Ino}, nil // #nosec G115 -- Dev is a non-negative device number.
}
