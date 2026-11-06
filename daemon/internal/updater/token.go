package updater

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"
)

// tokenEnvVars are the environment variables consulted for a GitHub token, in
// priority order. HOMESHELL_UPDATE_TOKEN lets operators scope a token to the
// updater without exposing it to everything that reads GITHUB_TOKEN.
var tokenEnvVars = []string{"HOMESHELL_UPDATE_TOKEN", "GITHUB_TOKEN", "GH_TOKEN"}

// ghTokenTimeout bounds the `gh auth token` lookup so a hung CLI can't block
// daemon startup (resolveToken runs synchronously during `serve`).
const ghTokenTimeout = 5 * time.Second

// lookupEnv is indirected so tests can control the environment.
var lookupEnv = os.LookupEnv

// ghToken runs `gh auth token` and returns its output. Indirected for tests.
var ghToken = func(ctx context.Context) (string, bool) {
	path, err := exec.LookPath("gh")
	if err != nil {
		return "", false
	}
	out, err := exec.CommandContext(ctx, path, "auth", "token").Output()
	if err != nil {
		return "", false
	}
	token := strings.TrimSpace(string(out))
	return token, token != ""
}

// resolveToken finds a GitHub API token for downloading private release
// assets. It prefers the GitHub CLI (`gh auth token`) when available so users
// who already logged in with `gh` need no extra setup, and falls back to the
// environment otherwise. An empty return means "no token"; the updater then
// operates unauthenticated, which is fine for public releases.
func resolveToken(ctx context.Context) string {
	ghCtx, cancel := context.WithTimeout(ctx, ghTokenTimeout)
	defer cancel()
	if token, ok := ghToken(ghCtx); ok {
		return token
	}
	for _, key := range tokenEnvVars {
		if token, ok := lookupEnv(key); ok {
			if token = strings.TrimSpace(token); token != "" {
				return token
			}
		}
	}
	return ""
}
