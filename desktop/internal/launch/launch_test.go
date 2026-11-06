package launch

import (
	"errors"
	"slices"
	"testing"
)

type fakeRunner struct {
	out  []byte
	err  error
	args []string
}

func (f *fakeRunner) Run(args ...string) ([]byte, error) {
	f.args = args
	return f.out, f.err
}

func TestResolveURLParsesEnsureRunningOutput(t *testing.T) {
	r := &fakeRunner{out: []byte(`{"url":"http://127.0.0.1:8420/","spawned":true}`)}
	url, err := ResolveURL(r)
	if err != nil {
		t.Fatalf("ResolveURL: %v", err)
	}
	if url != "http://127.0.0.1:8420/" {
		t.Fatalf("url = %q, want http://127.0.0.1:8420/", url)
	}
	if len(r.args) != 2 || r.args[0] != "ensure-running" || r.args[1] != "--json" {
		t.Fatalf("Run args = %v, want [ensure-running --json]", r.args)
	}
}

func TestResolveURLErrorsWhenRunnerFails(t *testing.T) {
	if _, err := ResolveURL(&fakeRunner{err: errors.New("boom")}); err == nil {
		t.Fatal("expected an error when the runner fails")
	}
}

func TestResolveURLErrorsOnEmptyURL(t *testing.T) {
	if _, err := ResolveURL(&fakeRunner{out: []byte(`{"url":"","spawned":false}`)}); err == nil {
		t.Fatal("expected an error when ensure-running returns an empty URL")
	}
}

func TestResolveURLErrorsOnMalformedJSON(t *testing.T) {
	if _, err := ResolveURL(&fakeRunner{out: []byte("not json")}); err == nil {
		t.Fatal("expected an error when ensure-running returns malformed JSON")
	}
}

// A launcher passes no arguments, so the environment is the only way to point
// the shell at a non-default daemon; without this it always targets the platform
// data directory on the default port and collides with a repo-local daemon.
func TestEnsureArgsForwardsTheEnvironment(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want []string
	}{
		{
			name: "bare when nothing is set",
			env:  nil,
			want: []string{"ensure-running", "--json"},
		},
		{
			name: "forwards the data directory",
			env:  map[string]string{homeDirEnv: "/tmp/db/branch"},
			want: []string{"ensure-running", "--json", "-home-dir", "/tmp/db/branch"},
		},
		{
			name: "forwards the listen address",
			env:  map[string]string{addrEnv: "127.0.0.1:9000"},
			want: []string{"ensure-running", "--json", "-addr", "127.0.0.1:9000"},
		},
		{
			name: "forwards both",
			env:  map[string]string{homeDirEnv: "/tmp/db/branch", addrEnv: "127.0.0.1:9000"},
			want: []string{"ensure-running", "--json", "-home-dir", "/tmp/db/branch", "-addr", "127.0.0.1:9000"},
		},
		{
			name: "ignores empty values so an exported-but-blank var is no value",
			env:  map[string]string{homeDirEnv: "", addrEnv: ""},
			want: []string{"ensure-running", "--json"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ensureArgs(func(key string) string { return tt.env[key] })
			if !slices.Equal(got, tt.want) {
				t.Errorf("ensureArgs = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestResolveURLPassesTheEnvironmentThrough(t *testing.T) {
	t.Setenv(homeDirEnv, "/tmp/db/branch")
	runner := &fakeRunner{out: []byte(`{"url":"http://127.0.0.1:8420/","spawned":false}`)}
	if _, err := ResolveURL(runner); err != nil {
		t.Fatalf("ResolveURL: %v", err)
	}
	if !slices.Contains(runner.args, "-home-dir") || !slices.Contains(runner.args, "/tmp/db/branch") {
		t.Errorf("Run args = %v, want them to carry the -home-dir from the environment", runner.args)
	}
}
