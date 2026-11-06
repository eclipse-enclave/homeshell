package provider

import (
	"context"
	"crypto/sha1"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/store"
)

// isPlaceholder reports whether ws is a creation placeholder; the reconciler
// never drops one.
func isPlaceholder(ws domain.Workstream) bool {
	return strings.HasPrefix(ws.ID, PlaceholderIDPrefix)
}

// Reconciler keeps the store's workstreams in sync with session-provider state:
// it attaches each discovered session to the workstream with a matching
// WorkingDir, adopting a workstream when none matches. It implements Sink so
// Watcher-capable providers can push change notifications.
type Reconciler struct {
	store    *store.Store
	registry *Registry
	poll     time.Duration
	trigger  chan struct{}
	dirtyMu  sync.Mutex
	dirty    dirtySet
	// annotate fills the derived per-projection fields of a project's workstreams
	// and sessions (open modes, materialization, restartability). The owning
	// Service supplies it; nil leaves them empty.
	annotate func(projectID string, workstreams []domain.Workstream)
	// projectLocks serializes projection passes per project: each ends in
	// ReplaceWorkstreams, which writes a whole list derived from its own read, so
	// two interleaved passes over one project would have the later revert the
	// earlier. Per project rather than global, so a reconcile triggered by an API
	// call does not queue behind an unrelated project's provider calls.
	locksMu      sync.Mutex
	projectLocks map[string]*sync.Mutex
}

// projectLock returns the mutex guarding projection passes for a project.
func (r *Reconciler) projectLock(projectID string) *sync.Mutex {
	r.locksMu.Lock()
	defer r.locksMu.Unlock()
	if r.projectLocks == nil {
		r.projectLocks = map[string]*sync.Mutex{}
	}
	lock, ok := r.projectLocks[projectID]
	if !ok {
		lock = &sync.Mutex{}
		r.projectLocks[projectID] = lock
	}
	return lock
}

// NewReconciler builds a reconciler. A poll of 0 disables periodic polling
// (the reconciler then refreshes only on explicit calls and Sink.Changed).
func NewReconciler(st *store.Store, reg *Registry, poll time.Duration) *Reconciler {
	return &Reconciler{
		store:    st,
		registry: reg,
		poll:     poll,
		trigger:  make(chan struct{}, 1),
	}
}

// watcherSink binds a change notification to its provider without changing
// the provider-facing Sink contract.
type watcherSink struct {
	reconciler *Reconciler
	providerID string
}

func (s watcherSink) Changed() { s.reconciler.markDirty(s.providerID) }

// Changed requests a full refresh. Provider watchers use their scoped sink.
func (r *Reconciler) Changed() { r.markDirty("") }

// dirtySet names the providers a reconcile refreshes: all of them, or ids.
// Only a full refresh rediscovers; a scoped one projects watcher observations.
type dirtySet struct {
	all bool
	ids map[string]bool
}

func (d dirtySet) has(id string) bool { return d.all || d.ids[id] }

func (r *Reconciler) markDirty(id string) {
	r.dirtyMu.Lock()
	if id == "" {
		r.dirty.all = true
	} else {
		if r.dirty.ids == nil {
			r.dirty.ids = map[string]bool{}
		}
		r.dirty.ids[id] = true
	}
	r.dirtyMu.Unlock()
	select {
	case r.trigger <- struct{}{}:
	default:
	}
}

func (r *Reconciler) takeDirty() dirtySet {
	r.dirtyMu.Lock()
	defer r.dirtyMu.Unlock()
	dirty := r.dirty
	r.dirty = dirtySet{}
	return dirty
}

// Reconcile projects every session provider's sessions into every project.
func (r *Reconciler) Reconcile(ctx context.Context) {
	r.reconcileOnly(ctx, dirtySet{all: true})
}

func (r *Reconciler) reconcileOnly(ctx context.Context, only dirtySet) {
	if !only.all && len(only.ids) == 0 {
		return
	}
	for _, project := range r.store.Projects() {
		r.reconcileProjectOnly(ctx, project, only)
	}
	r.store.RetryUnsavedWrite()
}

// ReconcileProject reconciles a single project by ID.
func (r *Reconciler) ReconcileProject(ctx context.Context, projectID string) error {
	for _, project := range r.store.Projects() {
		if project.ID == projectID {
			r.reconcileProject(ctx, project)
			return nil
		}
	}
	return fmt.Errorf("project %q not found", projectID)
}

func (r *Reconciler) reconcileProject(ctx context.Context, project domain.Project) {
	r.reconcileProjectOnly(ctx, project, dirtySet{all: true})
}

