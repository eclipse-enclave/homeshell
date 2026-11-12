package enclave

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"reflect"
	"slices"
	"sync"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/detect"
	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/hostbridge"
	"github.com/eclipsesource/homeshell/daemon/internal/persist"
	"github.com/eclipsesource/homeshell/daemon/internal/provider"
	"github.com/eclipsesource/homeshell/daemon/internal/sbridge"
	"github.com/eclipsesource/homeshell/daemon/internal/terminal"
)

// BridgeRuntime is a host PTY bridge. Container creation is deliberately not
// part of it: losing this process must never restart a container or agent.
type BridgeRuntime interface {
	Available(context.Context, string) error
	Start(context.Context, string, string, []string) error
	Snapshot(context.Context, string) (sbridge.Snapshot, error)
	Stop(context.Context, string) error
	SendInput(context.Context, string, string) error
	SendKey(context.Context, string, string) error
	AttachCommand(string) []string
}

// bridgeLocator is the optional BridgeRuntime capability that finds a bridge
// still serving where an earlier daemon start put it (see local.LocationRecorder).
type bridgeLocator interface {
	Location(target string) string
	LocateElsewhere(ctx context.Context, target, recorded string) string
}

var _ bridgeLocator = hostbridge.Runtime{}

type bridgeRecord struct {
	Container Container `json:"container"`
	// Sock is where the bridge was attached, so a daemon resolving another
	// socket directory does not attach the container a second time.
	Sock      string              `json:"sock,omitempty"`
	LastTitle string              `json:"lastTitle,omitempty"`
	State     domain.SessionState `json:"state,omitempty"`
	Error     string              `json:"error,omitempty"`
}

// bridgeGuard is one target's per-session state. lifecycle serializes its side
// effects, never all sessions'. The rest is protected by hostBridges.mu:
// revision invalidates observations across lifecycle changes.
type bridgeGuard struct {
	lifecycle sync.Mutex
	// sampling skips a poll while the socket or Docker is already being
	// queried, including by a background recovery.
	sampling   bool
	revision   uint64
	retryAt    time.Time
	retryDelay time.Duration
	retryError error
	hold       detect.Hold
	// attachedAt is when this daemon attached the container, cleared once a rule
	// recognizes the agent's screen. invalidate keeps it.
	attachedAt time.Time
}

// invalidate is called with hostBridges.mu held. Explicit lifecycle changes
// discard stale observations, the idle hold and the old runtime's cooldown.
func (g *bridgeGuard) invalidate() {
	g.revision++
	g.retryAt, g.retryDelay, g.retryError = time.Time{}, 0, nil
	g.hold = detect.Hold{}
}

// bridgeDetail is the Enclave detection detail carried in ObservationData.
type bridgeDetail struct {
	detect.Detail
	// ReadError is why the terminal could not be read; Session.Error is for failed sessions only.
	ReadError string `json:"readError,omitempty"`
}

const (
	recoveryRetryMin = 2 * time.Second
	recoveryRetryMax = 30 * time.Second
	// recoveryTimeout bounds inventory and reattach; cleanup keeps cmdTimeout so a cut stop cannot leak the gateway.
	recoveryTimeout = 5 * time.Second
	// startupSettle is how soon after attaching an exit is a failed start; a dying container was measured exiting about 0.5 s after `enclave run`.
	startupSettle = 3 * time.Second
)

type hostBridges struct {
	// mu protects the provider's metadata, not runtime or CLI I/O.
	mu      sync.Mutex
	runtime BridgeRuntime
	path    string
	// seq numbers encoded record files so an older one never overwrites a newer one.
	seq uint64
	// writeMu serializes file writes, so observations can fsync without holding mu.
	writeMu sync.Mutex
	// last is what the record file holds (guarded by writeMu), so an unchanged save writes nothing.
	last     []byte
	written  uint64
	records  map[string]bridgeRecord
	observed map[string]domain.Session
	guards   map[string]*bridgeGuard
	// restarting publishes in-flight replacements of owned sessions.
	restarting map[string]restartProjection
	// outdated is the latest drift result, refreshed separately from polling.
	outdated map[string][]string
	restarts uint64
}

