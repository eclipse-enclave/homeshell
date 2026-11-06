package cli

import (
	"bytes"
	"strings"
	"testing"
)

const testVersion = "9.9.9"

func runDispatch(args []string) (code int, stdout, stderr string) {
	var out, err bytes.Buffer
	code = Dispatch(Env{Version: testVersion, Stdout: &out, Stderr: &err}, args)
	return code, out.String(), err.String()
}

func TestDispatch(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout []string // substrings the standard output must contain
		wantStderr []string // substrings the standard error must contain
	}{
		{
			name:       "no args prints usage on stdout",
			args:       nil,
			wantCode:   0,
			wantStdout: []string{"Usage", "serve"},
		},
		{
			name:       "help verb prints usage on stdout",
			args:       []string{"help"},
			wantCode:   0,
			wantStdout: []string{"Usage", "serve"},
		},
		{
			name:       "-h prints usage on stdout",
			args:       []string{"-h"},
			wantCode:   0,
			wantStdout: []string{"Usage"},
		},
		{
			name:       "--help prints usage on stdout",
			args:       []string{"--help"},
			wantCode:   0,
			wantStdout: []string{"Usage"},
		},
		{
			name:       "-version prints the version on stdout",
			args:       []string{"-version"},
			wantCode:   0,
			wantStdout: []string{testVersion},
		},
		{
			name:       "--version prints the version on stdout",
			args:       []string{"--version"},
			wantCode:   0,
			wantStdout: []string{testVersion},
		},
		{
			name:       "unknown command errors on stderr",
			args:       []string{"bogus"},
			wantCode:   2,
			wantStderr: []string{"unknown command", "bogus", "Usage"},
		},
		{
			name:       "serve -h prints usage and succeeds",
			args:       []string{"serve", "-h"},
			wantCode:   0,
			wantStderr: []string{"-addr", "-demo"},
		},
		{
			name:       "serve with an unknown flag fails",
			args:       []string{"serve", "-nope"},
			wantCode:   2,
			wantStderr: []string{"nope"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, stdout, stderr := runDispatch(tt.args)
			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d (stderr: %q)", code, tt.wantCode, stderr)
			}
			for _, want := range tt.wantStdout {
				if !strings.Contains(stdout, want) {
					t.Errorf("stdout = %q, want it to contain %q", stdout, want)
				}
			}
			for _, want := range tt.wantStderr {
				if !strings.Contains(stderr, want) {
					t.Errorf("stderr = %q, want it to contain %q", stderr, want)
				}
			}
		})
	}
}

func TestUsageListsRegisteredVerbs(t *testing.T) {
	var buf bytes.Buffer
	writeUsage(&buf)
	usage := buf.String()
	for _, verb := range []string{"serve", "update"} {
		if !strings.Contains(usage, verb) {
			t.Errorf("usage does not list the %q verb:\n%s", verb, usage)
		}
	}
}

// TestUsageAlignsSummaries pins the verb column to the widest registered verb.
// A fixed width regressed silently as soon as a longer verb was added
// (`ensure-running`, `workstream`), leaving their summaries jammed against the
// name while the short verbs stayed aligned — so assert the property rather than
// a hard-coded column.
func TestUsageAlignsSummaries(t *testing.T) {
	var buf bytes.Buffer
	writeUsage(&buf)

	column := -1
	seen := 0
	for _, line := range strings.Split(buf.String(), "\n") {
		// A verb line is "  <name><padding><summary>". Take the name as the first
		// field and locate the summary by walking past it and its padding — not by
		// splitting on a two-space run, which is itself the thing under test: a
		// too-narrow column leaves a single space and would make this skip exactly
		// the lines that regressed.
		if !strings.HasPrefix(line, "  ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := fields[0]
		if _, isVerb := registry[name]; !isVerb {
			continue
		}
		rest := line[len("  ")+len(name):]
		start := len("  ") + len(name) + (len(rest) - len(strings.TrimLeft(rest, " ")))
		seen++
		if column == -1 {
			column = start
			continue
		}
		if start != column {
			t.Errorf("summary for %q starts at column %d, want %d:\n%s", name, start, column, buf.String())
		}
	}
	if seen < 2 {
		t.Fatalf("expected several verb lines to compare, saw %d:\n%s", seen, buf.String())
	}
	if column == -1 {
		t.Fatalf("no verb lines found in usage:\n%s", buf.String())
	}
}

// The bridge verb is run by the daemon and by terminals it opens, not typed by
// users, so the usage text leaves it out while Dispatch still routes it.
func TestBridgeVerbIsHiddenButDispatched(t *testing.T) {
	var out, errb bytes.Buffer
	env := Env{Stdout: &out, Stderr: &errb}
	Dispatch(env, nil)
	if strings.Contains(out.String(), "bridge") {
		t.Errorf("usage lists the hidden bridge verb:\n%s", out.String())
	}
	if code := Dispatch(env, []string{"bridge", "help"}); code != 0 {
		t.Errorf("bridge help exit %d, want 0; stderr %q", code, errb.String())
	}
}