func (r *Reconciler) reconcileProjectOnly(ctx context.Context, project domain.Project, only dirtySet) {
	lock := r.projectLock(project.ID)
	lock.Lock()
	defer lock.Unlock()
	ref := ProjectRef{ID: project.ID, Path: project.Path, GitBacked: project.GitBacked}

	// Gather the sessions every available session provider observes, keyed by
	// the cleaned working directory they run in. refreshed records which
	// providers reported this cycle, so a provider that hit a transient error
	// does not flush its sessions from the store.
	refreshed := map[string]bool{}
	discovered := map[string][]domain.Session{}
	for _, p := range r.registry.SessionProviders() {
		if !only.has(p.ID()) {
			continue
		}
		available, err := p.Available(ctx)
		if err != nil {
			slog.Warn("provider availability check failed", "provider", p.ID(), "error", err)
			continue
		}
		if !available {
			// Its sessions are gone, so count it as refreshed with none.
			refreshed[p.ID()] = true
			continue
		}
		var sessions []DiscoveredSession
		if watched, ok := p.(WatchedDiscoverer); !only.all && ok {
			sessions, err = watched.DiscoverWatchedSessions(ctx, ref)
		} else {
			sessions, err = p.DiscoverSessions(ctx, ref)
		}
		if err != nil {
			slog.Warn("provider failed to discover sessions", "provider", p.ID(), "project", project.ID, "error", err)
			continue
		}
		refreshed[p.ID()] = true
		for _, ds := range sessions {
			sess := ds.Session
			sess.Provider = p.ID()
			dir := cleanDir(ds.WorkingDir)
			discovered[dir] = append(discovered[dir], sess)
		}
	}

	// A fresh discovery supersedes any retained session with the same ID,
	// wherever it sits, so an ID cannot land in the result set twice.
	// Empty-dir discoveries are excluded: neither pass attaches them anywhere,
	// so letting them supersede would drop the stored session outright.
	superseded := map[string]bool{}
	for dir, sessions := range discovered {
		if dir == "" {
			continue
		}
		for _, sess := range sessions {
			superseded[sess.ID] = true
		}
	}

	// Discovery can be slow and workstreams land concurrently, so re-read the
	// store: ReplaceWorkstreams below writes the whole list, and deriving it
	// from the pre-discovery snapshot would erase anything added in between.
	workstreams := project.Workstreams
	for _, current := range r.store.Projects() {
		if current.ID == project.ID {
			workstreams = current.Workstreams
			break
		}
	}

	result := []domain.Workstream{}
	matchedDirs := map[string]bool{}

	// Pass 1: existing workstreams keep their identity; re-attach sessions by
	// matching working directory.
	for _, ws := range workstreams {
		if isPlaceholder(ws) {
			result = append(result, ws)
			continue
		}
		dir := cleanDir(ws.WorkingDir)
		discoveredHere := discovered[dir]

		sessions := []domain.Session{}
		// Retain a session only while a registered provider could still confirm it
		// but did not report this cycle, and no fresh discovery supersedes it. A
		// session whose provider is not in the registry is dropped: nothing can
		// ever confirm it or route to it.
		for _, sess := range ws.Sessions {
			// A starting placeholder is retained unconditionally: no provider reports it
			// (it stands for a session that does not exist yet), so the rule above would
			// drop it on the first cycle after a create — mid-build, which is exactly
			// when it is the only thing the user has to look at.
			// Service.finishCreateSession is what ends its life — except when the create
			// fails, where the same row stays on as the `error` record of the attempt and
			// only a removal by the user clears it. Because it is never persisted a daemon
			// restart cannot leave one behind either way.
			//
			// This covers the placeholders that existed when the snapshot was taken. One
			// that appears *while* the providers are being asked is invisible here, and is
			// carried over by the store instead (store.retainInFlightLocked) — the only
			// place that can see it, since it is the only one holding the live state.
			if IsStartingPlaceholder(sess) {
				sessions = append(sessions, sess)
				continue
			}
			if _, known := r.registry.SessionProvider(sess.Provider); !known {
				continue
			}
			if refreshed[sess.Provider] || superseded[sess.ID] {
				continue
			}
			sessions = append(sessions, sess)
		}
		if dir != "" {
			sessions = append(sessions, discoveredHere...)
			matchedDirs[dir] = true
		}
		ws.Sessions = sessions
		result = append(result, ws)
	}

	// Pass 2: discovered sessions whose directory matches no known workstream
	// get a workstream adopted for them.
	for dir, sessions := range discovered {
		if dir == "" || matchedDirs[dir] {
			continue
		}
		result = append(result, adoptedWorkstream(dir, sessions))
	}

	// Derived last, so it sees each workstream's final session set.
	if r.annotate != nil {
		r.annotate(project.ID, result)
	}

	if err := r.store.ReplaceWorkstreams(project.ID, result); err != nil {
		slog.Warn("projecting workstreams failed", "project", project.ID, "error", err)
	}
}

// adoptedWorkstream builds a workstream record for sessions discovered in a
// directory the daemon had no workstream for. No workstream provider owns it,
// so it carries no CreatorData and its directory is never torn down.
func adoptedWorkstream(dir string, sessions []domain.Session) domain.Workstream {
	provider := ""
	if len(sessions) > 0 {
		provider = sessions[0].Provider
	}
	return domain.Workstream{
		ID:         adoptedWorkstreamID(dir),
		Name:       filepath.Base(dir),
		Provider:   provider,
		WorkingDir: dir,
		Sessions:   sessions,
	}
}

// adoptedWorkstreamID derives a stable ID from the working directory so an
// adopted workstream keeps the same identity across reconcile cycles.
func adoptedWorkstreamID(dir string) string {
	sum := sha1.Sum([]byte(dir))
	return fmt.Sprintf("disc-%x", sum[:8])
}

func cleanDir(dir string) string {
	if dir == "" {
		return ""
	}
	return filepath.Clean(dir)
}

// Run does an initial reconcile, starts any Watcher providers, and then keeps
// the projection fresh on Sink.Changed and (when configured) a poll ticker. It
// blocks until ctx is cancelled.
func (r *Reconciler) Run(ctx context.Context) {
	for _, p := range r.registry.SessionProviders() {
		if w, ok := p.(Watcher); ok {
			go w.Watch(ctx, watcherSink{reconciler: r, providerID: p.ID()})
		}
	}
	r.Reconcile(ctx)

	var tick <-chan time.Time
	if r.poll > 0 {
		ticker := time.NewTicker(r.poll)
		defer ticker.Stop()
		tick = ticker.C
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.trigger:
			r.reconcileOnly(ctx, r.takeDirty())
		case <-tick:
			r.Reconcile(ctx)
		}
	}
}