// guard is called with b.mu held. Retain guards after removal so an old poll
// and a recreated target cannot acquire different lifecycle locks. This keeps
// one small entry per target for the lifetime of the provider.
func (b *hostBridges) guard(id string) *bridgeGuard {
	if b.guards == nil {
		b.guards = map[string]*bridgeGuard{}
	}
	guard := b.guards[id]
	if guard == nil {
		guard = &bridgeGuard{}
		b.guards[id] = guard
	}
	return guard
}

func (b *hostBridges) guardFor(id string) *bridgeGuard {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.guard(id)
}

// withLifecycle runs fn holding id's lifecycle lock, invalidating on entry and
// exit: a poll that read state before or during fn can neither publish over
// its result nor attach a second bridge beside the one fn started.
func (b *hostBridges) withLifecycle(id string, fn func(*bridgeGuard) error) error {
	guard := b.guardFor(id)
	guard.lifecycle.Lock()
	defer guard.lifecycle.Unlock()
	b.invalidate(guard)
	defer b.invalidate(guard)
	return fn(guard)
}

// location is where the runtime currently resolves target, "" when it cannot tell.
func (b *hostBridges) location(target string) string {
	if locator, ok := b.runtime.(bridgeLocator); ok {
		return locator.Location(target)
	}
	return ""
}

func (b *hostBridges) invalidate(guard *bridgeGuard) {
	b.mu.Lock()
	guard.invalidate()
	b.mu.Unlock()
}

// NewWithBridge enables host attachment for newly created sessions. Persisted
// records are the sole source of managed session ownership. A corrupt record file fails construction rather than silently losing owners.
func NewWithBridge(cli CLI, runtime BridgeRuntime, path string) (*Provider, error) {
	bridges := &hostBridges{
		runtime: runtime, path: path, records: map[string]bridgeRecord{}, observed: map[string]domain.Session{},
		restarting: map[string]restartProjection{}, outdated: map[string][]string{},
	}
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if err == nil {
		var saved struct {
			Version int            `json:"version"`
			Records []bridgeRecord `json:"records"`
		}
		if err := json.Unmarshal(data, &saved); err != nil {
			return nil, fmt.Errorf("read enclave bridge records: %w", err)
		}
		if saved.Version != 1 {
			return nil, fmt.Errorf("unsupported enclave bridge records version %d", saved.Version)
		}
		for _, record := range saved.Records {
			if record.Container.Name == "" || record.Container.Directory == "" {
				return nil, fmt.Errorf("invalid enclave bridge record")
			}
			if _, exists := bridges.records[record.Container.Name]; exists {
				return nil, fmt.Errorf("duplicate enclave bridge record %q", record.Container.Name)
			}
			bridges.records[record.Container.Name] = record
		}
		bridges.last = data
	}
	p := &Provider{cli: cli, bridges: bridges, workstreamsRoot: provider.DefaultWorkstreamsRoot}
	p.term = terminal.NewOSLauncher(p.TerminalPreference)
	return p, nil
}

func shortContainerHash(container string) string {
	sum := sha256.Sum256([]byte(container))
	return hex.EncodeToString(sum[:])[:20]
}

func (r bridgeRecord) target() string { return bridgeTarget(r.Container.Name) }

func bridgeTarget(container string) string {
	// Container names can exceed sun_path; keep the runtime target short and
	// separate from local agent UUIDs even within the same runtime namespace.
	// Name is Docker's identity, not an argv fragment or a filesystem path.
	return "enc-" + shortContainerHash(container)
}

// recordFile is an encoded snapshot of the records, ordered by seq.
type recordFile struct {
	data []byte
	seq  uint64
}

// save persists the records synchronously; called with b.mu held.
func (b *hostBridges) save() error {
	file, err := b.encode()
	if err != nil {
		return err
	}
	return b.write(file)
}

