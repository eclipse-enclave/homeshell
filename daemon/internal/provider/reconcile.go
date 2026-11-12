package provider

import (
	"context"
	"crypto/sha1"
	"fmt"
	"log/slog"
	"maps"
	"path/filepath"
	"slices"
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

// adoptedIDPrefix marks a workstream the reconciler adopted for sessions found
// in a folder no workstream claimed (adoptedWorkstreamID).
const adoptedIDPrefix = "disc-"

// isAdopted reports whether workstream is a workstream the reconciler adopted. It does
// not claim its folder: a folder that turns out to be an untracked worktree is
// suggested instead, and the adopted workstream dropped.
func isAdopted(workstream domain.Workstream) bool {
	return strings.HasPrefix(workstream.ID, adoptedIDPrefix)
}

// Reconciler keeps the store's workstreams in sync with session-provider state:
// it attaches each discovered session to the workstream with a matching
// WorkingDir, adopting a workstream when none matches. It implements Sink so
// Watcher-capable providers can push change notifications.
type Reconciler struct {
	store    *store.Store
	registry *Registry
	poll     time.Duration
	// refreshEvery is how often a Refresher's workstreams are checked when it
	// names no interval of its own.
	refreshEvery time.Duration
	trigger      chan struct{}
	dirtyMu      sync.Mutex
	dirty        dirtySet
	// annotate fills the derived per-projection fields of a project's workstreams
	// and sessions (open modes, materialization, restartability). The owning
	// Service supplies it; nil leaves them empty.
	annotate func(projectID string, workstreams []domain.Workstream)
	// projects holds what passes keep of each project (projectState).
	projectsMu sync.Mutex
	projects   map[string]*projectState
	// cachedListings caches each FolderSurveyor's folder listing of each project.
	cachedListings *listingCache
	// retire starts removing a workstream whose folder vanished, unless a
	// removal of it is under way. Passes keep it listed until the removal drops
	// its record. The owning Service supplies it; nil leaves it listed.
	retire func(ctx context.Context, ref WorkstreamRef)
}

// projectState is what the reconciler keeps of a project between passes.
type projectState struct {
	// passLock serializes projection passes over the project: each ends in
	// ReplaceProjection, which writes a whole list derived from its own read, so
	// two interleaved passes would have the later revert the earlier. Per project
	// rather than global, so a reconcile triggered by an API call does not queue
	// behind an unrelated project's provider calls.
	passLock sync.Mutex
	// firstSeen is when a pass first saw each of the project's workstreams, by
	// ID; held under passLock.
	firstSeen map[string]time.Time
}

// sight stamps the workstreams a pass sees for the first time with now, drops
// those gone, and returns when each was first seen. Called with passLock held.
func (ps *projectState) sight(workstreams []domain.Workstream) map[string]time.Time {
	now := time.Now()
	present := map[string]bool{}
	for _, workstream := range workstreams {
		present[workstream.ID] = true
		if _, seen := ps.firstSeen[workstream.ID]; !seen {
			ps.firstSeen[workstream.ID] = now
		}
	}
	maps.DeleteFunc(ps.firstSeen, func(workstreamID string, _ time.Time) bool { return !present[workstreamID] })
	return ps.firstSeen
}

// projectState returns what passes keep of a project.
func (r *Reconciler) projectState(projectID string) *projectState {
	r.projectsMu.Lock()
	defer r.projectsMu.Unlock()
	state, ok := r.projects[projectID]
	if !ok {
		state = &projectState{firstSeen: map[string]time.Time{}}
		r.projects[projectID] = state
	}
	return state
}

// NewReconciler builds a reconciler. A poll of 0 disables periodic polling
// (the reconciler then refreshes only on explicit calls and Sink.Changed).
func NewReconciler(st *store.Store, reg *Registry, poll time.Duration) *Reconciler {
	return &Reconciler{
		store:          st,
		registry:       reg,
		poll:           poll,
		refreshEvery:   2 * time.Second,
		trigger:        make(chan struct{}, 1),
		projects:       map[string]*projectState{},
		cachedListings: newListingCache(),
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
		r.reconcileProjectOnly(ctx, projectRefOf(project), only)
	}
	r.store.RetryUnsavedWrite()
}

// ReconcileProject reconciles a single project by ID.
func (r *Reconciler) ReconcileProject(ctx context.Context, projectID string) error {
	project, err := r.store.Project(projectID)
	if err != nil {
		return err
	}
	r.reconcileProjectOnly(ctx, projectRefOf(project), dirtySet{all: true})
	return nil
}

// reconcileProjectOnly lists the project's folders, projects it, then has the
// workstreams whose folder vanished retired. Why around the pass: listing runs
// git and retiring is service work, and neither must hold up the pass lock.
// Listing first also means a workstream the pass sees for the first time is
// stamped after the listing began, so that listing cannot vanish it.
func (r *Reconciler) reconcileProjectOnly(ctx context.Context, project ProjectRef, only dirtySet) {
	listings, listErr := r.listings(ctx, project, r.listing)
	vanished := r.projectPass(ctx, project, only, listings, listErr)
	if r.retire == nil {
		return
	}
	for _, workstreamID := range vanished {
		r.retire(ctx, WorkstreamRef{ProjectID: project.ID, WorkstreamID: workstreamID})
	}
}

// discovery is what the session providers reported in one pass.
type discovery struct {
	// sessions are the sessions reported, by the cleaned folder they run in.
	sessions map[string][]domain.Session
	// refreshed are the providers that reported, so one that hit a transient
	// error does not flush its sessions from the store.
	refreshed map[string]bool
	// superseded are the IDs of sessions reported from a folder: a fresh report
	// supersedes any retained session with the same ID, wherever it sits, so an
	// ID cannot land in the result set twice. Empty-dir reports are excluded:
	// neither pass attaches them anywhere, so letting them supersede would drop
	// the stored session outright.
	superseded map[string]bool
}

// discover gathers the sessions every available session provider in only
// observes in the project.
func (r *Reconciler) discover(ctx context.Context, project ProjectRef, only dirtySet) discovery {
	found := discovery{
		sessions:   map[string][]domain.Session{},
		refreshed:  map[string]bool{},
		superseded: map[string]bool{},
	}
	for _, sessionProvider := range r.registry.SessionProviders() {
		if !only.has(sessionProvider.ID()) {
			continue
		}
		available, err := sessionProvider.Available(ctx)
		if err != nil {
			slog.Warn("provider availability check failed", "provider", sessionProvider.ID(), "error", err)
			continue
		}
		if !available {
			// Its sessions are gone, so count it as refreshed with none.
			found.refreshed[sessionProvider.ID()] = true
			continue
		}
		var sessions []DiscoveredSession
		if watched, ok := sessionProvider.(WatchedDiscoverer); !only.all && ok {
			sessions, err = watched.DiscoverWatchedSessions(ctx, project)
		} else {
			sessions, err = sessionProvider.DiscoverSessions(ctx, project)
		}
		if err != nil {
			slog.Warn("provider failed to discover sessions",
				"provider", sessionProvider.ID(), "project", project.ID, "error", err)
			continue
		}
		found.refreshed[sessionProvider.ID()] = true
		for _, discovered := range sessions {
			session := discovered.Session
			session.Provider = sessionProvider.ID()
			dir := cleanDir(discovered.WorkingDir)
			found.sessions[dir] = append(found.sessions[dir], session)
			if dir != "" {
				found.superseded[session.ID] = true
			}
		}
	}
	return found
}

// projectPass is one projection pass over a project, against its listings
// (listErr: one failed). It returns the IDs of the workstreams whose folder
// vanished; they stay listed, with the sessions there, until their removal
// drops them.
func (r *Reconciler) projectPass(ctx context.Context, project ProjectRef, only dirtySet,
	listings surveyorListings, listErr error) []string {
	state := r.projectState(project.ID)
	state.passLock.Lock()
	defer state.passLock.Unlock()

	found := r.discover(ctx, project, only)

	// Discovery can be slow and workstreams land concurrently, so re-read the
	// store: ReplaceProjection below writes the whole list, and deriving it
	// from a pre-discovery snapshot would erase anything added in between.
	// ReplaceProjection is handed this read, so it keeps what changes after it
	// while the pass refreshes and annotates (store.keepChangesSinceRead).
	projects := r.store.Projects()
	index := slices.IndexFunc(projects, func(candidate domain.Project) bool { return candidate.ID == project.ID })
	if index < 0 {
		r.forgetProject(project.ID) // removed meanwhile
		return nil
	}
	stored := projects[index]
	firstSeen := state.sight(stored.Workstreams)

	// A failed listing is no empty listing: nothing vanishes, and the stored
	// suggestions stay, unless claimed since.
	claimed := claimedFolders(projects)
	var survey folderSurvey
	if listErr != nil {
		survey.untracked = unclaimedSuggestions(stored.UntrackedWorktrees, claimed)
	} else {
		survey = listings.survey(project, stored.Workstreams, claimed, firstSeen)
	}
	if survey.stale {
		r.cachedListings.forget(project.ID)
	}
	suggested, unhidden := r.hideSessions(survey.untracked, stored, found)

	result := []domain.Workstream{}
	var vanished []string
	matchedDirs := maps.Clone(suggested)

	// Pass 1: existing workstreams keep their identity; re-attach sessions by
	// matching working directory. One working in a folder suggested until now
	// takes the sessions hidden there; one adopted in a folder suggested now
	// gave its sessions to the suggestion. One whose folder vanished stays,
	// and holds its folder's sessions, until its removal is done.
	for _, workstream := range stored.Workstreams {
		if isPlaceholder(workstream) {
			result = append(result, workstream)
			continue
		}
		dir := cleanDir(workstream.WorkingDir)
		if isAdopted(workstream) && suggested[dir] {
			continue
		}
		sessions := r.retainedSessions(slices.Concat(workstream.Sessions, unhidden[dir]), found)
		if dir != "" {
			sessions = append(sessions, found.sessions[dir]...)
			matchedDirs[dir] = true
		}
		workstream.Sessions = sessions
		result = append(result, workstream)
		if survey.vanished[workstream.ID] {
			vanished = append(vanished, workstream.ID)
		}
	}

	// Pass 2: discovered sessions whose directory matches no known workstream
	// or suggestion get a workstream adopted for them.
	for dir, sessions := range found.sessions {
		if dir == "" || matchedDirs[dir] {
			continue
		}
		result = append(result, adoptedWorkstream(dir, sessions))
	}

	r.refresh(ctx, project, result)

	// Derived last, so it sees each workstream's final session set.
	if r.annotate != nil {
		r.annotate(project.ID, result)
	}

	if err := r.store.ReplaceProjection(project.ID, stored.Workstreams, result, survey.untracked); err != nil {
		slog.Warn("projecting workstreams failed", "project", project.ID, "error", err)
	}
	return vanished
}

// retainedSessions are the stored sessions a pass keeps: only while a
// registered provider could still confirm one but did not report in found,
// and no fresh discovery supersedes it. A session whose provider is not in the
// registry is dropped: nothing can ever confirm it or route to it.
func (r *Reconciler) retainedSessions(stored []domain.Session, found discovery) []domain.Session {
	sessions := []domain.Session{}
	for _, session := range stored {
		// A starting placeholder is retained unconditionally: no provider reports it
		// (it stands for a session that does not exist yet), so the retention rule
		// would drop it on the first cycle after a create — mid-build, which is
		// exactly when it is the only thing the user has to look at.
		// Service.finishCreateSession is what ends its life — except when the create
		// fails, where the same row stays on as the `error` record of the attempt and
		// only a removal by the user clears it. Because it is never persisted a daemon
		// restart cannot leave one behind either way.
		//
		// This covers the placeholders that existed when the snapshot was taken. One
		// that appears *while* the providers are being asked is invisible here, and is
		// carried over by the store instead (store.retainInFlightLocked) — the only
		// place that can see it, since it is the only one holding the live state.
		if IsStartingPlaceholder(session) {
			sessions = append(sessions, session)
			continue
		}
		if _, known := r.registry.SessionProvider(session.Provider); !known {
			continue
		}
		if found.refreshed[session.Provider] || found.superseded[session.ID] {
			continue
		}
		sessions = append(sessions, session)
	}
	return sessions
}

// refresh has each workstream's provider bring it up to date (Refresher). A
// name a refresh gives that another workstream in the project already answers
// to, by the store's rule (store.NameTaken), is not taken: the workstream keeps
// the name it had.
func (r *Reconciler) refresh(ctx context.Context, project ProjectRef, workstreams []domain.Workstream) {
	for i, workstream := range workstreams {
		if isPlaceholder(workstream) {
			continue
		}
		workstreamProvider, ok := r.registry.WorkstreamProvider(workstream.Provider)
		if !ok {
			continue
		}
		refresher, ok := workstreamProvider.(Refresher)
		if !ok {
			continue
		}
		refreshed := refresher.Refresh(ctx, project, workstream)
		if refreshed.Name != workstream.Name {
			if _, taken := store.NameTaken(workstreams, refreshed.Name, workstream.ID); taken {
				refreshed.Name, refreshed.CreatorData = workstream.Name, workstream.CreatorData
			}
		}
		workstreams[i] = refreshed
	}
}

// branchMoved reports whether a refresh would move any of providerID's
// workstreams to another branch. The branch is what moves a name too, so it is
// all that is compared.
func (r *Reconciler) branchMoved(ctx context.Context, projects []domain.Project, providerID string,
	refresher Refresher) bool {
	for _, project := range projects {
		ref := projectRefOf(project)
		for _, workstream := range project.Workstreams {
			if workstream.Provider != providerID || isPlaceholder(workstream) {
				continue
			}
			if refresher.Refresh(ctx, ref, workstream).Branch != workstream.Branch {
				return true
			}
		}
	}
	return false
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
	return fmt.Sprintf("%s%x", adoptedIDPrefix, sum[:8])
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
	for _, workstreamProvider := range r.registry.WorkstreamProviders() {
		refresher, ok := workstreamProvider.(Refresher)
		if !ok {
			continue
		}
		providerID := workstreamProvider.ID()
		surveyor, surveys := workstreamProvider.(FolderSurveyor)
		sink := watcherSink{reconciler: r, providerID: providerID}
		go WatchLoop(ctx, sink, refresher.RefreshInterval, r.refreshEvery, func(ctx context.Context) bool {
			projects := r.store.Projects()
			// Both asked every tick: the folder listings are refreshed on each.
			changed := surveys && r.foldersChanged(ctx, projects, surveyor)
			return r.branchMoved(ctx, projects, providerID, refresher) || changed
		})
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
