package cli

// The `workstream` verb: list, create, remove and open workstreams.
//
// There is deliberately no `workstream start`/`stop`: the daemon exposes no such
// endpoint — a workstream's runtime state is reported by its provider, and it is
// *sessions* that start and stop (`homeshell session start|stop`). This verb
// grows with the API rather than ahead of it.

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"strings"
)

func init() {
	register(Command{
		Name:    "workstream",
		Summary: "List, create, rename, open and remove workstreams",
		Run:     runWorkstream,
	})
}

func runWorkstream(env Env, args []string) int {
	return dispatchGroup(env, "workstream", "Manage the workstreams of a project.", workstreamSubcommands(), args)
}

func workstreamSubcommands() []clientCommand {
	return []clientCommand{
		workstreamList(), workstreamProviders(), workstreamCreate(), workstreamRename(),
		workstreamRemove(), workstreamOpen(), workstreamNote(), workstreamClearStatus(),
	}
}

// workstreamProvider mirrors one entry of
// GET /api/projects/{projectId}/workstream-providers: the providers that can
// create a workstream in that project, each with its creation schema.
type workstreamProvider struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Status struct {
		State  string `json:"state"`
		Reason string `json:"reason"`
	} `json:"status"`
}

// openResult mirrors the payload of the open endpoints: the mode that was used
// and whatever the provider attached to it (a web IDE returns a URL for the
// *client* to open — the daemon resolves it and never proxies it).
type openResult struct {
	Mode string          `json:"mode"`
	Data json.RawMessage `json:"data"`
}

func workstreamList() clientCommand {
	var projectRef string
	return clientCommand{
		Verb:    "workstream",
		Name:    "list",
		Summary: "List workstreams, across every project or within one",
		Setup: func(fs *flag.FlagSet) {
			fs.StringVar(&projectRef, "project", "", "only list this project's workstreams (ID or name)")
		},
		Run: func(call clientCall) error {
			if err := call.none(); err != nil {
				return err
			}
			snap, err := fetchSnapshot(call.Ctx, call.Client)
			if err != nil {
				return err
			}
			entries, err := snap.workstreams(projectRef)
			if err != nil {
				return err
			}
			if call.Options.asJSON {
				objects := make([]json.RawMessage, 0, len(entries))
				for _, entry := range entries {
					objects = append(objects, entry.Workstream.raw)
				}
				writeRawList(call.Env.Stdout, objects)
				return nil
			}
			rows := make([][]string, 0, len(entries))
			for _, entry := range entries {
				rows = append(rows, []string{
					entry.Workstream.ID,
					entry.Workstream.Name,
					entry.Project.Name,
					entry.Workstream.Effective,
					fmt.Sprint(len(entry.Workstream.Sessions)),
					entry.Workstream.Provider,
					entry.Workstream.Strategy,
					entry.Workstream.WorkingDir,
				})
			}
			writeTable(call.Env.Stdout,
				[]string{"ID", "NAME", "PROJECT", "STATE", "SESSIONS", "PROVIDER", "STRATEGY", "DIR"}, rows)
			return nil
		},
	}
}

func workstreamCreate() clientCommand {
	var projectRef, providerID, name, config string
	return clientCommand{
		Verb:    "workstream",
		Name:    "create",
		Summary: "Create a workstream in a project via a workstream provider",
		Setup: func(fs *flag.FlagSet) {
			fs.StringVar(&projectRef, "project", "", "the project to create in (ID or name); required")
			fs.StringVar(&providerID, "provider", "", "workstream provider ID (optional when the project offers exactly one)")
			fs.StringVar(&name, "name", "", "workstream name (empty lets the daemon apply its name template)")
			fs.StringVar(&config, "config", "", `provider config as a JSON object, e.g. '{"strategy":"worktree"}'`)
		},
		Run: func(call clientCall) error {
			if err := call.none(); err != nil {
				return err
			}
			if strings.TrimSpace(projectRef) == "" {
				return missingArgError("-project")
			}
			settings, err := parseConfigFlag(config)
			if err != nil {
				return err
			}
			snap, err := fetchSnapshot(call.Ctx, call.Client)
			if err != nil {
				return err
			}
			owner, err := snap.findProject(projectRef)
			if err != nil {
				return err
			}
			chosen, err := resolveWorkstreamProvider(call, owner.ID, providerID)
			if err != nil {
				return err
			}

			request := map[string]any{"providerId": chosen, "name": name}
			if settings != nil {
				request["config"] = settings
			}
			payload, err := requestJSON(call.Ctx, call.Client, http.MethodPost,
				"/api/projects/"+owner.ID+"/workstreams", request)
			if err != nil {
				return err
			}
			if call.Options.asJSON {
				writeRaw(call.Env.Stdout, payload)
				return nil
			}
			var created workstreamFields
			if err := decodePayload(payload, &created); err != nil {
				return err
			}
			// The daemon answers as soon as the placeholder exists; the provider
			// finishes setting the workstream up asynchronously, so say so rather
			// than implying the directory is ready.
			fmt.Fprintf(call.Env.Stdout, "creating workstream %s (%s) in %s via %s\n",
				created.Name, created.ID, owner.Name, chosen)
			fmt.Fprintln(call.Env.Stdout, `follow it with "homeshell workstream list" or "homeshell events"`)
			return nil
		},
	}
}