// encode is called with b.mu held.
func (b *hostBridges) encode() (recordFile, error) {
	if b.path == "" {
		return recordFile{}, nil
	}
	records := make([]bridgeRecord, 0, len(b.records))
	for _, name := range slices.Sorted(maps.Keys(b.records)) {
		records = append(records, b.records[name])
	}
	data, err := json.Marshal(struct {
		Version int            `json:"version"`
		Records []bridgeRecord `json:"records"`
	}{1, records})
	if err != nil {
		return recordFile{}, err
	}
	b.seq++
	return recordFile{data: data, seq: b.seq}, nil
}

// write needs no b.mu; a file encoded before the one already written is dropped.
func (b *hostBridges) write(file recordFile) error {
	if file.data == nil {
		return nil
	}
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	if file.seq <= b.written {
		return nil
	}
	if !bytes.Equal(file.data, b.last) {
		if err := persist.WriteFileAtomic(b.path, file.data); err != nil {
			return err
		}
		b.last = file.data
	}
	b.written = file.seq
	return nil
}

// registerBridge persists ownership before attachment. A crash between the
// preceding CLI run and this persistence can still leave an untracked container.
func (p *Provider) registerBridge(ctx context.Context, container Container) error {
	bridges := p.bridges
	return bridges.withLifecycle(container.Name, func(guard *bridgeGuard) error {
		bridges.mu.Lock()
		record := bridgeRecord{Container: container, Sock: bridges.location(bridgeTarget(container.Name))}
		bridges.records[container.Name] = record
		delete(bridges.observed, container.Name)
		persistErr := bridges.save()
		bridges.mu.Unlock()
		var err error
		if persistErr != nil {
			err = fmt.Errorf("persist enclave bridge: %w", persistErr)
		} else if startErr := bridges.runtime.Start(ctx, record.target(), container.Directory, p.cli.AttachCommand(container.Name)); startErr != nil {
			err = fmt.Errorf("attach enclave container: %w", startErr)
		}
		if err == nil {
			// A poll during the attach found no socket yet; reject and drop that observation.
			bridges.mu.Lock()
			guard.invalidate()
			guard.attachedAt = p.clock()
			delete(bridges.observed, container.Name)
			bridges.mu.Unlock()
			return nil
		}
		// This is the single owner of cleanup after either persistence or attachment
		// failure. Keep ownership if cleanup fails, so an explicit removal can retry.
		cleanupErr := p.cli.Stop(context.WithoutCancel(ctx), container.Name)
		bridges.mu.Lock()
		defer bridges.mu.Unlock()
		if cleanupErr == nil {
			delete(bridges.records, container.Name)
			delete(bridges.observed, container.Name)
			if saveErr := bridges.save(); saveErr != nil {
				err = errors.Join(err, saveErr)
			}
		} else {
			record.State = domain.SessionError
			record.Error = err.Error()
			bridges.records[container.Name] = record
			_ = bridges.save()
			err = errors.Join(err, fmt.Errorf("cleanup enclave container: %w", cleanupErr))
		}
		return err
	})
}

func (p *Provider) bridgeSession(record bridgeRecord) domain.Session {
	state := record.State
	if state == "" {
		state = domain.SessionRunning
	}
	title := detect.CleanTitle(record.LastTitle)
	if title == "" {
		title = fmt.Sprintf("%s session", record.Container.Tool)
	}
	return domain.Session{ID: record.Container.Name, Agent: record.Container.Tool, Title: title, State: state, Error: record.Error}
}

