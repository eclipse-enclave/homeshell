package enclave

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
)

// CLI sessions: Enclave sessions started with the `enclave` CLI rather than in HomeShell. The inventory check
// finds them; HomeShell attaches to a background one and runs it as its own, and keeps a bridgeless Foreground
// record of a foreground one, which runs in the user's terminal and cannot be attached to. A foreground session
// that leaves the listing has ended.

// ideTools are the tool profiles of IDE containers, which are not agent sessions.
var ideTools = []string{"theia", "theia-next"}

// agentTools are the tool profiles whose containers are agent sessions: those `enclave tools` lists, but the IDE
// profiles. An Enclave listing none has the one profile every installation has, as for SessionSchema.
func (p *Provider) agentTools(ctx context.Context) ([]string, error) {
	tools, err := p.cli.Tools(ctx)
	if err != nil {
		return nil, err
	}
	if len(tools) == 0 {
		tools = []Tool{{Name: defaultTool}}
	}
	var names []string
	for _, tool := range tools {
		if !slices.Contains(ideTools, tool.Name) {
			names = append(names, tool.Name)
		}
	}
	return names, nil
}

// sessionNameOf is the container's session name, read off its name when the listing does not say.
func sessionNameOf(container Container) string {
	if container.SessionName != "" {
		return container.SessionName
	}
	return parseSessionName(container.Name, container.Tool)
}

// adoptListed adopts the CLI sessions the inventory lists and ends the foreground ones it no longer lists,
// reporting whether any record changed, and saves the records once.
func (p *Provider) adoptListed(ctx context.Context, listed inventory) bool {
	attempted, adopted := p.adoptRunning(ctx, listed)
	ended := p.endForeground(listed)
	if attempted || ended {
		p.bridges.mu.Lock()
		err := p.bridges.save()
		p.bridges.mu.Unlock()
		if err != nil {
			slog.Warn("persisting enclave sessions started with the CLI failed", "error", err)
		}
	}
	return adopted || ended
}

// adoptRunning adopts each listed container that is due, once however many projects list it, reporting whether it
// tried any and whether any was adopted. The agent tool profiles, which tell an agent's container from others, are
// listed once a running unrecorded container needs them; without them no such container is adopted.
func (p *Provider) adoptRunning(ctx context.Context, listed inventory) (attempted, adopted bool) {
	var agentTools []string
	var toolsErr error
	toolsListed := false
	seen := map[string]bool{}
	for _, listing := range listed.listings {
		project := refOf(listing.project)
		for _, container := range listing.containers {
			if seen[container.Name] || !ownsContainerIn(listed.projects, project, container) {
				continue
			}
			seen[container.Name] = true
			record, recorded := p.bridgeRecordFor(container.Name)
			if !recorded && !toolsListed && mayBeCLISession(container) {
				toolsListed = true
				if agentTools, toolsErr = p.agentTools(ctx); toolsErr != nil {
					slog.Debug("not adopting enclave sessions started with the CLI: cannot list the tool profiles",
						"error", toolsErr)
				}
			}
			if !adoptionDue(container, record, recorded, agentTools) {
				continue
			}
			attempted = true
			done, err := p.adoptContainer(ctx, container, listed, agentTools)
			if err != nil {
				slog.Warn("adopting an enclave session started with the CLI failed; trying again on the next check",
					"container", container.Name, "error", err)
			}
			adopted = adopted || done
		}
	}
	return attempted, adopted
}

// adoptionDue reports whether a running listed container is to be adopted: a recorded session that ended and runs
// again, as a user does under the same name, or, without a record, a container of one of agentTools that may be a
// CLI session.
func adoptionDue(container Container, record bridgeRecord, recorded bool, agentTools []string) bool {
	if recorded {
		return container.Status == "running" && record.State.Terminal()
	}
	return mayBeCLISession(container) && slices.Contains(agentTools, container.Tool)
}

