// Package launch resolves the daemon URL the desktop shell opens. All of the
// attach-or-spawn logic lives in the `homeshell` CLI (ensure-running); this
// package only invokes it and parses the result, so the shell stays a thin,
// replaceable window over the daemon-served UI.
package launch

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Runner executes the homeshell CLI and returns its stdout. It is an interface
// so tests can supply a fake instead of forking a real binary.
type Runner interface {
	Run(args ...string) ([]byte, error)
}

// The environment is how the shell is told which daemon to talk to: a desktop
// launcher and a double-clicked .app pass no arguments, so there is nowhere to
// put a flag. Without these, the shell always targets the platform default data
// directory on the default port, which collides with a repo-local daemon (`make
// run-local`) instead of attaching to it.
const (
	homeDirEnv = "HOMESHELL_HOME_DIR"
	addrEnv    = "HOMESHELL_ADDR"
)

// ensureArgs builds the ensure-running invocation, forwarding the data directory
// and listen address when the environment names them.
func ensureArgs(lookupEnv func(string) string) []string {
	args := []string{"ensure-running", "--json"}
	if homeDir := lookupEnv(homeDirEnv); homeDir != "" {
		args = append(args, "-home-dir", homeDir)
	}
	if addr := lookupEnv(addrEnv); addr != "" {
		args = append(args, "-addr", addr)
	}
	return args
}

// ensureResult mirrors the JSON printed by `homeshell ensure-running --json`.
type ensureResult struct {
	URL     string `json:"url"`
	Spawned bool   `json:"spawned"`
}

// ResolveURL asks the CLI to ensure a daemon is running and returns the loopback
// URL the webview should load.
func ResolveURL(runner Runner) (string, error) {
	out, err := runner.Run(ensureArgs(os.Getenv)...)
	if err != nil {
		return "", fmt.Errorf("ensuring daemon is running: %w", err)
	}
	var res ensureResult
	if err := json.Unmarshal(out, &res); err != nil {
		return "", fmt.Errorf("parsing ensure-running output: %w", err)
	}
	if res.URL == "" {
		return "", fmt.Errorf("ensure-running returned an empty URL")
	}
	return res.URL, nil
}

// ExecRunner runs the homeshell binary colocated with the desktop binary (how
// the installers ship them), falling back to $PATH.
type ExecRunner struct{}

// Run execs `homeshell <args...>` and returns its stdout.
func (ExecRunner) Run(args ...string) ([]byte, error) {
	bin, err := homeshellBinary()
	if err != nil {
		return nil, err
	}
	out, err := exec.Command(bin, args...).Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			return nil, fmt.Errorf("%w: %s", err, string(exitErr.Stderr))
		}
		return nil, err
	}
	return out, nil
}

// homeshellBinary prefers a homeshell binary sitting next to this executable
// (installer layout), then falls back to $PATH.
func homeshellBinary() (string, error) {
	if exe, err := os.Executable(); err == nil {
		sibling := filepath.Join(filepath.Dir(exe), "homeshell")
		if _, statErr := os.Stat(sibling); statErr == nil {
			return sibling, nil
		}
	}
	return exec.LookPath("homeshell")
}