// sampleBridge captures outside the metadata lock. A stopped, removed or
// restarted target rejects any old observation before classification/publication.
// Only the watcher may recover: request-scoped reads must not mutate lifecycle.
func (p *Provider) sampleBridge(ctx context.Context, id string, agents map[string]string, recoverMissing bool) {
	bridges := p.bridges
	bridges.mu.Lock()
	guard := bridges.guard(id)
	record, exists := bridges.records[id]
	// A restart publishes its own projection; its old bridge is gone on purpose.
	_, restarting := bridges.restarting[id]
	if !exists || restarting || guard.sampling {
		bridges.mu.Unlock()
		return
	}
	guard.sampling = true
	revision := guard.revision
	session, observed := bridges.observed[id]
	bridges.mu.Unlock()
	if !observed {
		session = p.bridgeSession(record)
	}
	var snap sbridge.Snapshot
	var err error
	var recover bool
	sampledAt := p.clock()
	if record.State == "" {
		snap, err = p.captureSnapshot(ctx, record.target())
		if recoverMissing && errors.Is(err, sbridge.ErrNotServing) {
			var retryErr error
			recover, retryErr = p.recoveryDue(guard, revision)
			if !recover && retryErr != nil {
				err = retryErr
			}
		}
	}
	p.observeBridge(guard, revision, record, session, snap, err, agents)
	if !recover {
		bridges.endSample(guard)
		return
	}
	// Fanout can finish now; later ticks skip only this target until its
	// bounded recovery completes.
	go func() {
		defer bridges.endSample(guard)
		record, err := p.recoverBridge(ctx, guard, revision, record, sampledAt)
		var snap sbridge.Snapshot
		if err == nil && record.State == "" {
			snap, err = p.captureSnapshot(ctx, record.target())
		}
		failure := err
		if failure == nil && snap.Error != "" {
			failure = fmt.Errorf("cannot read enclave terminal: %s", snap.Error)
		}
		p.finishRecovery(guard, revision, failure)
		p.observeBridge(guard, revision, record, session, snap, err, agents)
	}()
}

func (b *hostBridges) endSample(guard *bridgeGuard) {
	b.mu.Lock()
	guard.sampling = false
	b.mu.Unlock()
}

// observeBridge publishes only observations from the current lifecycle. A
// background recovery is picked up by the watcher's next projection tick.
func (p *Provider) observeBridge(guard *bridgeGuard, revision uint64, record bridgeRecord, session domain.Session, snap sbridge.Snapshot, err error, agents map[string]string) {
	bridges := p.bridges
	id := record.Container.Name
	// Resolve bindings and classify before reacquiring metadata. The index was
	// read once by the caller, never read from the agent catalog under bridges.mu.
	agent := record.Container.Tool
	if mapped, ok := agents[agent]; ok {
		agent = mapped
	}
	var detection detect.Result
	if record.State == "" && err == nil && snap.Error == "" {
		if snap.OSCTitle != "" {
			record.LastTitle = rememberBridgeTitle(agent, record.LastTitle, snap.OSCTitle)
		} else if hostbridge.Classify(agent, sbridge.Snapshot{OSCTitle: record.LastTitle}).State != domain.SessionRunning {
			// A turn may have ended unattached; a replayed spinner would outrank the screen.
			snap.OSCTitle = record.LastTitle
		}
		detection = hostbridge.Classify(agent, snap)
		if !detection.Confident && !detection.SkipStateUpdate {
			// A live container with no matching rule keeps its runtime state (#133).
			detection.State = domain.SessionRunning
		}
	}
	now := p.clock()
	bridges.mu.Lock()
	if _, exists := bridges.records[id]; !exists || guard.revision != revision {
		bridges.mu.Unlock()
		return
	}
	if record.State != "" {
		session.State, session.Error = record.State, record.Error
		session.ObservationData = nil
		guard.hold = detect.Hold{}
	} else if err != nil || snap.Error != "" {
		session.State, session.Error = domain.SessionUnknown, ""
		readError := "cannot read enclave terminal"
		if err != nil {
			readError = err.Error()
		}
		if data, err := json.Marshal(bridgeDetail{ReadError: readError}); err == nil {
			session.ObservationData = data
		}
	} else {
		guard.retryAt, guard.retryDelay, guard.retryError = time.Time{}, 0, nil
		if guard.hold.State == "" && detection.SkipStateUpdate {
			// A history viewer right after attach keeps the container's live state.
			guard.hold.State = domain.SessionRunning
		}
		session.State = guard.hold.Observe(agent, detection, now, p.RefreshInterval())
		if detection.Confident {
			guard.attachedAt = time.Time{}
		}
		session.Preview, session.Error = detection.Tail, ""
		if title := detect.CleanTitle(record.LastTitle); title != "" {
			session.Title = title
		}
		if data, err := json.Marshal(bridgeDetail{Detail: detection.Detail()}); err == nil {
			session.ObservationData = data
		}
	}
	bridges.records[id], bridges.observed[id] = record, session
	file, err := bridges.encode()
	bridges.mu.Unlock()
	// Ownership is unchanged; a lost title or state is retried on the next observation.
	if err == nil {
		err = bridges.write(file)
	}
	if err != nil {
		slog.Warn("persisting an enclave observation failed", "container", id, "error", err)
	}
}

