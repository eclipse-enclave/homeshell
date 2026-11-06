package cli

// The `config` verb: the daemon's **live runtime configuration** — today the log
// level — as opposed to the durable preference document behind `settings`.
//
// The two are deliberately different endpoints: `/api/config` always reports
// what the daemon is actually doing right now, while settings.json holds the
// startup default. Changing the level here takes effect immediately for every
// client and is persisted as that default, so it survives a restart.
//
// It mirrors `settings`' shape — `get [key]` / `set <key> <value>` — so that
// runtime config joining this payload later needs no new verb.

import (
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
)

func init() {
	register(Command{
		Name:    "config",
		Summary: "Read and change the daemon's live runtime config (log level)",
		Run:     runConfig,
	})
}

func runConfig(env Env, args []string) int {
	return dispatchGroup(env, "config",
		"Read and change the daemon's live runtime configuration.",
		configSubcommands(), args)
}

func configSubcommands() []clientCommand {
	return []clientCommand{configGet(), configSet()}
}

const configPath = "/api/config"

func configGet() clientCommand {
	return clientCommand{
		Verb:    "config",
		Name:    "get",
		Args:    "[key]",
		Summary: "Print the live runtime config, or the value of one key",
		Run: func(call clientCall) error {
			if len(call.Args) > 1 {
				return surplusArgsError(call.Args[1:])
			}
			payload, err := requestJSON(call.Ctx, call.Client, http.MethodGet, configPath, nil)
			if err != nil {
				return err
			}
			var document map[string]any
			if err := decodePayload(payload, &document); err != nil {
				return err
			}
			if len(call.Args) == 0 {
				if call.Options.asJSON {
					writeRaw(call.Env.Stdout, payload)
					return nil
				}
				for _, key := range slices.Sorted(maps.Keys(document)) {
					fmt.Fprintf(call.Env.Stdout, "%s=%s\n", key, scalarText(document[key]))
				}
				return nil
			}
			key := call.Args[0]
			value, ok := document[key]
			if !ok {
				return &usageError{message: fmt.Sprintf(
					"no runtime config is named %q — known keys: %s",
					key, strings.Join(slices.Sorted(maps.Keys(document)), ", "))}
			}
			fmt.Fprintln(call.Env.Stdout, scalarText(value))
			return nil
		},
	}
}

func configSet() clientCommand {
	return clientCommand{
		Verb:    "config",
		Name:    "set",
		Args:    "<key> <value>",
		Summary: "Change one runtime config value, effective immediately",
		Run: func(call clientCall) error {
			key, value, err := call.two("a config key", "a value")
			if err != nil {
				return err
			}
			// PATCH takes only the key being changed, so the request is built from
			// what was asked for. Which keys are settable is the daemon's to say — an
			// unknown one is its 400, not a list kept here.
			payload, err := requestJSON(call.Ctx, call.Client, http.MethodPatch,
				configPath, map[string]string{key: value})
			if err != nil {
				return err
			}
			if call.Options.asJSON {
				writeRaw(call.Env.Stdout, payload)
				return nil
			}
			// The daemon answers with the config it now has, so report that rather
			// than echoing the request — the two differ if it normalized the value.
			var document map[string]any
			if err := decodePayload(payload, &document); err == nil {
				if applied, ok := document[key]; ok {
					fmt.Fprintf(call.Env.Stdout, "%s=%s\n", key, scalarText(applied))
					return nil
				}
			}
			fmt.Fprintf(call.Env.Stdout, "%s=%s\n", key, value)
			return nil
		},
	}
}