// resolveWorkstreamProvider picks the provider to create with. An explicit ID is
// passed through untouched (the daemon validates it and reports why it cannot
// create); with none given, a project that offers exactly one provider needs no
// choice, and anything else is refused with the list to choose from rather than
// the CLI guessing.
func resolveWorkstreamProvider(call clientCall, projectID, requested string) (string, error) {
	if strings.TrimSpace(requested) != "" {
		return requested, nil
	}
	payload, err := requestJSON(call.Ctx, call.Client, http.MethodGet,
		"/api/projects/"+projectID+"/workstream-providers", nil)
	if err != nil {
		return "", err
	}
	var offered []workstreamProvider
	if err := decodePayload(payload, &offered); err != nil {
		return "", err
	}
	if len(offered) == 1 {
		return offered[0].ID, nil
	}
	if len(offered) == 0 {
		return "", fmt.Errorf("no workstream provider can create in this project")
	}
	choices := make([]string, 0, len(offered))
	for _, candidate := range offered {
		choices = append(choices, fmt.Sprintf("%s (%s)", candidate.ID, candidate.Label))
	}
	return "", &usageError{message: fmt.Sprintf(
		"-provider is required when a project offers several: %s", strings.Join(choices, ", "))}
}

// workstreamProviders lists what can create a workstream in a project — the
// answer to "what do I pass to -provider?". The set is the daemon's: it offers
// only providers that are available and can create on that project, each with
// the status it reports for itself.
func workstreamProviders() clientCommand {
	var projectRef string
	return clientCommand{
		Verb:    "workstream",
		Name:    "providers",
		Summary: "List the workstream providers that can create in a project",
		Setup: func(fs *flag.FlagSet) {
			fs.StringVar(&projectRef, "project", "", "the project to ask about (ID or name); required")
		},
		Run: func(call clientCall) error {
			if err := call.none(); err != nil {
				return err
			}
			if strings.TrimSpace(projectRef) == "" {
				return missingArgError("-project")
			}
			snap, err := fetchSnapshot(call.Ctx, call.Client)
			if err != nil {
				return err
			}
			owner, err := snap.findProject(projectRef)
			if err != nil {
				return err
			}
			payload, err := requestJSON(call.Ctx, call.Client, http.MethodGet,
				"/api/projects/"+owner.ID+"/workstream-providers", nil)
			if err != nil {
				return err
			}
			if call.Options.asJSON {
				writeRaw(call.Env.Stdout, payload)
				return nil
			}
			var offered []workstreamProvider
			if err := decodePayload(payload, &offered); err != nil {
				return err
			}
			rows := make([][]string, 0, len(offered))
			for _, candidate := range offered {
				rows = append(rows, []string{
					candidate.ID, candidate.Label, candidate.Status.State, candidate.Status.Reason,
				})
			}
			writeTable(call.Env.Stdout, []string{"ID", "LABEL", "STATE", "REASON"}, rows)
			return nil
		},
	}
}

