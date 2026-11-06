package updater

import (
	"context"
	"testing"
	"time"
)

func TestResolveTokenPrefersGH(t *testing.T) {
	restore(t)
	ghToken = func(context.Context) (string, bool) { return "gh-cli-token", true }
	lookupEnv = func(string) (string, bool) { return "env-token", true }

	if got := resolveToken(context.Background()); got != "gh-cli-token" {
		t.Errorf("resolveToken = %q, want gh-cli-token", got)
	}
}

func TestResolveTokenFallsBackToEnv(t *testing.T) {
	restore(t)
	ghToken = func(context.Context) (string, bool) { return "", false }
	env := map[string]string{"GITHUB_TOKEN": "env-token"}
	lookupEnv = func(k string) (string, bool) { v, ok := env[k]; return v, ok }

	if got := resolveToken(context.Background()); got != "env-token" {
		t.Errorf("resolveToken = %q, want env-token", got)
	}
}

func TestResolveTokenEnvPriority(t *testing.T) {
	restore(t)
	ghToken = func(context.Context) (string, bool) { return "", false }
	env := map[string]string{
		"HOMESHELL_UPDATE_TOKEN": "scoped",
		"GITHUB_TOKEN":           "generic",
	}
	lookupEnv = func(k string) (string, bool) { v, ok := env[k]; return v, ok }

	if got := resolveToken(context.Background()); got != "scoped" {
		t.Errorf("resolveToken = %q, want scoped (HOMESHELL_UPDATE_TOKEN wins)", got)
	}
}

func TestResolveTokenNoneAvailable(t *testing.T) {
	restore(t)
	ghToken = func(context.Context) (string, bool) { return "", false }
	lookupEnv = func(string) (string, bool) { return "", false }

	if got := resolveToken(context.Background()); got != "" {
		t.Errorf("resolveToken = %q, want empty", got)
	}
}

func TestResolveTokenIgnoresBlankEnv(t *testing.T) {
	restore(t)
	ghToken = func(context.Context) (string, bool) { return "", false }
	env := map[string]string{"GITHUB_TOKEN": "   "}
	lookupEnv = func(k string) (string, bool) { v, ok := env[k]; return v, ok }

	if got := resolveToken(context.Background()); got != "" {
		t.Errorf("resolveToken = %q, want empty for blank token", got)
	}
}

func TestResolveTokenBoundsGHLookup(t *testing.T) {
	restore(t)
	// A gh lookup that outlives ghTokenTimeout must be cancelled so startup is
	// not blocked; resolveToken then falls back to the environment.
	ghToken = func(ctx context.Context) (string, bool) {
		select {
		case <-ctx.Done():
			return "", false
		case <-time.After(time.Minute):
			return "should-not-happen", true
		}
	}
	lookupEnv = func(k string) (string, bool) {
		if k == "GITHUB_TOKEN" {
			return "env-token", true
		}
		return "", false
	}

	done := make(chan string, 1)
	go func() { done <- resolveToken(context.Background()) }()

	select {
	case got := <-done:
		if got != "env-token" {
			t.Errorf("resolveToken = %q, want env-token", got)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("resolveToken did not return; gh lookup was not bounded")
	}
}

// restore snapshots the indirected token hooks and restores them after the test.
func restore(t *testing.T) {
	t.Helper()
	origGH := ghToken
	origEnv := lookupEnv
	t.Cleanup(func() {
		ghToken = origGH
		lookupEnv = origEnv
	})
}