// rememberBridgeTitle retains a representative raw title for reattachment.
// Spinner frames with the same human label and detection meaning do not force
// fsync/rename on every poll. Never strip a status glyph used by title rules.
func rememberBridgeTitle(agent, previous, current string) string {
	if previous == current {
		return previous
	}
	if previous != "" && detect.CleanTitle(previous) == detect.CleanTitle(current) {
		before := hostbridge.Classify(agent, sbridge.Snapshot{OSCTitle: previous})
		after := hostbridge.Classify(agent, sbridge.Snapshot{OSCTitle: current})
		if before.State == after.State && before.Confident == after.Confident && before.MatchedRule == after.MatchedRule && before.SkipStateUpdate == after.SkipStateUpdate && before.VisibleBlocker == after.VisibleBlocker {
			return previous
		}
	}
	return current
}

func (p *Provider) captureSnapshot(ctx context.Context, target string) (sbridge.Snapshot, error) {
	captureCtx, cancel := p.CaptureContext(ctx)
	defer cancel()
	return p.bridges.runtime.Snapshot(captureCtx, target)
}

func (p *Provider) recoveryDue(guard *bridgeGuard, revision uint64) (bool, error) {
	now := p.clock()
	bridges := p.bridges
	bridges.mu.Lock()
	defer bridges.mu.Unlock()
	if guard.revision != revision {
		return false, sbridge.ErrNotServing
	}
	if now.Before(guard.retryAt) {
		return false, guard.retryError
	}
	return true, nil
}

func (p *Provider) finishRecovery(guard *bridgeGuard, revision uint64, failure error) {
	now := p.clock()
	bridges := p.bridges
	bridges.mu.Lock()
	defer bridges.mu.Unlock()
	if guard.revision != revision {
		return
	}
	if failure == nil {
		guard.retryAt, guard.retryDelay, guard.retryError = time.Time{}, 0, nil
		return
	}
	delay := guard.retryDelay * 2
	if delay < recoveryRetryMin {
		delay = recoveryRetryMin
	}
	if delay > recoveryRetryMax {
		delay = recoveryRetryMax
	}
	guard.retryDelay, guard.retryAt, guard.retryError = delay, now.Add(delay), failure
}

// startupWindow allows one poll period on top of startupSettle, since only a poll notices the exit.
func (p *Provider) startupWindow() time.Duration {
	interval := p.RefreshInterval()
	if interval <= 0 {
		interval = watchInterval
	}
	return startupSettle + interval
}

