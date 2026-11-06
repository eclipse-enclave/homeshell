package cli

// The `project` verb: list, add and remove the projects the daemon tracks. Each
// subcommand is a thin wrapper over one API call — the daemon validates the path,
// defaults the name and derives the states; this prints what it answers.

import (
	"flag"
	"fmt"
	"net/http"
)

func init() {
	register(Command{
		Name:    "project",
		Summary: "List, add, rename, refresh and remove projects",
		Run:     runProject,
	})
}

func runProject(env Env, args []string) int {
	return dispatchGroup(env, "project", "Manage the projects the daemon tracks.", projectSubcommands(), args)
}

// projectSubcommands builds the subcommand set fresh on every invocation: each
// command's flags are bound to locals captured by its own closure, so no
// command-line state outlives the call that parsed it.
func projectSubcommands() []clientCommand {
	return []clientCommand{projectList(), projectAdd(), projectRename(), projectRefresh(), projectRemove()}
}

func projectList() clientCommand {
	return clientCommand{
		Verb:    "project",
		Name:    "list",
		Summary: "List the projects and their states",
		Run: func(call clientCall) error {
			if err := call.none(); err != nil {
				return err
			}
			snap, err := fetchSnapshot(call.Ctx, call.Client)
			if err != nil {
				return err
			}
			if call.Options.asJSON {
				// The payload untouched: `project list --json` *is* GET /api/projects.
				writeRaw(call.Env.Stdout, snap.raw)
				return nil
			}
			rows := make([][]string, 0, len(snap.Projects))
			for i := range snap.Projects {
				found := &snap.Projects[i]
				rows = append(rows, []string{
					found.ID,
					found.Name,
					found.Effective,
					fmt.Sprint(len(found.Workstreams)),
					found.Path,
				})
			}
			writeTable(call.Env.Stdout, []string{"ID", "NAME", "STATE", "WORKSTREAMS", "PATH"}, rows)
			return nil
		},
	}
}

func projectAdd() clientCommand {
	var name string
	return clientCommand{
		Verb:    "project",
		Name:    "add",
		Args:    "<path>",
		Summary: "Add a project from a directory on the daemon's host",
		Setup: func(fs *flag.FlagSet) {
			fs.StringVar(&name, "name", "", "display name (defaults to the directory's basename)")
		},
		Run: func(call clientCall) error {
			path, err := call.one("a directory path")
			if err != nil {
				return err
			}
			// The path is resolved and validated by the daemon, not here: it is the
			// daemon's filesystem that has to hold the directory, and it is not
			// necessarily this process's.
			payload, err := requestJSON(call.Ctx, call.Client, http.MethodPost, "/api/projects", map[string]string{
				"path": path,
				"name": name,
			})
			if err != nil {
				return err
			}
			if call.Options.asJSON {
				writeRaw(call.Env.Stdout, payload)
				return nil
			}
			var created projectFields
			if err := decodePayload(payload, &created); err != nil {
				return err
			}
			fmt.Fprintf(call.Env.Stdout, "added project %s (%s) at %s\n", created.Name, created.ID, created.Path)
			return nil
		},
	}
}

func projectRename() clientCommand {
	return clientCommand{
		Verb:    "project",
		Name:    "rename",
		Args:    "<project> <new-name>",
		Summary: "Rename a project (its directory is untouched)",
		Run: func(call clientCall) error {
			ref, name, err := call.two("a project", "a new name")
			if err != nil {
				return err
			}
			snap, err := fetchSnapshot(call.Ctx, call.Client)
			if err != nil {
				return err
			}
			target, err := snap.findProject(ref)
			if err != nil {
				return err
			}
			// A blank name is the daemon's to refuse (400), like any other
			// validation — this only forwards what was typed.
			if _, err := requestJSON(call.Ctx, call.Client, http.MethodPatch,
				"/api/projects/"+target.ID, map[string]string{"name": name}); err != nil {
				return err
			}
			// The daemon answers 204, so there is nothing to pass through for --json.
			if !call.Options.asJSON {
				fmt.Fprintf(call.Env.Stdout, "renamed project %s to %s (%s)\n", target.Name, name, target.ID)
			}
			return nil
		},
	}
}

func projectRefresh() clientCommand {
	return clientCommand{
		Verb:    "project",
		Name:    "refresh",
		Args:    "<project>",
		Summary: "Re-detect whether the project's directory is git-backed",
		Run: func(call clientCall) error {
			ref, err := call.one("a project")
			if err != nil {
				return err
			}
			snap, err := fetchSnapshot(call.Ctx, call.Client)
			if err != nil {
				return err
			}
			target, err := snap.findProject(ref)
			if err != nil {
				return err
			}
			// The point of this verb: a project added before `git init` was run in
			// its directory can be brought up to date without recreating it.
			if _, err := requestJSON(call.Ctx, call.Client, http.MethodPost,
				"/api/projects/"+target.ID+"/refresh", nil); err != nil {
				return err
			}
			if call.Options.asJSON {
				return nil
			}
			// Report what the re-detection concluded rather than only that it ran,
			// which is the whole reason to call it — so re-read the project.
			after, err := fetchSnapshot(call.Ctx, call.Client)
			if err != nil {
				return err
			}
			refreshed, err := after.findProject(target.ID)
			if err != nil {
				return err
			}
			state := "not git-backed"
			if refreshed.GitBacked {
				state = "git-backed"
			}
			fmt.Fprintf(call.Env.Stdout, "refreshed project %s (%s): %s\n", refreshed.Name, refreshed.ID, state)
			return nil
		},
	}
}

func projectRemove() clientCommand {
	return clientCommand{
		Verb:    "project",
		Name:    "remove",
		Aliases: []string{"delete"},
		Args:    "<project>",
		Summary: "Remove a project by ID or name (its files are left alone)",
		Run: func(call clientCall) error {
			ref, err := call.one("a project")
			if err != nil {
				return err
			}
			snap, err := fetchSnapshot(call.Ctx, call.Client)
			if err != nil {
				return err
			}
			target, err := snap.findProject(ref)
			if err != nil {
				return err
			}
			if _, err := requestJSON(call.Ctx, call.Client, http.MethodDelete, "/api/projects/"+target.ID, nil); err != nil {
				return err
			}
			// Nothing to pass through for --json: the daemon answers 204, no body.
			if !call.Options.asJSON {
				fmt.Fprintf(call.Env.Stdout, "removed project %s (%s)\n", target.Name, target.ID)
			}
			return nil
		},
	}
}