func workstreamRename() clientCommand {
	var projectRef string
	return clientCommand{
		Verb:    "workstream",
		Name:    "rename",
		Args:    "<workstream> <new-name>",
		Summary: "Rename a workstream (names are unique within a project)",
		Setup: func(fs *flag.FlagSet) {
			fs.StringVar(&projectRef, "project", "", "narrow the lookup to this project (ID or name)")
		},
		Run: func(call clientCall) error {
			ref, name, err := call.two("a workstream", "a new name")
			if err != nil {
				return err
			}
			snap, err := fetchSnapshot(call.Ctx, call.Client)
			if err != nil {
				return err
			}
			target, err := snap.findWorkstream(ref, projectRef)
			if err != nil {
				return err
			}
			// A name another workstream in the project already holds is a 409 from
			// the daemon, reported like any other refusal — the uniqueness rule is
			// the store's, not this CLI's to re-check.
			if _, err := requestJSON(call.Ctx, call.Client, http.MethodPatch,
				workstreamPath(target), map[string]string{"name": name}); err != nil {
				return err
			}
			if !call.Options.asJSON {
				fmt.Fprintf(call.Env.Stdout, "renamed workstream %s to %s (%s)\n",
					target.Workstream.Name, name, target.Workstream.ID)
			}
			return nil
		},
	}
}

// workstreamNote opens the workstream's NOTES.md through the daemon host's
// default application — the same hand-off the UI performs. It is a desktop
// action on the daemon's machine, so what comes back is the path it opened.
func workstreamNote() clientCommand {
	var projectRef string
	return clientCommand{
		Verb:    "workstream",
		Name:    "note",
		Args:    "<workstream>",
		Summary: "Open the workstream's NOTES.md (created on first use)",
		Setup: func(fs *flag.FlagSet) {
			fs.StringVar(&projectRef, "project", "", "narrow the lookup to this project (ID or name)")
		},
		Run: func(call clientCall) error {
			ref, err := call.one("a workstream")
			if err != nil {
				return err
			}
			snap, err := fetchSnapshot(call.Ctx, call.Client)
			if err != nil {
				return err
			}
			target, err := snap.findWorkstream(ref, projectRef)
			if err != nil {
				return err
			}
			payload, err := requestJSON(call.Ctx, call.Client, http.MethodPost,
				workstreamPath(target)+"/note", nil)
			if err != nil {
				return err
			}
			if call.Options.asJSON {
				writeRaw(call.Env.Stdout, payload)
				return nil
			}
			var note struct {
				Path    string `json:"path"`
				Created bool   `json:"created"`
			}
			if err := decodePayload(payload, &note); err != nil {
				return err
			}
			verb := "opened"
			if note.Created {
				verb = "created and opened"
			}
			fmt.Fprintf(call.Env.Stdout, "%s %s\n", verb, note.Path)
			return nil
		},
	}
}

// workstreamClearStatus discards a workstream's transient status message. The
// daemon refuses it for a workstream in `error` state, where the message is the
// failed creation's only account of itself — removing the workstream is that
// one's disposal.
func workstreamClearStatus() clientCommand {
	var projectRef string
	return clientCommand{
		Verb:    "workstream",
		Name:    "clear-status",
		Args:    "<workstream>",
		Summary: "Discard the workstream's transient status message",
		Setup: func(fs *flag.FlagSet) {
			fs.StringVar(&projectRef, "project", "", "narrow the lookup to this project (ID or name)")
		},
		Run: func(call clientCall) error {
			ref, err := call.one("a workstream")
			if err != nil {
				return err
			}
			snap, err := fetchSnapshot(call.Ctx, call.Client)
			if err != nil {
				return err
			}
			target, err := snap.findWorkstream(ref, projectRef)
			if err != nil {
				return err
			}
			if _, err := requestJSON(call.Ctx, call.Client, http.MethodDelete,
				workstreamPath(target)+"/status", nil); err != nil {
				return err
			}
			if !call.Options.asJSON {
				fmt.Fprintf(call.Env.Stdout, "cleared the status message on %s (%s)\n",
					target.Workstream.Name, target.Workstream.ID)
			}
			return nil
		},
	}
}

func workstreamRemove() clientCommand {
	var projectRef string
	var keepFiles, force bool
	return clientCommand{
		Verb:    "workstream",
		Name:    "remove",
		Aliases: []string{"delete"},
		Args:    "<workstream>",
		Summary: "Remove a workstream by ID or name, tearing its sessions down",
		Setup: func(fs *flag.FlagSet) {
			fs.StringVar(&projectRef, "project", "", "narrow the lookup to this project (ID or name)")
			fs.BoolVar(&keepFiles, "keep-files", false, "keep the working directory on disk")
			fs.BoolVar(&force, "force", false, "stop busy sessions and remove anyway")
		},
		Run: func(call clientCall) error {
			ref, err := call.one("a workstream")
			if err != nil {
				return err
			}
			snap, err := fetchSnapshot(call.Ctx, call.Client)
			if err != nil {
				return err
			}
			target, err := snap.findWorkstream(ref, projectRef)
			if err != nil {
				return err
			}
			// force is never sent unless asked for, so the daemon's guard applies to
			// the CLI exactly as it does to a click: a workstream with a busy session
			// is refused with the sessions named, and the retry is the user's call.
			_, err = requestJSON(call.Ctx, call.Client, http.MethodDelete,
				workstreamPath(target), map[string]bool{"keepFiles": keepFiles, "force": force})
			if err != nil {
				return err
			}
			if !call.Options.asJSON {
				fmt.Fprintf(call.Env.Stdout, "removed workstream %s (%s)\n",
					target.Workstream.Name, target.Workstream.ID)
			}
			return nil
		},
	}
}