// recoverBridge serializes only this target's side effects. A missing socket is
// not an agent exit; Docker must confirm it before cleanup or reattachment.
// missingAt is when the poll that found the socket missing started.
func (p *Provider) recoverBridge(ctx context.Context, guard *bridgeGuard, revision uint64, record bridgeRecord, missingAt time.Time) (bridgeRecord, error) {
	bridges := p.bridges
	bridges.mu.Lock()
	current := guard.revision == revision
	attachedAt := guard.attachedAt
	bridges.mu.Unlock()
	if !current {
		return record, sbridge.ErrNotServing
	}
	// Inventory is read-only and cannot delay an explicit stop. Revalidate after
	// it returns, before serializing this target's attach/cleanup side effects.
	budget, cancel := context.WithTimeout(ctx, recoveryTimeout)
	defer cancel()
	containers, err := p.cli.ListAllSessions(budget, record.Container.Directory)
	if err != nil {
		// Docker unavailable is not an exit, but the old state is no longer known.
		return record, fmt.Errorf("check enclave container: %w", err)
	}
	// A stop or restart invalidates this observation anyway; don't wait for it.
	if !guard.lifecycle.TryLock() {
		return record, sbridge.ErrNotServing
	}
	defer guard.lifecycle.Unlock()
	bridges.mu.Lock()
	_, exists := bridges.records[record.Container.Name]
	current = exists && guard.revision == revision
	bridges.mu.Unlock()
	if !current {
		return record, sbridge.ErrNotServing
	}
	var found *Container
	for _, container := range containers {
		if container.Name == record.Container.Name {
			found = &container
			break
		}
	}
	if found == nil {
		record.State, record.Error = domain.SessionDone, ""
		return record, nil
	}
	if exited(found.Status) {
		// An agent a rule recognized has started; one without rules gets the time window only.
		if !attachedAt.IsZero() && missingAt.Sub(attachedAt) < p.startupWindow() {
			// enclave stop removes the container, and with it the only record of why it died (GAP 8).
			name := record.Container.Name
			record.State = domain.SessionError
			record.Error = fmt.Sprintf("enclave container %q exited right after starting; run `docker logs %s` to see why", name, name)
			return record, nil
		}
		if err := p.cli.Stop(ctx, record.Container.Name); err != nil {
			return record, fmt.Errorf("clean up exited enclave container: %w", err)
		}
		record.State, record.Error = domain.SessionDone, ""
		return record, nil
	}
	if found.Status != "running" {
		return record, fmt.Errorf("enclave container is %s", found.Status)
	}
	if locator, ok := bridges.runtime.(bridgeLocator); ok && record.Sock != "" {
		if elsewhere := locator.LocateElsewhere(budget, record.target(), record.Sock); elsewhere != "" {
			return record, fmt.Errorf("enclave session is still attached at %s, which this daemon no longer uses; not attaching it again", elsewhere)
		}
	}
	if err := bridges.runtime.Start(budget, record.target(), record.Container.Directory, p.cli.AttachCommand(record.Container.Name)); err != nil {
		return record, fmt.Errorf("reattach enclave container: %w", err)
	}
	record.Sock = bridges.location(record.target())
	return record, nil
}

// sampleBridges captures every owned target once, without holding the shared lock.
func (p *Provider) sampleBridges(ctx context.Context, agents map[string]string, owns func(dir string) bool, recoverMissing bool) {
	bridges := p.bridges
	bridges.mu.Lock()
	var ids []string
	for _, id := range slices.Sorted(maps.Keys(bridges.records)) {
		if owns(bridges.records[id].Container.Directory) {
			ids = append(ids, id)
		}
	}
	bridges.mu.Unlock()
	provider.Fanout(len(ids), provider.CaptureConcurrency, func(i int) {
		p.sampleBridge(ctx, ids[i], agents, recoverMissing)
	})
}

// projectBridges reports the current records without fresh I/O, so
// watch-triggered projection never waits for a capture. projects is the store
// snapshot ownership is matched against.
func (p *Provider) projectBridges(project provider.ProjectRef, projects []domain.Project) []provider.DiscoveredSession {
	bridges := p.bridges
	bridges.mu.Lock()
	defer bridges.mu.Unlock()
	var out []provider.DiscoveredSession
	for _, id := range slices.Sorted(maps.Keys(bridges.records)) {
		record := bridges.records[id]
		if !ownsDirIn(projects, project, record.Container.Directory) {
			continue
		}
		session, ok := bridges.observed[id]
		if !ok {
			session = p.bridgeSession(record)
		}
		out = append(out, provider.DiscoveredSession{WorkingDir: record.Container.Directory, Session: bridges.withOutdated(session)})
	}
	for name, restarting := range bridges.restarting {
		if !ownsDirIn(projects, project, restarting.workingDir) {
			continue
		}
		out = slices.DeleteFunc(out, func(discovered provider.DiscoveredSession) bool { return discovered.Session.ID == name })
		out = append(out, provider.DiscoveredSession{WorkingDir: restarting.workingDir, Session: restarting.session})
	}
	return out
}

func (p *Provider) DiscoverWatchedSessions(_ context.Context, project provider.ProjectRef) ([]provider.DiscoveredSession, error) {
	return p.projectBridges(project, p.storeProjects()), nil
}

