package cli

// The `session` verb: list sessions, create one with a chosen agent, start/stop
// it, type a line into it, and open a terminal attached to it. Every subcommand
// is one API call; the daemon enforces the provider's concurrency limit, decides
// what an agent option means, and reports session state.

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
)

func init() {
	register(Command{
		Name:    "session",
		Summary: "List, create, control, prompt, rename and remove sessions",
		Run:     runSession,
	})
}

func runSession(env Env, args []string) int {
	return dispatchGroup(env, "session", "Manage the agent sessions inside a workstream.", sessionSubcommands(), args)
}

func sessionSubcommands() []clientCommand {
	return []clientCommand{
		sessionList(), sessionAgents(), sessionNew(), sessionStart(), sessionStop(),
		sessionRestart(), sessionPrompt(), sessionKey(), sessionOpen(), sessionRename(), sessionRemove(),
	}
}

// sessionSchemas mirrors one entry of
// GET /api/projects/{id}/workstreams/{id}/session-schemas: the session types a
// provider can create in that workstream. Labels are the daemon's, so
// the CLI never special-cases a provider.
type sessionSchemas struct {
	ProviderID string `json:"providerId"`
	Label      string `json:"label"`
	Options    []struct {
		ID    string `json:"id"`
		Label string `json:"label"`
	} `json:"options"`
}

// scopeFlags are the two narrowing flags the session subcommands share, so a
// session named by title can be disambiguated the same way everywhere.
type scopeFlags struct {
	project    string
	workstream string
}

func (s *scopeFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&s.project, "project", "", "narrow the lookup to this project (ID or name)")
	fs.StringVar(&s.workstream, "workstream", "", "narrow the lookup to this workstream (ID or name)")
}

func sessionList() clientCommand {
	var scope scopeFlags
	return clientCommand{
		Verb:    "session",
		Name:    "list",
		Summary: "List sessions, across every workstream or within one",
		Setup:   func(fs *flag.FlagSet) { scope.register(fs) },
		Run: func(call clientCall) error {
			if err := call.none(); err != nil {
				return err
			}
			snap, err := fetchSnapshot(call.Ctx, call.Client)
			if err != nil {
				return err
			}
			entries, err := snap.sessions(scope.project, scope.workstream)
			if err != nil {
				return err
			}
			if call.Options.asJSON {
				objects := make([]json.RawMessage, 0, len(entries))
				for _, entry := range entries {
					objects = append(objects, entry.Session.raw)
				}
				writeRawList(call.Env.Stdout, objects)
				return nil
			}
			rows := make([][]string, 0, len(entries))
			for _, entry := range entries {
				rows = append(rows, []string{
					entry.Session.ID,
					entry.Session.Title,
					entry.Session.Agent,
					entry.Session.State,
					entry.Workstream.Name,
					entry.Project.Name,
				})
			}
			writeTable(call.Env.Stdout, []string{"ID", "TITLE", "AGENT", "STATE", "WORKSTREAM", "PROJECT"}, rows)
			return nil
		},
	}
}

func sessionNew() clientCommand {
	var scope scopeFlags
	var agent, providerID, config string
	return clientCommand{
		Verb:    "session",
		Name:    "new",
		Summary: "Create a session in a workstream with a chosen agent",
		Setup: func(fs *flag.FlagSet) {
			scope.register(fs)
			fs.StringVar(&agent, "agent", "", "session option ID (optional when the workstream offers exactly one)")
			fs.StringVar(&providerID, "provider", "", "session provider ID (only needed when two providers offer the same option ID)")
			fs.StringVar(&config, "config", "", `launch config as a JSON object, e.g. '{"model":"opus"}'`)
		},
		Run: func(call clientCall) error {
			if err := call.none(); err != nil {
				return err
			}
			if strings.TrimSpace(scope.workstream) == "" {
				return missingArgError("-workstream")
			}
			settings, err := parseConfigFlag(config)
			if err != nil {
				return err
			}
			snap, err := fetchSnapshot(call.Ctx, call.Client)
			if err != nil {
				return err
			}
			target, err := snap.findWorkstream(scope.workstream, scope.project)
			if err != nil {
				return err
			}
			chosenProvider, chosenOption, err := resolveSessionOption(call, target, providerID, agent)
			if err != nil {
				return err
			}

			request := map[string]any{"providerId": chosenProvider, "optionId": chosenOption}
			if settings != nil {
				request["config"] = settings
			}
			payload, err := requestJSON(call.Ctx, call.Client, http.MethodPost,
				workstreamPath(target)+"/sessions", request)
			if err != nil {
				return err
			}
			if call.Options.asJSON {
				// 202 Accepted carries no body; there is nothing to pass through.
				writeRaw(call.Env.Stdout, payload)
				return nil
			}
			// Creation is asynchronous: the daemon accepts it and the session appears
			// once the provider has launched it, which is what the SSE feed carries.
			fmt.Fprintf(call.Env.Stdout, "creating a %s session in %s via %s\n",
				chosenOption, target.Workstream.Name, chosenProvider)
			fmt.Fprintln(call.Env.Stdout, `follow it with "homeshell session list" or "homeshell events"`)
			return nil
		},
	}
}

