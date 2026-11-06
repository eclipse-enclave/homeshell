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

package enclave
