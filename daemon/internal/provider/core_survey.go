package provider

import (
	"context"
	"fmt"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/git"
	wsfs "github.com/eclipsesource/homeshell/daemon/internal/provider/workstream"
)

// ListFolders lists the linked worktrees of the project's repository: every
// one git lists but its first, the main worktree (see listingContext for its
// bound).
func (c *core) ListFolders(ctx context.Context, project ProjectRef) ([]Folder, error) {
	if !project.GitBacked {
		return nil, fmt.Errorf("project %q is not a git repository", project.Path)
	}
	ctx, cancel := c.listingContext(ctx)
	defer cancel()
	worktrees, err := git.LinkedWorktrees(ctx, project.Path)
	if err != nil {
		return nil, err
	}
	folders := make([]Folder, 0, len(worktrees))
	for _, worktree := range worktrees {
		folders = append(folders, Folder{
			Path: worktree.Path, Branch: worktree.Branch, Missing: worktree.Missing, Gone: worktree.Deleted,
		})
	}
	return folders, nil
}

// listingContext bounds a listing by the caller's deadline where it set one, as
// a user's Add does. Without one it is a refresh's, bounded like a terminal
// read. Why: a refresh lists every git project each tick, and a slow one must
// not hold up the others; a user waits on an Add, which may take longer.
func (c *core) listingContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, bounded := ctx.Deadline(); bounded {
		return context.WithCancel(ctx)
	}
	return c.CaptureContext(ctx)
}

// Survey suggests each listed worktree whose folder is there, not claimed and
// not being created, named after its branch or, detached, its folder. A
// worktree workstream of the core's vanishes once its folder is unlisted or
// gone; one only out of reach is still listed, and stays.
func (c *core) Survey(_ ProjectRef, folders []Folder, workstreams []domain.Workstream, claimed []string) Survey {
	c.claimMu.Lock()
	inFlight := make([]string, 0, len(c.claimed))
	for dir := range c.claimed {
		inFlight = append(inFlight, dir)
	}
	c.claimMu.Unlock()

	taken := newFolderSet(claimed, inFlight)
	var survey Survey
	present := map[string]bool{}
	for _, folder := range folders {
		if folder.Gone {
			continue
		}
		present[taken.realPath(folder.Path)] = true
		if folder.Missing || taken.has(folder.Path) {
			continue
		}
		survey.Untracked = append(survey.Untracked, domain.UntrackedWorktree{
			Name: coreWorkstreamName(folder.Path, folder.Branch), Branch: folder.Branch, Path: folder.Path,
		})
	}
	for _, workstream := range workstreams {
		if workstream.Provider != CoreProviderID || isPlaceholder(workstream) ||
			wsfs.Strategy(parseCreatorData(workstream).Strategy) != wsfs.Worktree {
			continue
		}
		if !present[taken.realPath(workstream.WorkingDir)] {
			survey.Vanished = append(survey.Vanished, workstream.ID)
		}
	}
	return survey
}

// TrackFolder makes a worktree workstream of an untracked worktree, named after
// it and numbered past the names nameTaken reports taken (nil: none), a
// numbered name capped like a managed directory's. Remove then treats it like
// one the core created.
func (c *core) TrackFolder(folder domain.UntrackedWorktree, nameTaken func(name string) bool) domain.Workstream {
	name := folder.Name
	for number := 2; nameTaken != nil && nameTaken(name); number++ {
		name = capWithSuffix(folder.Name, fmt.Sprintf("-%d", number))
	}
	return domain.Workstream{
		Name:        name,
		StrategyID:  string(wsfs.Worktree),
		Branch:      folder.Branch,
		WorkingDir:  folder.Path,
		CreatorData: marshalCreatorData(coreCreatorData{Strategy: string(wsfs.Worktree)}),
		Sessions:    []domain.Session{},
	}
}