// changeDetector returns the poll Watch runs each period, reporting whether
// anything the reconciler projects changed.
func (p *Provider) changeDetector() func(context.Context) bool {
	previous := map[string][]provider.DiscoveredSession{}
	return func(ctx context.Context) bool {
		if p.store == nil {
			return false
		}
		// One store clone per tick: ownership is checked per record and project.
		snapshot := p.store.Projects()
		var projects []provider.ProjectRef
		for _, project := range snapshot {
			projects = append(projects, provider.ProjectRef{ID: project.ID, Path: project.Path})
		}
		// A directory owned by two projects is still one target to sample per tick.
		p.sampleBridges(ctx, p.detectionAgents(), func(dir string) bool {
			return slices.ContainsFunc(projects, func(project provider.ProjectRef) bool { return ownsDirIn(snapshot, project, dir) })
		}, true)
		for id := range previous {
			if !slices.ContainsFunc(projects, func(project provider.ProjectRef) bool { return project.ID == id }) {
				delete(previous, id)
			}
		}
		changed := false
		for _, project := range projects {
			out := p.projectBridges(project, snapshot)
			if !reflect.DeepEqual(previous[project.ID], out) {
				changed = true
				previous[project.ID] = out
			}
		}
		return changed
	}
}

func (p *Provider) bridgeRecordFor(id string) (bridgeRecord, bool) {
	p.bridges.mu.Lock()
	defer p.bridges.mu.Unlock()
	record, ok := p.bridges.records[id]
	return record, ok
}

// liveBridgeRecord centralizes the input/open gate for ended or restarting sessions.
func (p *Provider) liveBridgeRecord(id string) (bridgeRecord, error) {
	p.bridges.mu.Lock()
	defer p.bridges.mu.Unlock()
	record, ok := p.bridges.records[id]
	if !ok {
		return record, fmt.Errorf("enclave session %q not found", id)
	}
	if _, restarting := p.bridges.restarting[id]; record.State != "" || restarting {
		return record, provider.ErrSessionStopped
	}
	return record, nil
}

func (p *Provider) removeBridge(ctx context.Context, id string, remove bool) error {
	bridges := p.bridges
	return bridges.withLifecycle(id, func(*bridgeGuard) error {
		bridges.mu.Lock()
		record, ok := bridges.records[id]
		_, restarting := bridges.restarting[id]
		bridges.mu.Unlock()
		if !ok {
			return fmt.Errorf("enclave bridge session %q not found", id)
		}
		// Checked under the lifecycle lock, which a restart holds while it takes over.
		if !remove && restarting {
			return fmt.Errorf("cannot stop a session while its settings restart is in progress")
		}
		// Killing only enclave attach is a detach, so stop the container first. A
		// recorded end is not trusted: a container it missed would keep its gateway.
		if err := p.cli.Stop(ctx, id); err != nil {
			containers, listErr := p.cli.ListAllSessions(ctx, record.Container.Directory)
			if listErr != nil {
				return errors.Join(err, listErr)
			}
			if slices.ContainsFunc(containers, func(container Container) bool { return container.Name == id }) {
				return err
			}
		}
		if err := bridges.runtime.Stop(ctx, record.target()); err != nil {
			return err
		}
		bridges.mu.Lock()
		defer bridges.mu.Unlock()
		if remove {
			delete(bridges.records, id)
			delete(bridges.observed, id)
		} else {
			record.State, record.Error = domain.SessionStopped, ""
			bridges.records[id], bridges.observed[id] = record, p.bridgeSession(record)
		}
		return bridges.save()
	})
}

// StopSession removes the runtime but keeps the durable session/conversation.
func (p *Provider) StopSession(ctx context.Context, ref provider.SessionRef) error {
	err := p.removeBridge(ctx, ref.SessionID, false)
	if err == nil {
		p.notify()
	}
	return err
}

// StartSession only checks the session has ended (stopped, done or error). Starting it
// is an `enclave continue` with the recorded launch config, which the service runs as
// a restart.
func (p *Provider) StartSession(_ context.Context, ref provider.SessionRef) error {
	if record, ok := p.bridgeRecordFor(ref.SessionID); !ok || !record.State.Terminal() {
		return fmt.Errorf("enclave session has not ended")
	}
	return provider.ErrStartIsRestart
}
