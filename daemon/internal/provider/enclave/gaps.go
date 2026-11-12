// This file documents gaps in the Eclipse Enclave CLI/API
// surface that prevent the HomeShell provider from fully implementing
// the provider contract. Each gap should be filed as a request against
// the Enclave project.
//
// # GAP 1 (resolved): No structured output from `enclave ps`
//
// `enclave ps --json` lists containers as a JSON array, including the options
// each was started with, which is what tells a session that is out of date
// (drift.go).
//
// # GAP 3: No container-lifecycle subscription
//
// Session state comes from the host bridge. When that bridge is gone,
// recovery still needs `enclave ps --all` to distinguish attachment loss
// from agent exit; Enclave has no container event stream.
//
// # GAP 4: `enclave stop` both stops AND removes the container
//
// There is no way to stop a container without removing it. HomeShell's
// model distinguishes "stopped" (can be restarted) from "removed" (gone).
// With the current CLI, stopping is destructive.
//
// Request: Separate `enclave stop` (stop only) from `enclave rm`
// (remove), or add a --no-remove flag.
//
// # GAP 5: No `enclave start` for stopped containers
//
// There is no command to restart a stopped container. Combined with GAP 4
// (stop removes), the start/stop lifecycle is effectively create/destroy.
//
// Request: Add `enclave start <container-name>` to restart a stopped
// container without re-running the full image build pipeline.
//
// # GAP 7: No way to learn a container's published ports / web-IDE URL
//
// The daemon can offer a browser-openable link as an open mode (a provider
// reports a URL and the client opens the tab). Enclave publishes container
// ports (`enclave run -p`) and can attach Theia (`enclave theia`), but reports
// no URL, and this provider does not read the `ports` `enclave ps --json` lists
// (GAP 1). Reading the mapping from Docker directly is out of the question — this
// provider never talks to Docker.
//
// The only URL this provider could build was a per-host guess against a
// configured port. It could not tell two containers apart, so with more than one
// running it answered with whichever container owned the port. This provider
// therefore reports no links at all, and no web-IDE mode is offered for an
// Enclave workstream.
//
// Next step: read the `ports` of `enclave ps --json` (or ask for an explicit
// `enclave theia --url <container>` that prints the URL it would open). Then the
// provider can implement provider.LinkOpener and answer instead of guessing.
//
// # GAP 8: No access to a container's output
//
// `enclave` has no `logs` verb. When a container exits at startup, the reason
// is in its output, and the provider can only point at `docker logs`.
//
// Request: Add `enclave logs <container-name>`.
//
// # GAP 9: `enclave ps --json` does not tell a shell container from an agent's
//
// A container `enclave shell` starts runs the tool's image under the tool's
// profile, and `ps --json` lists it like the agent container of a foreground
// session. HomeShell adopts sessions started with the CLI from that listing
// (adopt.go), so such a shell shows as a foreground session.
//
// Request: Add the command a container runs (agent or shell) to `ps --json`.
//
// # GAP 10: `enclave ps --json` does not say whether a session is attached
//
// HomeShell adopts a background session started with the CLI by attaching a
// bridge to it (adopt.go). Two HomeShell daemons with different data
// directories whose projects cover the same folder each list the session and
// each attach: nothing in the listing says another one already has. The `hs-`
// session names keep each daemon off the sessions the other started, but a
// CLI session has no such mark.
//
// Request: Report in `ps --json` whether, and by how many clients, a background
// session is attached.
//
// # GAP 11: `enclave ps --json` options say less than the create form can set
//
// HomeShell restarts a session started with the CLI with the `options` Enclave
// recorded for its container, as far as the create form models them (features,
// directories, ports, bridge ports, allowed domains, host config). Lists only
// extend the configured ones (enclave#99), so an entry the session dropped from
// a configured list comes back; other options and the tool's own arguments
// restart with what Enclave resolves now.
//
// Request: Let a run take an option to replace a list, and report the tool's
// arguments in `ps --json`.

package enclave
