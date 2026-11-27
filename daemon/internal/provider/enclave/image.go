package enclave

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/provider"
)

// Attaching an image: Enclave imports it into the image inbox of the session's project (`enclave img import`),
// which every session of that project with the inbox on mounts read-only; HomeShell then hands it to the agent,
// without submitting it: by native paste when the agent pastes the image's format, else as its path.

var _ provider.ImageAttacher = (*Provider)(nil)

// recordedImageInbox reports whether a session started with options may have the image inbox: Enclave recorded
// it on, or recorded no options at all. Why "no options" counts: Enclave omits them when a session's provenance is
// unreadable, yet still finds its inbox by a container label, so the session may have one; if not, Enclave's error
// reaches the user. Options without `image_inbox` come from an Enclave before the inbox, so there is none.
func recordedImageInbox(options map[string]json.RawMessage) bool {
	if options == nil {
		return true
	}
	inboxOn, _ := decode[bool](options, "image_inbox")
	return inboxOn
}

// mayHaveImageInbox reports whether the record's session may have the image inbox, as the inventory last listed
// it, else as its record says. Called with b.mu held.
func (b *hostBridges) mayHaveImageInbox(record bridgeRecord) bool {
	if listed, ok := b.imageInboxes[record.Container.Name]; ok {
		return listed
	}
	return recordedImageInbox(record.Container.Options)
}

// attachable reports whether the record's session can take an image: the installed Enclave has the image inbox
// (supported, asked of the CLI before taking b.mu), HomeShell runs a live bridge to the session, and it may have
// the inbox. Called with b.mu held.
func (b *hostBridges) attachable(record bridgeRecord, supported bool) bool {
	return supported && record.bridged() && record.State == "" && b.mayHaveImageInbox(record)
}

// published is session as the provider reports it: with the latest drift result, and with attachImage added to
// its capabilities when attachable. Called with b.mu held.
func (b *hostBridges) published(session domain.Session, record bridgeRecord, supported bool) domain.Session {
	session = b.withOutdated(session)
	if b.attachable(record, supported) {
		// No capabilities means a live session; a copy, as they may be shared.
		capabilities := domain.SessionCapabilities{Live: true}
		if session.Capabilities != nil {
			capabilities = *session.Capabilities
		}
		capabilities.AttachImage = true
		session.Capabilities = &capabilities
	}
	return session
}

// rememberLaunchedImageInbox remembers whether the session HomeShell starts, or restarts, as name with opts gets
// the image inbox, until an inventory check lists it: the flag the run passes, else Enclave's config, which the run
// resolves the same way. A create or restart with a sandbox choice asked for that config already, so it is
// remembered; one without asks Enclave here, after the run. Why not skip the ask: an unknown inbox counts as "may
// have it", so the row would offer attaching to a session whose inbox is off until the next inventory check.
// When Enclave cannot say, what was known of an earlier container under the name is dropped.
func (p *Provider) rememberLaunchedImageInbox(ctx context.Context, name, dir string, opts RunOpts) {
	if !p.cli.ImageInbox() {
		return
	}
	switch {
	case slices.Contains(opts.ExtraFlags, "--image-inbox"):
		p.bridges.rememberImageInbox(name, true, true)
	case slices.Contains(opts.ExtraFlags, "--image-inbox=false"):
		p.bridges.rememberImageInbox(name, false, true)
	default:
		config, err := p.enclaveConfig(ctx, dir, opts.Tool)
		p.bridges.rememberImageInbox(name, config.Options.Bool("image_inbox"), err == nil)
	}
}

// rememberImageInbox records whether the session may have the image inbox, or forgets it when that is not known.
func (b *hostBridges) rememberImageInbox(name string, inbox, known bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if known {
		b.imageInboxes[name] = inbox
	} else {
		delete(b.imageInboxes, name)
	}
}

// imageInboxesFrom remembers, for each container the inventory listed, whether it may have the image inbox, and
// reports whether that changed for any session. A record written since the listing began keeps what was known.
func (p *Provider) imageInboxesFrom(found inventory) bool {
	bridges := p.bridges
	bridges.mu.Lock()
	defer bridges.mu.Unlock()
	changed := false
	for _, listing := range found.listings {
		for _, container := range listing.containers {
			record, recorded := bridges.records[container.Name]
			if !recorded || bridges.changedSince(found, container.Name) {
				continue
			}
			inbox := recordedImageInbox(container.Options)
			if inbox != bridges.mayHaveImageInbox(record) {
				changed = true
			}
			bridges.imageInboxes[container.Name] = inbox
		}
	}
	for name := range bridges.imageInboxes {
		if _, recorded := bridges.records[name]; !recorded {
			delete(bridges.imageInboxes, name)
		}
	}
	return changed
}

// pasteKey is Ctrl+V. In the sandbox it pastes the project inbox's newest image during Enclave's paste window,
// which an import opens.
const pasteKey = "\x16"

// AttachImage imports the image into the image inbox of the session's project and hands it to the agent without
// submitting it (implements provider.ImageAttacher): Ctrl+V when the agent's catalog entry lists the image's
// format (agentspec.Agent.ImagePaste), else the image's path followed by a space. Attaches in one project run one
// at a time, import and delivery together. Enclave's error is returned as it words it.
func (p *Provider) AttachImage(ctx context.Context, ref provider.SessionRef, image provider.Image) (string, error) {
	record, err := p.liveBridgeRecord(ref.SessionID)
	if err != nil {
		return "", err
	}
	if err := p.checkProject(ref, record); err != nil {
		return "", err
	}
	supported := p.cli.ImageInbox()
	p.bridges.mu.Lock()
	attachable := p.bridges.attachable(record, supported)
	p.bridges.mu.Unlock()
	if !attachable {
		return "", provider.ErrCapabilityUnsupported
	}

	unlock, err := p.imageLocks.lock(ctx, ref.ProjectID)
	if err != nil {
		return "", err
	}
	defer unlock()
	path, err := p.cli.ImportImage(ctx, record.Container.Directory, record.Container.Name, image.Data)
	if err != nil {
		return "", err
	}
	input, handing := path+" ", "typing the image's path into"
	if agent, described := p.agentFor(record.Container.Tool); described && agent.PastesImage(image.MediaType) {
		input, handing = pasteKey, "pasting the image into"
	}
	if err := p.bridges.runtime.SendRaw(ctx, record.target(), input); err != nil {
		return "", fmt.Errorf("%s the agent's input: %w", handing, err)
	}
	return path, nil
}

// projectLocks serializes image attaches per project. Why: Enclave's inbox is per project, and a native paste
// takes the inbox's newest image, so another attach in between could hand an agent the wrong one. An import from
// a terminal at the same moment is not covered.
type projectLocks struct {
	mu sync.Mutex
	// locks holds one token per project, taken while an attach runs.
	locks map[string]chan struct{}
}

// lock takes the project's lock and returns its unlock, or gives up when ctx ends first.
func (l *projectLocks) lock(ctx context.Context, projectID string) (func(), error) {
	l.mu.Lock()
	if l.locks == nil {
		l.locks = map[string]chan struct{}{}
	}
	projectLock, ok := l.locks[projectID]
	if !ok {
		projectLock = make(chan struct{}, 1)
		l.locks[projectID] = projectLock
	}
	l.mu.Unlock()
	select {
	case projectLock <- struct{}{}:
		return func() { <-projectLock }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
