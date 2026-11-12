package provider

import (
	"context"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
)

// listingKey names one surveyor's listing of one project.
type listingKey struct {
	providerID string
	projectID  string
}

// folderListing is a surveyor's listing of a project's folders.
type folderListing struct {
	folders []Folder
	// takenAt is when the listing began.
	takenAt time.Time
}

// listingCache holds each surveyor's latest listing of each project, so a
// pass lists only when none is held, and which listings' latest attempt
// failed, so a project out of reach warns once per failure streak.
type listingCache struct {
	mu      sync.Mutex
	entries map[listingKey]listingEntry
}

// listingEntry is what the cache holds of one listing.
type listingEntry struct {
	// listing is the latest that succeeded; a zero takenAt while none did.
	listing folderListing
	failing bool
}

func newListingCache() *listingCache {
	return &listingCache{entries: map[listingKey]listingEntry{}}
}

// get returns the listing held for key.
func (c *listingCache) get(key listingKey) (folderListing, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	listing := c.entries[key].listing
	return listing, !listing.takenAt.IsZero()
}

// put holds listing for key, unless the one held began later.
func (c *listingCache) put(key listingKey, listing folderListing) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.entries[key]
	if listing.takenAt.After(entry.listing.takenAt) {
		entry.listing = listing
	}
	entry.failing = false
	c.entries[key] = entry
}

// fail records that the latest attempt at key's listing failed, and reports
// whether that starts a failure streak.
func (c *listingCache) fail(key listingKey) (streakStarts bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.entries[key]
	streakStarts = !entry.failing
	entry.failing = true
	c.entries[key] = entry
	return streakStarts
}

// forget drops what is held of a project's listings: its next pass lists anew.
func (c *listingCache) forget(projectID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	maps.DeleteFunc(c.entries, func(key listingKey, _ listingEntry) bool { return key.projectID == projectID })
}

