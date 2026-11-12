package enclave

import (
	"context"
	"slices"
	"strconv"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/provider/workstream"
)

// The inventory is what `enclave ps` lists for every project in one periodic check. Each directory is listed once
// per check, however many projects reach it, and what the check learns from it (drift, CLI sessions) shares it.

// inventoryIntervalEnv shortens the check interval, in milliseconds, for end-to-end tests.
const inventoryIntervalEnv = "HOMESHELL_ENCLAVE_INVENTORY_MS"

// inventory is one check's listing of every project.
type inventory struct {
	listings []projectListing
	// projects is the store snapshot the listing was taken for, which ownership is matched against.
	projects []domain.Project
	// generations is each record's generation before the listing began; one written since (changedSince) may
	// say what the listing does not show yet, and wins.
	generations map[string]uint64
}

// changedSince reports whether a container's record was written, added or removed after listed began; called with
// b.mu held.
func (b *hostBridges) changedSince(listed inventory, name string) bool {
	return b.guard(name).generation != listed.generations[name]
}

// projectListing is what listing a project's directories found: its containers, deduplicated by name, or the
// error of a directory that could not be listed.
type projectListing struct {
	project    domain.Project
	containers []Container
	err        error
}

// inventoryDirs are the directories to list for a project: its folder and the folder of each of its copy
// workstreams, whose containers `enclave ps` does not resolve to the project folder.
func inventoryDirs(project domain.Project) []string {
	dirs := []string{project.Path}
	for _, workstreamEntry := range project.Workstreams {
		if workstreamEntry.StrategyID != string(workstream.Copy) || workstreamEntry.WorkingDir == "" {
			continue
		}
		if !slices.ContainsFunc(dirs, func(dir string) bool { return domain.SameFolder(dir, workstreamEntry.WorkingDir) }) {
			dirs = append(dirs, workstreamEntry.WorkingDir)
		}
	}
	return dirs
}

// listInventory lists every project's directories, each directory once.
func (p *Provider) listInventory(ctx context.Context) inventory {
	type dirListing struct {
		containers []Container
		err        error
	}
	byDir := map[string]dirListing{}
	listed := inventory{generations: map[string]uint64{}}
	p.bridges.mu.Lock()
	for name, guard := range p.bridges.guards {
		listed.generations[name] = guard.generation
	}
	p.bridges.mu.Unlock()
	listed.projects = p.storeProjects()
	for _, project := range listed.projects {
		listing := projectListing{project: project}
		seen := map[string]bool{}
		for _, dir := range inventoryDirs(project) {
			result, done := byDir[dir]
			if !done {
				result.containers, result.err = p.cli.ListSessions(ctx, dir)
				byDir[dir] = result
			}
			if result.err != nil {
				listing.err = result.err
				continue
			}
			for _, container := range result.containers {
				if !seen[container.Name] {
					seen[container.Name] = true
					listing.containers = append(listing.containers, container)
				}
			}
		}
		listed.listings = append(listed.listings, listing)
	}
	return listed
}

// checkInventory lists the containers once and works out from that what changed, reporting whether it did: the
// CLI sessions to adopt or end, and the sessions out of date.
func (p *Provider) checkInventory(ctx context.Context) bool {
	listed := p.listInventory(ctx)
	adopted := p.adoptListed(ctx, listed)
	outdated := p.outdatedFrom(ctx, listed)
	return adopted || outdated
}

// inventoryInterval is how often the watch loop checks.
func (p *Provider) inventoryInterval() time.Duration {
	if p.inventoryEvery > 0 {
		return p.inventoryEvery
	}
	return driftCheckInterval
}

// inventoryIntervalFrom reads the interval from the environment's value in milliseconds; a missing or invalid
// one is 0, which falls back to driftCheckInterval.
func inventoryIntervalFrom(value string) time.Duration {
	milliseconds, err := strconv.Atoi(value)
	if err != nil || milliseconds <= 0 {
		return 0
	}
	return time.Duration(milliseconds) * time.Millisecond
}
