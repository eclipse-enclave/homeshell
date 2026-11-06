package cli

// The `provider` verb: what session providers the daemon has registered, whether
// each is available, and what it can do.
//
// Availability and capabilities are the provider's own answers, collected by the
// daemon (a provider reports itself unavailable when its backend is not
// installed, with the reason). This prints them; it never probes anything itself
// (capability gating is read from data, never branched on a provider name).

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
)

func init() {
	register(Command{
		Name:    "provider",
		Summary: "List the session providers and what they support",
		Run:     runProvider,
	})
}

func runProvider(env Env, args []string) int {
	return dispatchGroup(env, "provider", "Inspect the providers the daemon has registered.",
		providerSubcommands(), args)
}

func providerSubcommands() []clientCommand {
	return []clientCommand{providerList()}
}

// providerDescriptor mirrors one entry of GET /api/providers. Capabilities are
// decoded loosely — as a map of the flags the daemon derived — so a capability
// added to the daemon shows up here without a change to this CLI.
type providerDescriptor struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Status struct {
		State  string `json:"state"`
		Reason string `json:"reason"`
	} `json:"status"`
	Capabilities map[string]json.RawMessage `json:"capabilities"`
}

func providerList() clientCommand {
	return clientCommand{
		Verb:    "provider",
		Name:    "list",
		Summary: "List the session providers, their availability and capabilities",
		Run: func(call clientCall) error {
			if err := call.none(); err != nil {
				return err
			}
			payload, err := requestJSON(call.Ctx, call.Client, http.MethodGet, "/api/providers", nil)
			if err != nil {
				return err
			}
			if call.Options.asJSON {
				writeRaw(call.Env.Stdout, payload)
				return nil
			}
			var described []providerDescriptor
			if err := decodePayload(payload, &described); err != nil {
				return err
			}
			rows := make([][]string, 0, len(described))
			for _, found := range described {
				rows = append(rows, []string{
					found.ID, found.Label, found.Status.State, found.Status.Reason,
					strings.Join(enabledCapabilities(found.Capabilities), " "),
				})
			}
			writeTable(call.Env.Stdout, []string{"ID", "LABEL", "STATE", "REASON", "CAPABILITIES"}, rows)
			return nil
		},
	}
}

// enabledCapabilities names the capabilities a provider declares. Only the
// boolean flags that are true are listed — the table answers "what can this
// provider do", and a column of falses answers it worse than a short list. The
// non-boolean entries (a concurrency limit, say) are printed as key=value, since
// their value is the answer rather than their presence.
func enabledCapabilities(caps map[string]json.RawMessage) []string {
	named := make([]string, 0, len(caps))
	for name := range caps {
		named = append(named, name)
	}
	sort.Strings(named)

	var enabled []string
	for _, name := range named {
		raw := strings.TrimSpace(string(caps[name]))
		switch raw {
		case "true":
			enabled = append(enabled, name)
		case "false", "null", "":
			// Not declared: leave it out rather than printing a negative.
		default:
			enabled = append(enabled, name+"="+strings.Trim(raw, `"`))
		}
	}
	return enabled
}
