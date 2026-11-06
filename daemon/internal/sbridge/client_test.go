package sbridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/sbridge/sbridgetest"
)

func TestDumpWireContract(t *testing.T) {
	for _, tt := range []struct {
		name, payload string
		valid         bool
	}{
		{"signals", `{"screen":"output\n","osc_title":"title","osc_progress":"4;0"}`, true},
		{"capture failure is not a dead bridge", `{"error":"emulator failed","osc_progress":null}`, true},
		{"empty screen", `{"screen":"","osc_title":"","osc_progress":null}`, true},
		{"empty response", "", false},
		{"null", "null", false},
		{"truncated", `{"screen":`, false},
		{"refused protocol", "err unsupported handshake\n", false},
		{"trailing data", `{}garbage`, false},
		{"oversized", strings.Repeat("x", maxSnapshotBytes+1), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			sock := filepath.Join(sbridgetest.TempDir(t), "bridge.sock")
			count := sbridgetest.Serve(t, sock, func() string { return tt.payload })
			snap, err := Dump(context.Background(), sock)
			if (err == nil) != tt.valid || errors.Is(err, ErrNotServing) {
				t.Fatalf("snapshot=%+v error=%v", snap, err)
			}
			if count.Load() != 1 {
				t.Fatalf("reads=%d", count.Load())
			}
			if tt.name == "signals" && (snap.Screen != "output\n" || snap.OSCTitle != "title" || snap.OSCProgress == nil || *snap.OSCProgress != "4;0") {
				t.Fatalf("signals=%+v", snap)
			}
			if tt.name == "capture failure is not a dead bridge" && snap.Error != "emulator failed" {
				t.Fatal(snap)
			}
		})
	}
}

func TestDumpNotServing(t *testing.T) {
	sock := filepath.Join(sbridgetest.TempDir(t), "missing.sock")
	if _, err := Dump(context.Background(), sock); !errors.Is(err, ErrNotServing) {
		t.Fatal(err)
	}
	// A closed listener whose socket file remains returns ECONNREFUSED.
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	ln.SetUnlinkOnClose(false)
	_ = ln.Close()
	if _, err := Dump(context.Background(), sock); !errors.Is(err, ErrNotServing) {
		t.Fatal(err)
	}
	_ = os.Remove(sock)
}

func TestDumpLongSocketPathDoesNotChangeWorkingDirectory(t *testing.T) {
	base := sbridgetest.TempDir(t)
	deep := filepath.Join(base, strings.Repeat("nested-", 25))
	if err := os.MkdirAll(deep, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "short")
	if err := os.Symlink(deep, alias); err != nil {
		t.Fatal(err)
	}
	count := sbridgetest.Serve(t, filepath.Join(alias, "bridge.sock"), func() string { return `{"screen":"long path"}` })
	before, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	snap, err := Dump(context.Background(), filepath.Join(deep, "bridge.sock"))
	if err != nil || snap.Screen != "long path" || count.Load() != 1 {
		t.Fatalf("snapshot=%+v error=%v", snap, err)
	}
	if after, err := os.Getwd(); err != nil || after != before {
		t.Fatalf("cwd changed to %q (%v)", after, err)
	}
}

// macOS's TMPDIR is about 50 bytes; an alias under it would be no shorter.
func TestDumpLongSocketPathWithLongTempDir(t *testing.T) {
	base := sbridgetest.TempDir(t)
	deep := filepath.Join(base, strings.Repeat("nested-", 25))
	if err := os.MkdirAll(deep, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "short")
	if err := os.Symlink(deep, alias); err != nil {
		t.Fatal(err)
	}
	count := sbridgetest.Serve(t, filepath.Join(alias, "bridge.sock"), func() string { return `{"screen":"long path"}` })
	longTemp := filepath.Join(base, strings.Repeat("t", 80))
	if err := os.MkdirAll(longTemp, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", longTemp)
	snap, err := Dump(context.Background(), filepath.Join(deep, "bridge.sock"))
	if err != nil || snap.Screen != "long path" || count.Load() != 1 {
		t.Fatalf("snapshot=%+v error=%v", snap, err)
	}
}

func TestDumpCancellationAfterConnection(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "deadline"}[deadline], func(t *testing.T) {
			sock := filepath.Join(sbridgetest.TempDir(t), "wedged.sock")
			connected := make(chan struct{})
			sbridgetest.Listen(t, sock, func(conn net.Conn) { close(connected); _, _ = io.Copy(io.Discard, conn) })
			ctx, cancel := context.WithCancel(context.Background())
			want := context.Canceled
			if deadline {
				ctx, cancel = context.WithTimeout(context.Background(), 40*time.Millisecond)
				want = context.DeadlineExceeded
			}
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := Dump(ctx, sock); done <- err }()
			<-connected
			if !deadline {
				cancel()
			}
			select {
			case err := <-done:
				if !errors.Is(err, want) || errors.Is(err, ErrNotServing) {
					t.Fatalf("error=%v want %v", err, want)
				}
			case <-time.After(time.Second):
				t.Fatal("exchange ignored cancellation")
			}
		})
	}
}