// resolveSessionOption picks the provider and session option to create with. A
// workstream offering exactly one option needs no choice; otherwise the caller
// must name one, and the refusal lists what is on offer. Which options exist is
// the provider's answer (only installed agent CLIs are offered), so this asks
// rather than assumes.
func resolveSessionOption(call clientCall, target workstreamEntry, requestedProvider, requestedOption string) (providerID, optionID string, err error) {
	payload, err := requestJSON(call.Ctx, call.Client, http.MethodGet,
		workstreamPath(target)+"/session-schemas", nil)
	if err != nil {
		return "", "", err
	}
	var offered []sessionSchemas
	if err := decodePayload(payload, &offered); err != nil {
		return "", "", err
	}

	type candidate struct{ provider, option, label string }
	var candidates []candidate
	for _, offering := range offered {
		if requestedProvider != "" && offering.ProviderID != requestedProvider {
			continue
		}
		for _, option := range offering.Options {
			if requestedOption != "" && option.ID != requestedOption {
				continue
			}
			candidates = append(candidates, candidate{offering.ProviderID, option.ID, option.Label})
		}
	}

	switch len(candidates) {
	case 1:
		return candidates[0].provider, candidates[0].option, nil
	case 0:
		if requestedOption != "" {
			return "", "", fmt.Errorf("no session option %q is offered in this workstream%s",
				requestedOption, describeOffered(offered))
		}
		return "", "", fmt.Errorf("no provider offers a session in this workstream " +
			"(a session provider reports itself unavailable when its agent CLI is not installed)")
	default:
		named := make([]string, 0, len(candidates))
		for _, option := range candidates {
			named = append(named, fmt.Sprintf("%s/%s (%s)", option.provider, option.option, option.label))
		}
		return "", "", &usageError{message: fmt.Sprintf(
			"-agent is required when a workstream offers several: %s", strings.Join(named, ", "))}
	}
}

// describeOffered lists every option on offer, for the message that reports an
// unknown -agent.
func describeOffered(offered []sessionSchemas) string {
	var named []string
	for _, offering := range offered {
		for _, option := range offering.Options {
			named = append(named, offering.ProviderID+"/"+option.ID)
		}
	}
	if len(named) == 0 {
		return ""
	}
	return " — on offer: " + strings.Join(named, ", ")
}

// sessionAction builds a subcommand that resolves a session and posts one action
// to it, which is the shape of start, stop and open alike.
func sessionAction(name, summary, verbed string, action func(call clientCall, target sessionEntry) error) clientCommand {
	var scope scopeFlags
	command := clientCommand{
		Verb:    "session",
		Name:    name,
		Args:    "<session>",
		Summary: summary,
		Setup:   func(fs *flag.FlagSet) { scope.register(fs) },
	}
	command.Run = func(call clientCall) error {
		ref, err := call.one("a session")
		if err != nil {
			return err
		}
		snap, err := fetchSnapshot(call.Ctx, call.Client)
		if err != nil {
			return err
		}
		target, err := snap.findSession(ref, scope.project, scope.workstream)
		if err != nil {
			return err
		}
		if err := action(call, target); err != nil {
			return err
		}
		if verbed != "" && !call.Options.asJSON {
			fmt.Fprintf(call.Env.Stdout, "%s session %s (%s)\n", verbed, sessionLabel(target), target.Session.ID)
		}
		return nil
	}
	return command
}

func sessionStart() clientCommand {
	return sessionAction("start", "Start a stopped session", "started",
		func(call clientCall, target sessionEntry) error {
			_, err := requestJSON(call.Ctx, call.Client, http.MethodPost, sessionPath(target)+"/start", nil)
			return err
		})
}

func sessionStop() clientCommand {
	return sessionAction("stop", "Stop a running session (it can be started again)", "stopped",
		func(call clientCall, target sessionEntry) error {
			_, err := requestJSON(call.Ctx, call.Client, http.MethodPost, sessionPath(target)+"/stop", nil)
			return err
		})
}

