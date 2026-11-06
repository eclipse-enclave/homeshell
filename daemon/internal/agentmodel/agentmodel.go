// Package agentmodel is the model plumbing shared by every provider that
// launches a coding agent.
//
// It exists because more than one provider launches the same agent: the Local
// System provider runs Claude on the host and Eclipse Enclave runs it in a
// container, and a user picking "which Claude" should see the same list either
// way. What belongs here is what both need and neither owns — the rule for which
// list wins (Offered) and the parsing and caching of a CLI's own answer
// (discover.go).
//
// **The catalog itself is not here.** Which models an agent offers is user-authored
// data in the agent catalog (internal/agentcatalog),
// reached through the Catalog port below, because a list written in Go (the per-agent
// model lists provider/local used to carry) could only change in a release while the
// thing it mirrors changes on the vendors' schedule. An agent whose CLI can be asked
// is asked instead: OpenCode and pi report what *that installation* has
// authenticated, which no release could know.
package agentmodel
