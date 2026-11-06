package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/apiclient"
)

func init() {
	register(Command{
		Name:    "update",
		Summary: "Ask the running daemon to self-update to the latest release",
		Run:     runUpdate,
	})
}

// updateResponse mirrors the JSON returned by POST /api/update.
type updateResponse struct {
	Updated bool   `json:"updated"`
	Version string `json:"version"`
	Error   string `json:"error"`
}

// runUpdate implements the `homeshell update` verb. Rather than performing the
// update itself, it drives the *running* daemon through its public API (POST
// /api/update): the daemon owns all state and orchestration, so a second
// process must not swap the binary out from under it. On success the daemon
// verifies, swaps, and restarts itself; this command reports the outcome.
func runUpdate(env Env, _ []string) int {
	client, _, err := apiclient.Discover()
	if err != nil {
		fmt.Fprintf(env.Stderr, "homeshell: cannot discover the daemon: %v\n", err)
		fmt.Fprintln(env.Stderr, `Is the daemon running? Start it with "homeshell serve".`)
		return 1
	}

	// The download + swap + restart can take a while; give it generous room.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	base := client.BaseURL
	// Route through the client so the bearer token (populated by Discover from the
	// state file) is attached; Do applies no timeout of its own, so the update is
	// bounded by the generous ctx deadline above rather than a per-request one.
	resp, err := client.Do(ctx, http.MethodPost, "/api/update", nil)
	if err != nil {
		fmt.Fprintf(env.Stderr, "homeshell: cannot reach the daemon at %s: %v\n", base, err)
		fmt.Fprintln(env.Stderr, `Is the daemon running? Start it with "homeshell serve".`)
		return 1
	}
	defer resp.Body.Close()

	// The daemon restarts on a successful update, which can tear down the
	// connection before or during the response. Treat a mid-flight EOF on a
	// 200 as success rather than an error.
	var body updateResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		if resp.StatusCode == http.StatusOK {
			fmt.Fprintln(env.Stdout, "update triggered; the daemon is restarting")
			return 0
		}
		fmt.Fprintf(env.Stderr, "homeshell: update failed (status %d)\n", resp.StatusCode)
		return 1
	}

	if resp.StatusCode != http.StatusOK {
		if body.Error != "" {
			fmt.Fprintf(env.Stderr, "homeshell: update failed: %s\n", body.Error)
		} else {
			fmt.Fprintf(env.Stderr, "homeshell: update failed (status %d)\n", resp.StatusCode)
		}
		return 1
	}

	if body.Updated {
		fmt.Fprintf(env.Stdout, "updating to %s; the daemon is restarting\n", body.Version)
	} else {
		fmt.Fprintf(env.Stdout, "already up to date (%s)\n", body.Version)
	}
	return 0
}
