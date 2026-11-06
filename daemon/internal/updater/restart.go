package updater

import (
	"fmt"
	"log/slog"
	"syscall"
)

// restartFunc replaces the current process with the new binary. It is a
// package variable so tests can substitute a no-op and assert the
// orchestration without actually exec-ing.
var restartFunc = execReplace

// execReplace uses syscall.Exec to replace the current process image with the
// updated binary. Same PID, same process group, same parent — the OS simply
// loads the new executable. This is the most reliable restart strategy: no
// orphaned children, no race with process group signals from make/shell.
//
// The caller must shut down the TCP listener before calling this so the new
// binary can bind the same port.
//
// On success this function does not return (the process image is replaced).
func execReplace(exePath string, args []string, env []string) error {
	// syscall.Exec expects argv[0] to be the program name.
	argv := append([]string{exePath}, args...)
	slog.Info("self-update: exec into new binary", "exe", exePath, "args", args)
	err := syscall.Exec(exePath, argv, env)
	// syscall.Exec only returns on error.
	return fmt.Errorf("exec %s: %w", exePath, err)
}