// mayBeCLISession reports whether a container without a record may be a CLI session, before its tool profile is
// known: it runs, and its session name does not mark it as one another HomeShell daemon started.
func mayBeCLISession(container Container) bool {
	return container.Status == "running" && !strings.HasPrefix(sessionNameOf(container), sessionNamePrefix)
}

// adoptContainer records a listed container as a session and attaches a bridge to a background one, leaving the
// save to adoptListed. One whose record changed since the listing, or that is restarting, is left alone: the
// listing may predate the change. A container whose bridge cannot attach is the user's and keeps running,
// unrecorded, for the next check to try again. Its agent was running before the attach, so attachedAt stays unset:
// an exit right after is an end.
func (p *Provider) adoptContainer(ctx context.Context, container Container, listed inventory, agentTools []string) (bool, error) {
	bridges := p.bridges
	adopted := false
	err := bridges.withLifecycle(container.Name, func(guard *bridgeGuard) error {
		bridges.mu.Lock()
		previous, recorded := bridges.records[container.Name]
		_, restarting := bridges.restarting[container.Name]
		if restarting || bridges.changedSince(listed, container.Name) || !adoptionDue(container, previous, recorded, agentTools) {
			bridges.mu.Unlock()
			return nil
		}
		record := bridgeRecord{Container: container, Foreground: !container.Background, LastTitle: previous.LastTitle}
		if record.Foreground {
			bridges.setRecord(container.Name, record)
			delete(bridges.observed, container.Name)
			guard.invalidate()
			bridges.mu.Unlock()
		} else {
			bridges.mu.Unlock()
			// Saved before the attach, unlike a foreground record: a bridge attached without a record would be
			// attached again after a crash.
			if err := p.attachRecord(ctx, guard, record, previous, time.Time{}); err != nil {
				bridges.mu.Lock()
				bridges.restore(container.Name, previous, recorded)
				bridges.mu.Unlock()
				return err
			}
		}
		adopted = true
		if recorded {
			p.forgetLaunchConfig(container.Name)
		}
		return nil
	})
	return adopted, err
}

// forgetLaunchConfig drops what HomeShell last started a session with, once the session was started again in a
// terminal under the same name: drift and a restart in HomeShell then go by the options it runs with, which Enclave
// reports, rather than replay settings it no longer has.
func (p *Provider) forgetLaunchConfig(name string) {
	if p.store != nil {
		p.store.ForgetLaunchConfig(name)
	}
}

// restore puts back the record a failed adoption replaced, or removes the one it added; called with b.mu held.
func (b *hostBridges) restore(name string, previous bridgeRecord, recorded bool) {
	if recorded {
		b.setRecord(name, previous)
	} else {
		b.deleteRecord(name)
	}
}

// endForeground marks done each live foreground session no project lists any more. Only a successful listing
// says that: a session a project that could not list owns stays live. One whose record changed during the
// listing, or that is restarting, is left alone.
func (p *Provider) endForeground(listed inventory) bool {
	running := map[string]bool{}
	for _, listing := range listed.listings {
		for _, container := range listing.containers {
			running[container.Name] = true
		}
	}
	bridges := p.bridges
	bridges.mu.Lock()
	defer bridges.mu.Unlock()
	changed := false
	for name, record := range bridges.records {
		_, restarting := bridges.restarting[name]
		if !record.Foreground || record.State != "" || restarting || running[name] || bridges.changedSince(listed, name) {
			continue
		}
		listedWithout, listFailed := false, false
		for _, listing := range listed.listings {
			if !ownsContainerIn(listed.projects, refOf(listing.project), record.Container) {
				continue
			}
			if listing.err != nil {
				listFailed = true
			} else {
				listedWithout = true
			}
		}
		if !listedWithout || listFailed {
			continue
		}
		record.State, record.Error = domain.SessionDone, ""
		bridges.setRecord(name, record)
		delete(bridges.observed, name)
		bridges.guard(name).invalidate()
		changed = true
	}
	return changed
}
