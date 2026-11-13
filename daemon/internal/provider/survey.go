package provider

import (
	"context"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/git"
)

// Folder is one folder of a FolderSurveyor's listing.
type Folder struct {
	Path   string
	Branch string // the branch checked out there; "" when none
	// Missing says the folder is not there, deleted or out of reach: there is
	// nothing to suggest adding.
	Missing bool
	// Gone says the folder was deleted, though the listing still names it: its
	// workstream vanishes. A folder only out of reach is Missing, not Gone.
	Gone bool
}

// Survey is what a FolderSurveyor makes of a listing: the folders no one
// claims, suggested as workstreams, and the IDs of its workstreams whose
// folder is gone.
type Survey struct {
	Untracked []domain.UntrackedWorktree
	Vanished  []string
}

// FolderSurveyor is a workstream provider whose workstreams sit in folders something outside HomeShell
// also tracks (a repository's worktrees), which appear and vanish on their own.
type FolderSurveyor interface {
	WorkstreamProvider
	// ListFolders is the provider's one external query per project per refresh. A caller's deadline
	// bounds it; without one, the provider bounds it as a refresh's.
	ListFolders(ctx context.Context, project ProjectRef) ([]Folder, error)
	// Survey is pure apart from resolving paths: which listed folders no one claims, and which of the
	// provider's own workstreams in workstreams lost their folder. claimed are the folders any project
	// or workstream already holds.
	Survey(project ProjectRef, folders []Folder, workstreams []domain.Workstream, claimed []string) Survey
	// TrackFolder makes a workstream of an untracked folder, creating nothing on disk, under a
	// name nameTaken does not report taken.
	TrackFolder(folder domain.UntrackedWorktree, nameTaken func(name string) bool) domain.Workstream
}

// folderSet is a set of folders compared with symlinks resolved (git.SamePath),
// each path resolved once: resolving one stats each of its ancestors.
type folderSet struct {
	resolved map[string]string
	members  map[string]bool
}

// newFolderSet is the set of the folders in each of folderLists.
func newFolderSet(folderLists ...[]string) *folderSet {
	set := &folderSet{resolved: map[string]string{}, members: map[string]bool{}}
	for _, folders := range folderLists {
		for _, folder := range folders {
			set.members[set.realPath(folder)] = true
		}
	}
	return set
}

// realPath is folder with symlinks resolved (git.RealPath).
func (set *folderSet) realPath(folder string) string {
	resolvedPath, ok := set.resolved[folder]
	if !ok {
		resolvedPath = git.RealPath(folder)
		set.resolved[folder] = resolvedPath
	}
	return resolvedPath
}

// has reports whether folder is one of the set's.
func (set *folderSet) has(folder string) bool {
	return set.members[set.realPath(folder)]
}