func workstreamOpen() clientCommand {
	var projectRef, mode string
	return clientCommand{
		Verb:    "workstream",
		Name:    "open",
		Args:    "<workstream>",
		Summary: "Open a workstream in one of the modes the daemon offers for it",
		Setup: func(fs *flag.FlagSet) {
			fs.StringVar(&projectRef, "project", "", "narrow the lookup to this project (ID or name)")
			fs.StringVar(&mode, "mode", "", "open mode ID; empty uses the default open (a mode the daemon does not offer is refused with the ones it does)")
		},
		Run: func(call clientCall) error {
			ref, err := call.one("a workstream")
			if err != nil {
				return err
			}
			snap, err := fetchSnapshot(call.Ctx, call.Client)
			if err != nil {
				return err
			}
			target, err := snap.findWorkstream(ref, projectRef)
			if err != nil {
				return err
			}
			// The default open takes no body; a chosen mode is the open-with
			// endpoint's, which is the one that reads and validates it.
			path, body := workstreamPath(target)+"/open", any(nil)
			if mode != "" {
				path, body = workstreamPath(target)+"/open-with", map[string]string{"mode": mode}
			}
			payload, err := requestJSON(call.Ctx, call.Client, http.MethodPost, path, body)
			if err != nil {
				return openModeError(err, target)
			}
			if call.Options.asJSON {
				writeRaw(call.Env.Stdout, payload)
				return nil
			}
			return writeOpenResult(call, payload, target.Workstream.Name)
		},
	}
}

// openModeError adds the modes this workstream actually offers to a refusal, so a
// wrong -mode is answered with the choice rather than only a rejection. The set is
// the daemon's — it derives one mode per way it can really hand the workstream
// over — so the CLI lists what it was pushed and invents nothing.
func openModeError(cause error, target workstreamEntry) error {
	modes := target.Workstream.OpenModes
	if len(modes) == 0 {
		return fmt.Errorf("%w (this workstream offers no open modes)", cause)
	}
	offered := make([]string, 0, len(modes))
	for _, mode := range modes {
		offered = append(offered, fmt.Sprintf("%s (%s)", mode.ID, mode.Label))
	}
	return fmt.Errorf("%w — modes on offer: %s", cause, strings.Join(offered, ", "))
}

// writeOpenResult reports what an open did. A mode that resolves a URL (a web IDE)
// returns it for the caller to open, so print it — that is the whole result.
func writeOpenResult(call clientCall, payload []byte, subject string) error {
	var result openResult
	if err := decodePayload(payload, &result); err != nil {
		return err
	}
	fmt.Fprintf(call.Env.Stdout, "opened %s in mode %s\n", subject, result.Mode)
	var detail struct {
		URL string `json:"url"`
	}
	if len(result.Data) > 0 && json.Unmarshal(result.Data, &detail) == nil && detail.URL != "" {
		fmt.Fprintln(call.Env.Stdout, detail.URL)
	}
	return nil
}

// workstreamPath is the API path addressing a workstream: project and workstream
// together, which is how the daemon identifies one.
func workstreamPath(target workstreamEntry) string {
	return "/api/projects/" + target.Project.ID + "/workstreams/" + target.Workstream.ID
}

// parseConfigFlag turns a --config value into the object the API expects. It is
// validated here only as *being* JSON — a malformed value is a mistake in the
// invocation (exit 2), whereas whether the object satisfies the provider's schema
// is the daemon's judgement to make and report (exit 1).
func parseConfigFlag(raw string) (json.RawMessage, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	if !json.Valid([]byte(trimmed)) {
		return nil, &usageError{message: "-config is not valid JSON"}
	}
	return json.RawMessage(trimmed), nil
}
