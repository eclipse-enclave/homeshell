package git

import (
	"context"
	"strconv"
	"strings"
)

// maxRefs caps how many local branches, and separately how many other refs,
// ListRefs returns, so a repository with thousands of tags stays cheap to list
// and to offer.
const maxRefs = 200

// Ref is a named starting point in a repository.
type Ref struct {
	// Name is the shortest name git reads as this ref alone, e.g. "main",
	// "origin/main", "v1.0", or "heads/main" when a tag is called main too.
	Name string
	// Branch is the name of a local branch, and "" for any other ref.
	Branch string
	// CheckedOut reports whether a local branch is checked out in a worktree.
	CheckedOut bool
}

// ListRefs returns the local branches of the repository at dir, then its
// remote-tracking branches and tags, each part most recent first and at most
// maxRefs long. Symbolic refs such as origin/HEAD are left out, since they only
// alias another ref, and so are names git would read as an option.
func ListRefs(ctx context.Context, dir string) ([]Ref, error) {
	branches, err := listRefs(ctx, dir, "refs/heads")
	if err != nil {
		return nil, err
	}
	others, err := listRefs(ctx, dir, "refs/remotes", "refs/tags")
	if err != nil {
		return nil, err
	}
	return append(branches, others...), nil
}

// listRefs lists the refs under patterns, most recent first, at most maxRefs.
func listRefs(ctx context.Context, dir string, patterns ...string) ([]Ref, error) {
	out, err := query(ctx, dir, append([]string{"for-each-ref",
		"--sort=-creatordate",
		"--count=" + strconv.Itoa(maxRefs),
		"--format=%(refname)%09%(symref)%09%(worktreepath)%09%(refname:short)",
	}, patterns...)...)
	if err != nil {
		return nil, err
	}
	var refs []Ref
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) != 4 || fields[1] != "" {
			continue
		}
		branch, isBranch := strings.CutPrefix(fields[0], "refs/heads/")
		if !isBranch {
			branch = ""
		}
		name := fields[3]
		if strings.HasPrefix(name, "-") || strings.HasPrefix(branch, "-") {
			continue
		}
		refs = append(refs, Ref{Name: name, Branch: branch, CheckedOut: fields[2] != ""})
	}
	return refs, nil
}