// sessionRestart starts a session again with new launch settings. Without
// -config the daemon replays what the session was last started with, which is how
// a session picks up a change in the provider's own defaults.
func sessionRestart() clientCommand {
	var config string
	var fresh bool
	var settings json.RawMessage
	command := sessionAction("restart", "Start a session again, optionally with new settings", "restarting",
		func(call clientCall, target sessionEntry) error {
			body := map[string]any{"fresh": fresh}
			if settings != nil {
				body["config"] = settings
			}
			payload, err := requestJSON(call.Ctx, call.Client, http.MethodPost, sessionPath(target)+"/restart", body)
			if err != nil {
				return err
			}
			if call.Options.asJSON {
				writeRaw(call.Env.Stdout, payload)
			}
			return nil
		})
	scoped := command.Setup
	command.Setup = func(fs *flag.FlagSet) {
		scoped(fs)
		fs.StringVar(&config, "config", "", "the session's settings as a JSON object (the create form's fields); omitted, the ones it was last started with")
		fs.BoolVar(&fresh, "fresh", false, "start a new conversation instead of continuing the last one (required while another session of the same agent works in its directory)")
	}
	// The flag is checked before the snapshot is fetched, so a malformed one is a
	// usage error without a round-trip to the daemon.
	run := command.Run
	command.Run = func(call clientCall) error {
		var err error
		if settings, err = parseConfigFlag(config); err != nil {
			return err
		}
		return run(call)
	}
	return command
}

func sessionOpen() clientCommand {
	var mode string
	command := sessionAction("open", "Open a terminal attached to a session", "",
		func(call clientCall, target sessionEntry) error {
			payload, err := requestJSON(call.Ctx, call.Client, http.MethodPost,
				sessionPath(target)+"/open", map[string]string{"mode": mode})
			if err != nil {
				return err
			}
			if call.Options.asJSON {
				writeRaw(call.Env.Stdout, payload)
				return nil
			}
			return writeOpenResult(call, payload, sessionLabel(target))
		})
	// Wrap the shared Setup so this subcommand's own flag joins the scope flags.
	scoped := command.Setup
	command.Setup = func(fs *flag.FlagSet) {
		scoped(fs)
		fs.StringVar(&mode, "mode", "", "open mode ID; empty uses the terminal")
	}
	return command
}

func sessionPrompt() clientCommand {
	var scope scopeFlags
	return clientCommand{
		Verb:    "session",
		Name:    "prompt",
		Args:    "<session> [text...]",
		Summary: "Type a line of text into a session (reads stdin when no text is given)",
		Setup:   func(fs *flag.FlagSet) { scope.register(fs) },
		Run: func(call clientCall) error {
			if len(call.Args) == 0 {
				return missingArgError("a session")
			}
			text := strings.Join(call.Args[1:], " ")
			if text == "" {
				// No text on the command line: take it from the pipe, so a long or
				// generated prompt does not have to survive shell quoting.
				piped, err := io.ReadAll(call.Env.stdin())
				if err != nil {
					return fmt.Errorf("reading the prompt from stdin: %w", err)
				}
				text = strings.TrimRight(string(piped), "\r\n")
			}
			if strings.TrimSpace(text) == "" {
				return missingArgError("prompt text (as arguments or on stdin)")
			}
			snap, err := fetchSnapshot(call.Ctx, call.Client)
			if err != nil {
				return err
			}
			target, err := snap.findSession(call.Args[0], scope.project, scope.workstream)
			if err != nil {
				return err
			}
			if _, err := requestJSON(call.Ctx, call.Client, http.MethodPost,
				sessionPath(target)+"/prompt", map[string]string{"text": text}); err != nil {
				return err
			}
			if !call.Options.asJSON {
				fmt.Fprintf(call.Env.Stdout, "sent %d characters to session %s (%s)\n",
					len([]rune(text)), sessionLabel(target), target.Session.ID)
			}
			return nil
		},
	}
}

// sessionAgents lists the session options a workstream offers — the answer to
// "what do I pass to -agent?". Which options exist is the provider's answer
// (only installed agent CLIs are offered), so this asks the daemon rather than
// listing a set of its own.
func sessionAgents() clientCommand {
	var scope scopeFlags
	return clientCommand{
		Verb:    "session",
		Name:    "agents",
		Summary: "List the session options (agents) a workstream offers",
		Setup:   func(fs *flag.FlagSet) { scope.register(fs) },
		Run: func(call clientCall) error {
			if err := call.none(); err != nil {
				return err
			}
			if strings.TrimSpace(scope.workstream) == "" {
				return missingArgError("-workstream")
			}
			snap, err := fetchSnapshot(call.Ctx, call.Client)
			if err != nil {
				return err
			}
			target, err := snap.findWorkstream(scope.workstream, scope.project)
			if err != nil {
				return err
			}
			payload, err := requestJSON(call.Ctx, call.Client, http.MethodGet,
				workstreamPath(target)+"/session-schemas", nil)
			if err != nil {
				return err
			}
			if call.Options.asJSON {
				writeRaw(call.Env.Stdout, payload)
				return nil
			}
			var offered []sessionSchemas
			if err := decodePayload(payload, &offered); err != nil {
				return err
			}
			var rows [][]string
			for _, offering := range offered {
				for _, option := range offering.Options {
					// The AGENT column is exactly what `session new -agent` takes.
					rows = append(rows, []string{option.ID, option.Label, offering.ProviderID, offering.Label})
				}
			}
			writeTable(call.Env.Stdout, []string{"AGENT", "LABEL", "PROVIDER", "PROVIDER LABEL"}, rows)
			return nil
		},
	}
}

