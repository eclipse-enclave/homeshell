package cli

import (
	"strings"
	"testing"
)

func TestStatusRunning(t *testing.T) {
	isolateStateFile(t)
	liveDaemon(t)

	code, stdout, _ := runVerb(runStatus, nil)
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if !strings.HasPrefix(stdout, "running") {
		t.Errorf("stdout = %q, want it to start with %q", stdout, "running")
	}
	for _, want := range []string{"1.2.3", "pid", "daemon", statePath()} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want it to contain %q", stdout, want)
		}
	}
}

func TestStatusNotRunning(t *testing.T) {
	isolateStateFile(t)

	code, stdout, _ := runVerb(runStatus, nil)
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stdout, "not running") {
		t.Errorf("stdout = %q, want it to report not running", stdout)
	}
}

func TestStatusStale(t *testing.T) {
	isolateStateFile(t)
	writeStaleState(t)

	code, stdout, _ := runVerb(runStatus, nil)
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stdout, "stale") {
		t.Errorf("stdout = %q, want it to mention a stale state file", stdout)
	}
}