// retain drops the listings of every project but projects.
func (c *listingCache) retain(projects []domain.Project) {
	listed := map[string]bool{}
	for _, project := range projects {
		listed[project.ID] = true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	maps.DeleteFunc(c.entries, func(key listingKey, _ listingEntry) bool { return !listed[key.projectID] })
}

// forgetFolders drops what is cached of a project's listings, so a worktree
// deleted is not suggested from a listing taken before, and one kept is
// suggested on the next refresh.
func (r *Reconciler) forgetFolders(projectID string) {
	r.cachedListings.forget(projectID)
}

// forgetProject drops all the reconciler keeps of a removed project.
func (r *Reconciler) forgetProject(projectID string) {
	r.cachedListings.forget(projectID)
	r.projectsMu.Lock()
	defer r.projectsMu.Unlock()
	delete(r.projects, projectID)
}

// resurvey asks for a pass of every project that surveys its folders again,
// as a refresh tick that saw a listing change does; the session providers are
// not asked anew. For changes that claim or free folders without changing a
// listing: a removal that kept the files, a project added or removed.
func (r *Reconciler) resurvey() {
	for _, workstreamProvider := range r.registry.WorkstreamProviders() {
		if _, ok := workstreamProvider.(FolderSurveyor); ok {
			r.markDirty(workstreamProvider.ID())
		}
	}
}

// list has surveyor list the project's folders, and holds the listing. A
// failure is logged as a warning only when it starts a failure streak.
func (r *Reconciler) list(ctx context.Context, project ProjectRef, surveyor FolderSurveyor) (folderListing, error) {
	key := listingKey{providerID: surveyor.ID(), projectID: project.ID}
	takenAt := time.Now()
	folders, err := surveyor.ListFolders(ctx, project)
	if err != nil {
		log := slog.Debug
		if r.cachedListings.fail(key) {
			log = slog.Warn
		}
		log("listing folders failed", "provider", key.providerID, "project", project.ID, "error", err)
		return folderListing{}, err
	}
	listing := folderListing{folders: folders, takenAt: takenAt}
	r.cachedListings.put(key, listing)
	return listing, nil
}

// listing returns the listing of the project by surveyor held, or lists it
// when none is.
func (r *Reconciler) listing(ctx context.Context, project ProjectRef, surveyor FolderSurveyor) (folderListing, error) {
	if listing, held := r.cachedListings.get(listingKey{providerID: surveyor.ID(), projectID: project.ID}); held {
		return listing, nil
	}
	return r.list(ctx, project, surveyor)
}

// foldersChanged has surveyor list each git project's folders anew, holding
// the listings, and reports whether one differs from the listing held before,
// or none was held. Workstreams added or removed project themselves; a listing
// that fails changes nothing, as the pass keeps using the one held.
func (r *Reconciler) foldersChanged(ctx context.Context, projects []domain.Project, surveyor FolderSurveyor) bool {
	// A listing a pass put for a project removed meanwhile is dropped here.
	r.cachedListings.retain(projects)
	changed := false
	for _, project := range projects {
		if !project.GitBacked {
			continue
		}
		previous, held := r.cachedListings.get(listingKey{providerID: surveyor.ID(), projectID: project.ID})
		listing, err := r.list(ctx, projectRefOf(project), surveyor)
		if err != nil {
			continue
		}
		changed = changed || !held || !slices.Equal(previous.folders, listing.folders)
	}
	return changed
}

// surveyorListing is one FolderSurveyor's listing of a project.
type surveyorListing struct {
	surveyor FolderSurveyor
	folderListing
}

// surveyorListings are each FolderSurveyor's listing of one project.
type surveyorListings []surveyorListing

// lister gets a surveyor's listing of a project: Reconciler.listing the one
// held, Reconciler.list a fresh one.
type lister func(ctx context.Context, project ProjectRef, surveyor FolderSurveyor) (folderListing, error)

// listings are each FolderSurveyor's listing of a git project, got by list,
// none outside git, or the error of the first that failed.
func (r *Reconciler) listings(ctx context.Context, project ProjectRef, list lister) (surveyorListings, error) {
	if !project.GitBacked {
		return nil, nil
	}
	var listings surveyorListings
	for _, workstreamProvider := range r.registry.WorkstreamProviders() {
		surveyor, ok := workstreamProvider.(FolderSurveyor)
		if !ok {
			continue
		}
		listing, err := list(ctx, project, surveyor)
		if err != nil {
			return nil, err
		}
		listings = append(listings, surveyorListing{surveyor: surveyor, folderListing: listing})
	}
	return listings, nil
}

// untrackedSuggestion is a folder a surveyor suggests, and that surveyor.
type untrackedSuggestion struct {
	surveyor FolderSurveyor
	folder   domain.UntrackedWorktree
}

// untracked are the folders the project's surveyors suggest now, none outside
// git. Listed anew, not from the cache: a worktree removed since the last
// refresh is suggested no more. A user waits on it, so the listing gets a
// create's checks' bound rather than a refresh's.
func (r *Reconciler) untracked(ctx context.Context, project domain.Project) ([]untrackedSuggestion, error) {
	ref := projectRefOf(project)
	ctx, cancel := context.WithTimeout(ctx, createCheckTimeout)
	defer cancel()
	listings, err := r.listings(ctx, ref, r.list)
	if err != nil {
		return nil, err
	}
	claimed := claimedFolders(r.store.Projects())
	var suggestions []untrackedSuggestion
	for _, listing := range listings {
		for _, folder := range listing.surveyor.Survey(ref, listing.folders, project.Workstreams, claimed).Untracked {
			suggestions = append(suggestions, untrackedSuggestion{surveyor: listing.surveyor, folder: folder})
		}
	}
	return suggestions, nil
}

// folderSurvey is what a pass makes of a project's listings.
type folderSurvey struct {
	// untracked are the folders to suggest, no sessions attached yet.
	untracked []domain.UntrackedWorktree
	// vanished are the IDs of the workstreams whose folder is gone.
	vanished map[string]bool
	// stale says a listing missed a workstream first seen after it began. It
	// may predate that workstream's worktree, so it vanishes nothing, and is
	// to be listed anew.
	stale bool
}

// survey has each surveyor survey its listing against the project's
// workstreams and the folders claimed. A workstream vanishes only from a
// listing begun after firstSeen has it. Why: a listing older than a worktree
// workstream does not name its worktree.
func (listings surveyorListings) survey(project ProjectRef, workstreams []domain.Workstream, claimed []string,
	firstSeen map[string]time.Time) folderSurvey {
	result := folderSurvey{vanished: map[string]bool{}}
	for _, listing := range listings {
		survey := listing.surveyor.Survey(project, listing.folders, workstreams, claimed)
		result.untracked = append(result.untracked, survey.Untracked...)
		for _, workstreamID := range survey.Vanished {
			if listing.takenAt.After(firstSeen[workstreamID]) {
				result.vanished[workstreamID] = true
			} else {
				result.stale = true
			}
		}
	}
	return result
}

// unclaimedSuggestions are the stored suggestions, sessions not attached yet,
// but those whose folder something claims since, as a workstream just added
// there does: a session there must not be listed by both. Folders compare as
// the surveyors compare them, symlinks resolved.
func unclaimedSuggestions(stored []domain.UntrackedWorktree, claimed []string) []domain.UntrackedWorktree {
	claimedFolders := newFolderSet(claimed)
	kept := []domain.UntrackedWorktree{}
	for _, folder := range stored {
		if !claimedFolders.has(folder.Path) {
			kept = append(kept, domain.UntrackedWorktree{Name: folder.Name, Branch: folder.Branch, Path: folder.Path})
		}
	}
	return kept
}

// claimedFolders are the folders already held: every project's, and every
// workstream's. Adopted and placeholder workstreams hold none.
func claimedFolders(projects []domain.Project) []string {
	var claimed []string
	for _, project := range projects {
		claimed = append(claimed, project.Path)
		for _, workstream := range project.Workstreams {
			if workstream.WorkingDir != "" && !isPlaceholder(workstream) && !isAdopted(workstream) {
				claimed = append(claimed, workstream.WorkingDir)
			}
		}
	}
	return claimed
}

// hideSessions gives each suggestion in untracked the sessions hidden in its
// folder: those stored hidden there, those of a workstream adopted there
// before it was listed, and those discovered there now. Matched by cleaned
// folder, as workstreams are, so a session counted there is one that attaches
// once the folder is added. It returns the suggested folders, and the
// sessions stored hidden on folders suggested no more, for a workstream
// working there to take.
func (r *Reconciler) hideSessions(untracked []domain.UntrackedWorktree, stored domain.Project,
	found discovery) (suggested map[string]bool, unhidden map[string][]domain.Session) {
	suggested = map[string]bool{}
	for _, folder := range untracked {
		suggested[cleanDir(folder.Path)] = true
	}
	unhidden = map[string][]domain.Session{}
	for _, folder := range stored.UntrackedWorktrees {
		dir := cleanDir(folder.Path)
		unhidden[dir] = append(unhidden[dir], folder.Sessions...)
	}
	for _, workstream := range stored.Workstreams {
		if dir := cleanDir(workstream.WorkingDir); isAdopted(workstream) && suggested[dir] {
			unhidden[dir] = append(unhidden[dir], workstream.Sessions...)
		}
	}
	for i := range untracked {
		dir := cleanDir(untracked[i].Path)
		untracked[i].Sessions = append(r.retainedSessions(unhidden[dir], found), found.sessions[dir]...)
		delete(unhidden, dir)
	}
	return suggested, unhidden
}