func TestExitSummaryQuotesTheLastScreenLines(t *testing.T) {
	rec := Exit{Code: 2, Screen: "a\nb\n\nc\nd\n e \nf\n\n\n"}
	if got, want := rec.Summary(), "status 2: b / c / d / e / f"; got != want {
		t.Errorf("Summary() = %q, want %q", got, want)
	}
}

func TestReadExit(t *testing.T) {
	dir := t.TempDir()
	if _, err := ReadExit(filepath.Join(dir, "none.json")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing record: %v, want fs.ErrNotExist", err)
	}
	path := filepath.Join(dir, "exit.json")
	if err := os.WriteFile(path, []byte(`{"code":3,"screen":"bye"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if rec, err := ReadExit(path); err != nil || rec != (Exit{Code: 3, Screen: "bye"}) {
		t.Errorf("ReadExit = %+v, %v", rec, err)
	}
}

func TestExitQuitOnItsOwn(t *testing.T) {
	for _, tc := range []struct {
		record string
		want   bool
	}{
		{`{"code":0,"ended_by_bridge":false}`, true},
		{`{"code":143,"ended_by_bridge":true}`, false},
		{`{"code":0}`, false},                           // an older bridge's record does not say
		{`{"code":137,"ended_by_bridge":false}`, false}, // killed, e.g. by the OOM killer
	} {
		path := filepath.Join(t.TempDir(), "exit.json")
		if err := os.WriteFile(path, []byte(tc.record), 0o600); err != nil {
			t.Fatal(err)
		}
		rec, err := ReadExit(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := rec.QuitOnItsOwn(); got != tc.want {
			t.Errorf("%s: QuitOnItsOwn() = %v, want %v", tc.record, got, tc.want)
		}
	}
}

func TestSendTypesTheInputAndWaitsForTheAck(t *testing.T) {
	sock := filepath.Join(sbridgetest.TempDir(t), "bridge.sock")
	bridge := sbridgetest.Bridge(t, sock, func() string { return `{}` })
	if err := Send(context.Background(), sock, []byte("hello\r")); err != nil {
		t.Fatal(err)
	}
	if got := bridge.Requests(); len(got) != 1 || got[0].Input != "hello\r" {
		t.Errorf("requests = %+v, want one send of the input", got)
	}
}

func TestSendToADeadBridgeIsNotServing(t *testing.T) {
	sock := filepath.Join(sbridgetest.TempDir(t), "missing.sock")
	if err := Send(context.Background(), sock, []byte("x")); !errors.Is(err, ErrNotServing) {
		t.Fatalf("Send = %v, want ErrNotServing", err)
	}
}

func TestSendSurfacesTheBridgesError(t *testing.T) {
	sock := filepath.Join(sbridgetest.TempDir(t), "bridge.sock")
	sbridgetest.Listen(t, sock, func(conn net.Conn) { _, _ = io.WriteString(conn, "err send failed\n") })
	if err := Send(context.Background(), sock, []byte("x")); err == nil || !strings.Contains(err.Error(), "send failed") || errors.Is(err, ErrNotServing) {
		t.Fatalf("Send = %v, want the bridge's error", err)
	}
}

// The agent's grace is what is left of the deadline once the client has time
// to see the bridge exit.
func TestStopLeavesTheAgentTheDeadlineAsGrace(t *testing.T) {
	tests := []struct {
		name      string
		deadline  time.Duration // zero: no caller deadline
		wantGrace int           // ms
	}{
		{"short deadline", 5 * time.Second, 3000},
		{"deadline beyond StopTimeout", 30 * time.Second, 28000},
		{"no deadline", 0, int((StopTimeout - stopReserve).Milliseconds())},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sock := filepath.Join(sbridgetest.TempDir(t), "bridge.sock")
			bridge := sbridgetest.Bridge(t, sock, func() string { return `{}` })
			ctx := context.Background()
			if tt.deadline > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tt.deadline)
				defer cancel()
			}
			if err := Stop(ctx, sock); err != nil {
				t.Fatal(err)
			}
			got := bridge.Requests()
			if len(got) != 1 {
				t.Fatalf("requests = %+v, want one stop", got)
			}
			var grace int
			if _, err := fmt.Sscanf(got[0].Line, "sbridge-1 stop %d", &grace); err != nil || grace < tt.wantGrace-500 || grace > tt.wantGrace {
				t.Errorf("stop line %q, want a grace of about %d ms", got[0].Line, tt.wantGrace)
			}
		})
	}
}

func TestStopOnADeadBridgeIsDone(t *testing.T) {
	if err := Stop(context.Background(), filepath.Join(sbridgetest.TempDir(t), "missing.sock")); err != nil {
		t.Fatalf("Stop = %v, want nil for a bridge that is not serving", err)
	}
}
