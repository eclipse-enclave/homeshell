package cli

// The `version` verb: what this binary is, and what the daemon it can reach is.
// The two can differ — a self-update swaps the binary while an older daemon is
// still serving, and a `--addr` may name a daemon on another machine entirely — so
// reporting both is the point.
//
// Unlike the other data verbs this succeeds with no daemon running: the client
// version is a fact about this binary, and refusing to print it because nothing is
// listening would make `homeshell version` useless in exactly the situation where
// you reach for it.

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
)

func init() {
	register(Command{
		Name:    "version",
		Summary: "Print this binary's version and the running daemon's",
		Run:     runVersion,
	})
}

// daemonVersion mirrors the payload of GET /api/version.
type daemonVersion struct {
	Current         string `json:"current"`
	Latest          string `json:"latest"`
	UpdateAvailable bool   `json:"updateAvailable"`
	Error           string `json:"error"`
}

func runVersion(env Env, args []string) int {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	fs.Usage = func() {
		fmt.Fprint(env.Stderr, "Usage: homeshell version [flags]\n\n"+
			"Print this binary's version, and the running daemon's when one can be reached.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	var options clientOptions
	options.register(fs)
	positional, err := parseInterspersed(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if len(positional) > 0 {
		return reportError(env, fs, surplusArgsError(positional))
	}

	// A daemon that cannot be reached is reported, not an error: only a daemon that
	// answers *badly* fails the command.
	payload, err := fetchDaemonVersion(options)
	var down *unreachableError
	if err != nil && !errors.As(err, &down) {
		return reportError(env, fs, err)
	}

	if options.asJSON {
		writeVersionJSON(env, payload)
		return exitOK
	}
	writeVersionText(env, payload, err)
	return exitOK
}

// fetchDaemonVersion asks the discovered daemon for its version. An unreachable
// daemon comes back as an *unreachableError with a nil payload, which the caller
// reports rather than treats as a failure.
func fetchDaemonVersion(options clientOptions) ([]byte, error) {
	client, err := options.connect()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), options.timeout)
	defer cancel()
	return requestJSON(ctx, client, http.MethodGet, "/api/version", nil)
}

func writeVersionJSON(env Env, payload []byte) {
	// The daemon's own payload goes through untouched, under a key beside the
	// client version, which the API does not know about.
	daemon := json.RawMessage("null")
	if len(payload) > 0 {
		daemon = json.RawMessage(payload)
	}
	encoded, err := json.Marshal(struct {
		Client string          `json:"client"`
		Daemon json.RawMessage `json:"daemon"`
	}{Client: env.Version, Daemon: daemon})
	if err != nil {
		fmt.Fprintf(env.Stderr, "homeshell: %v\n", err)
		return
	}
	writeRaw(env.Stdout, encoded)
}

func writeVersionText(env Env, payload []byte, unreachable error) {
	fmt.Fprintf(env.Stdout, "client  %s\n", env.Version)
	if unreachable != nil {
		fmt.Fprintf(env.Stdout, "daemon  not running (%v)\n", unreachable)
		return
	}
	var reported daemonVersion
	if err := json.Unmarshal(payload, &reported); err != nil {
		fmt.Fprintln(env.Stdout, "daemon  running (its version could not be read)")
		return
	}
	fmt.Fprintf(env.Stdout, "daemon  %s\n", reported.Current)
	switch {
	case reported.Error != "":
		// The release check is best-effort in the daemon too; say why the latest
		// version is unknown rather than printing the current one as if it were.
		fmt.Fprintf(env.Stdout, "latest  unknown (%s)\n", reported.Error)
	case reported.UpdateAvailable:
		fmt.Fprintf(env.Stdout, "latest  %s — run \"homeshell update\"\n", reported.Latest)
	default:
		fmt.Fprintf(env.Stdout, "latest  %s (up to date)\n", reported.Latest)
	}
}