// sessionKey sends one named key to a session, for driving an agent's selection
// menu. The accepted set is the daemon's normalized one; an unknown key is its
// 400 to give, so this forwards what was typed rather than validating first.
func sessionKey() clientCommand {
	var scope scopeFlags
	return clientCommand{
		Verb:    "session",
		Name:    "key",
		Args:    "<session> <key>",
		Summary: "Send one key to a session (up/down/left/right/enter/escape/tab)",
		Setup:   func(fs *flag.FlagSet) { scope.register(fs) },
		Run: func(call clientCall) error {
			ref, key, err := call.two("a session", "a key")
			if err != nil {
				return err
			}
			snap, err := fetchSnapshot(call.Ctx, call.Client)
			if err != nil {
				return err
			}
			target, err := snap.findSession(ref, scope.project, scope.workstream)
			if err != nil {
				return err
			}
			if _, err := requestJSON(call.Ctx, call.Client, http.MethodPost,
				sessionPath(target)+"/key", map[string]string{"key": key}); err != nil {
				return err
			}
			if !call.Options.asJSON {
				fmt.Fprintf(call.Env.Stdout, "sent %s to session %s (%s)\n",
					key, sessionLabel(target), target.Session.ID)
			}
			return nil
		},
	}
}

// sessionRename sets a session's display name, or hands it back to the provider
// with -clear. Clearing is its own operation rather than an empty name: a blank
// title is refused by the daemon, precisely so that a mistyped rename cannot
// silently reset the name.
func sessionRename() clientCommand {
	var scope scopeFlags
	var clear bool
	return clientCommand{
		Verb:    "session",
		Name:    "rename",
		Args:    "<session> <new-title>",
		Summary: "Rename a session, or -clear to restore the provider's own name",
		Setup: func(fs *flag.FlagSet) {
			scope.register(fs)
			fs.BoolVar(&clear, "clear", false, "drop the custom name and use the provider's again")
		},
		Run: func(call clientCall) error {
			ref := ""
			title := ""
			var err error
			if clear {
				if ref, err = call.one("a session"); err != nil {
					return err
				}
			} else if ref, title, err = call.two("a session", "a new title"); err != nil {
				return err
			}
			snap, err := fetchSnapshot(call.Ctx, call.Client)
			if err != nil {
				return err
			}
			target, err := snap.findSession(ref, scope.project, scope.workstream)
			if err != nil {
				return err
			}
			if clear {
				if _, err := requestJSON(call.Ctx, call.Client, http.MethodDelete,
					sessionPath(target)+"/title", nil); err != nil {
					return err
				}
				if !call.Options.asJSON {
					fmt.Fprintf(call.Env.Stdout, "cleared the name of session %s\n", target.Session.ID)
				}
				return nil
			}
			// A title a sibling session already shows is a 409 from the daemon.
			if _, err := requestJSON(call.Ctx, call.Client, http.MethodPatch,
				sessionPath(target), map[string]string{"title": title}); err != nil {
				return err
			}
			if !call.Options.asJSON {
				fmt.Fprintf(call.Env.Stdout, "renamed session %s to %s\n", target.Session.ID, title)
			}
			return nil
		},
	}
}

// sessionRemove tears a session down. Unlike `stop`, this disposes of it: the
// daemon removes a session in any state, which is also how a failed one is
// cleared.
func sessionRemove() clientCommand {
	command := sessionAction("remove", "Remove a session in any state (it is torn down, not stopped)", "removed",
		func(call clientCall, target sessionEntry) error {
			_, err := requestJSON(call.Ctx, call.Client, http.MethodDelete, sessionPath(target), nil)
			return err
		})
	command.Aliases = []string{"delete"}
	return command
}

// sessionPath is the API path addressing a session: project, workstream and
// session together.
func sessionPath(target sessionEntry) string {
	return "/api/projects/" + target.Project.ID +
		"/workstreams/" + target.Workstream.ID +
		"/sessions/" + target.Session.ID
}

// sessionLabel names a session for a message: its live title when the agent
// reports one, else its ID.
func sessionLabel(target sessionEntry) string {
	if strings.TrimSpace(target.Session.Title) != "" {
		return inert(target.Session.Title)
	}
	return target.Session.ID
}
