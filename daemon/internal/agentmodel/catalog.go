package agentmodel

// The catalog port, and the one rule for which list an agent's Model field shows.
//
// Both live here because both providers need them and neither owns them: `claude`
// must mean the same models whether the Local provider runs it on the host or
// Enclave runs it in a container, and the two would drift the moment each decided for
// itself.

// Catalog is the user-authored model list, keyed by the agent id every provider
// shares (`claude`, `codex`, …). Implemented by internal/agentcatalog over
// the catalog file; a provider holds the port rather than the table so it can be given
// a stub, and so a daemon with no persistence wired simply has none.
//
// Models returns no error on purpose: the caller is assembling a create form, and
// an unreadable catalog means "offer no list" — a free-text Model field, which is
// the same shape an agent with no catalog already gets. Whoever implements it logs
// the reason.
type Catalog interface {
	Models(agent string) []string
}

// Offered is what an agent's Model field should list.
//
// **What the CLI reported wins.** An agent that enumerates its own models
// (`opencode models`, `pi --list-models`) is answering for the installation it is
// part of — which providers it has authenticated — and no stored list can know
// that. For the agents that cannot answer, discovered is always empty and the
// authored rows are what is offered, which is the whole point of keeping them.
//
// The authored rows are also the fallback when a CLI that *can* answer does not:
// an unauthenticated pi reports nothing, and a user who wrote down the models they
// use gets them either way.
//
// Empty means the field is free text — the honest shape when nothing can say what
// the models are. catalog may be nil, which is a daemon with no catalog wired
// (tests, and any build without persistence).
func Offered(discovered []string, catalog Catalog, agent string) []string {
	if len(discovered) > 0 {
		return discovered
	}
	if catalog == nil {
		return nil
	}
	return catalog.Models(agent)
}
