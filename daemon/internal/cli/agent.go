package cli

// The `agent` verb: read and change the agent descriptions — which agents HomeShell
// can launch, which settings a new session of each offers, and which models each one
// puts in that session's Model field.
//
// These used to be Go tables in the two providers, so a flag a CLI added was a
// HomeShell release. They live in the daemon's agent catalog file now, and this verb is
// how they are edited from a script: `agent list` to see what the daemon will
// offer, `agent add|set|remove` to change an agent,
// `agent setting-add|setting-set|setting-remove` to change one launch knob, and
// `agent model-add|model-set|model-remove` to change one offered model. Each
// remove also answers to its `delete` spelling (`delete`, `setting-delete`,
// `model-delete`).
//
// Like every other data verb, it is a pure client over the public API and holds no
// copy of the schema: it posts the fields it was given and lets the daemon decide what
// is storable, so an invalid delivery comes back in the daemon's own words.

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

func init() {
	register(Command{
		Name:    "agent",
		Summary: "Read and change the agents, their session settings and their models",
		Run:     runAgent,
	})
}

func runAgent(env Env, args []string) int {
	return dispatchGroup(env, "agent",
		"Read and change which agents can be launched and what a new session of each offers.",
		agentSubcommands(), args)
}

func agentSubcommands() []clientCommand {
	return []clientCommand{
		agentList(),
		agentAdd(), agentSet(), agentRemove(),
		agentSettingList(), agentSettingAdd(), agentSettingSet(), agentSettingRemove(),
		agentModelList(), agentModelAdd(), agentModelSet(), agentModelRemove(),
	}
}

const (
	agentsPath        = "/api/agents"
	agentSettingsPath = "/api/agent-settings"
	agentModelsPath   = "/api/agent-models"
)

// agentDescriptor mirrors one entry of GET /api/agents, for the tables. It names
// only the fields a table prints; --json never goes through it, so a field the
// daemon adds still reaches a script.
type agentDescriptor struct {
	ID            string            `json:"id"`
	Binary        string            `json:"binary"`
	Display       string            `json:"display"`
	SessionIDFlag string            `json:"sessionIdFlag"`
	ResumeFlag    string            `json:"resumeFlag"`
	Transcript    bool              `json:"transcript"`
	ListModels    []string          `json:"listModels"`
	Settings      []agentSettingRow `json:"settings"`
}

type agentSettingRow struct {
	ID          string   `json:"id"`
	Agent       string   `json:"agent"`
	Field       string   `json:"field"`
	Role        string   `json:"role"`
	Type        string   `json:"type"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Format      string   `json:"format"`
	Choices     []string `json:"choices"`
	Default     string   `json:"default"`
	Delivery    string   `json:"delivery"`
	DeliveryArg string   `json:"deliveryArg"`
	DeliveryKey string   `json:"deliveryKey"`
}

type agentModelRow struct {
	ID    string `json:"id"`
	Agent string `json:"agent"`
	Model string `json:"model"`
}

// agentSettings reads every agent's settings out of GET /api/agents, keeping
// those of agentRef only when it is set. Each setting comes back twice: decoded,
// for the table, and as the daemon's own bytes, for --json.
func agentSettings(call clientCall, agentRef string) ([]agentSettingRow, []json.RawMessage, error) {
	payload, err := requestJSON(call.Ctx, call.Client, http.MethodGet, agentsPath, nil)
	if err != nil {
		return nil, nil, err
	}
	var body struct {
		Agents []struct {
			ID       string            `json:"id"`
			Settings []json.RawMessage `json:"settings"`
		} `json:"agents"`
	}
	if err := decodePayload(payload, &body); err != nil {
		return nil, nil, err
	}
	var settings []agentSettingRow
	var raw []json.RawMessage
	for _, agent := range body.Agents {
		if agentRef != "" && agent.ID != agentRef {
			continue
		}
		for _, object := range agent.Settings {
			var setting agentSettingRow
			if err := decodePayload(object, &setting); err != nil {
				return nil, nil, err
			}
			settings = append(settings, setting)
			raw = append(raw, object)
		}
	}
	return settings, raw, nil
}

func agentList() clientCommand {
	return clientCommand{
		Verb:    "agent",
		Name:    "list",
		Summary: "List the agents the daemon can launch and what it knows about each",
		Run: func(call clientCall) error {
			if err := call.none(); err != nil {
				return err
			}
			payload, err := requestJSON(call.Ctx, call.Client, http.MethodGet, agentsPath, nil)
			if err != nil {
				return err
			}
			if call.Options.asJSON {
				writeRaw(call.Env.Stdout, payload)
				return nil
			}
			var body struct {
				Agents []agentDescriptor `json:"agents"`
			}
			if err := decodePayload(payload, &body); err != nil {
				return err
			}
			rows := make([][]string, 0, len(body.Agents))
			for _, a := range body.Agents {
				rows = append(rows, []string{
					a.ID, a.Binary, a.Display,
					yesNo(a.ResumeFlag != ""), yesNo(a.Transcript),
					strings.Join(a.ListModels, " "),
					strconv.Itoa(len(a.Settings)),
				})
			}
			writeTable(call.Env.Stdout,
				[]string{"ID", "BINARY", "DISPLAY", "RESUMABLE", "TRANSCRIPT", "LIST-MODELS", "SETTINGS"}, rows)
			return nil
		},
	}
}

// agentFlags are the fields an agent row carries, shared by add and set so the two
// cannot drift. Only the ones the caller actually passed are sent, which is what makes
// `set` leave the rest alone.
type agentFlags struct {
	binary, display, sessionIDFlag, resumeFlag, listModels string
	transcript                                             bool
}

func (f *agentFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&f.binary, "binary", "", "the executable to run (and the detect rule-set label)")
	fs.StringVar(&f.display, "display", "", "the user-facing agent name")
	fs.StringVar(&f.sessionIDFlag, "session-id-flag", "", "flag that hands the CLI a session id we generate")
	fs.StringVar(&f.resumeFlag, "resume-flag", "", "flag that resumes that session id; empty means the agent cannot be resumed")
	fs.StringVar(&f.listModels, "list-models", "", "argv suffix that makes the CLI print its models, e.g. --list-models")
	fs.BoolVar(&f.transcript, "transcript", false, "the agent writes a Claude-format JSONL transcript, so a resume can tell whether it answered")
}

// body is the request body for this call: every flag the caller set, and nothing else.
// A flag left alone is absent from the JSON, which is how a PATCH keeps what it does
// not mention.
func (f *agentFlags) body(fs *flag.FlagSet, id string) map[string]any {
	out := map[string]any{}
	if id != "" {
		out["id"] = id
	}
	setFlags(fs, map[string]func(){
		"binary":          func() { out["binary"] = f.binary },
		"display":         func() { out["display"] = f.display },
		"session-id-flag": func() { out["sessionIdFlag"] = f.sessionIDFlag },
		"resume-flag":     func() { out["resumeFlag"] = f.resumeFlag },
		"transcript":      func() { out["transcript"] = f.transcript },
		"list-models":     func() { out["listModels"] = strings.Fields(f.listModels) },
	})
	return out
}

func agentAdd() clientCommand {
	var fields agentFlags
	var parsed *flag.FlagSet
	return clientCommand{
		Verb:    "agent",
		Name:    "add",
		Args:    "<agent-id>",
		Summary: "Describe a new launchable agent",
		Setup: func(fs *flag.FlagSet) {
			fields.register(fs)
			parsed = fs
		},
		Run: func(call clientCall) error {
			id, err := call.one("an agent id")
			if err != nil {
				return err
			}
			return sendRow(call, http.MethodPost, agentsPath, fields.body(parsed, id))
		},
	}
}

func agentSet() clientCommand {
	var fields agentFlags
	var parsed *flag.FlagSet
	return clientCommand{
		Verb:    "agent",
		Name:    "set",
		Args:    "<agent-id>",
		Summary: "Change what the daemon knows about an agent, leaving unmentioned fields alone",
		Setup: func(fs *flag.FlagSet) {
			fields.register(fs)
			parsed = fs
		},
		Run: func(call clientCall) error {
			id, err := call.one("an agent id")
			if err != nil {
				return err
			}
			body := fields.body(parsed, "")
			if len(body) == 0 {
				return &usageError{message: "name at least one field to change"}
			}
			return sendRow(call, http.MethodPatch, agentsPath+"/"+id, body)
		},
	}
}

func agentRemove() clientCommand {
	return clientCommand{
		Verb:    "agent",
		Name:    "remove",
		Aliases: []string{"delete"},
		Args:    "<agent-id>",
		Summary: "Stop describing an agent, removing its session settings with it",
		Run: func(call clientCall) error {
			id, err := call.one("an agent id")
			if err != nil {
				return err
			}
			return sendRow(call, http.MethodDelete, agentsPath+"/"+id, nil)
		},
	}
}

func agentSettingList() clientCommand {
	var agentRef string
	return clientCommand{
		Verb:    "agent",
		Name:    "setting-list",
		Summary: "List the launch settings a new session offers, per agent",
		Setup: func(fs *flag.FlagSet) {
			fs.StringVar(&agentRef, "agent", "", "only this agent's settings")
		},
		Run: func(call clientCall) error {
			if err := call.none(); err != nil {
				return err
			}
			settings, raw, err := agentSettings(call, agentRef)
			if err != nil {
				return err
			}
			if call.Options.asJSON {
				writeRawList(call.Env.Stdout, raw)
				return nil
			}
			rows := make([][]string, 0, len(settings))
			for _, s := range settings {
				rows = append(rows, []string{
					s.ID, s.Agent, s.Field, s.Role, s.Type,
					deliveryOf(s), strings.Join(s.Choices, ","), s.Default,
				})
			}
			writeTable(call.Env.Stdout,
				[]string{"ID", "AGENT", "FIELD", "ROLE", "TYPE", "DELIVERY", "CHOICES", "DEFAULT"}, rows)
			return nil
		},
	}
}

// deliveryOf spells out how a setting reaches the agent, as one readable cell: the
// kind and the flag, variable or key it uses.
func deliveryOf(s agentSettingRow) string {
	switch {
	case s.DeliveryKey != "":
		return fmt.Sprintf("%s %s %s=", s.Delivery, s.DeliveryArg, s.DeliveryKey)
	case s.DeliveryArg != "":
		return s.Delivery + " " + s.DeliveryArg
	default:
		return s.Delivery
	}
}

// settingFlags are the fields a setting row carries, shared by add and set.
type settingFlags struct {
	agent, field, role, valueType, title, description string
	format, choices, defaultValue                     string
	delivery, deliveryArg, deliveryKey                string
}

func (f *settingFlags) register(fs *flag.FlagSet, withAgent bool) {
	if withAgent {
		fs.StringVar(&f.agent, "agent", "", "the agent this setting belongs to; required")
	}
	fs.StringVar(&f.field, "field", "", "the session-config property name the client submits")
	fs.StringVar(&f.role, "role", "", `the daemon's own meaning: "model", "prompt", "title", or empty`)
	fs.StringVar(&f.valueType, "type", "", `"string" or "boolean"`)
	fs.StringVar(&f.title, "title", "", "the label the create form shows")
	fs.StringVar(&f.description, "description", "", "the help text the create form shows")
	fs.StringVar(&f.format, "format", "", `rendering hint, e.g. "textarea" for a multi-line box`)
	fs.StringVar(&f.choices, "choices", "", "comma-separated enum values; empty means free text")
	fs.StringVar(&f.defaultValue, "default", "", "the schema default; empty leaves the field unset")
	fs.StringVar(&f.delivery, "delivery", "", `how the value reaches the agent: flag, bool-flag, assign, env or positional`)
	fs.StringVar(&f.deliveryArg, "delivery-arg", "", "the flag or variable name the delivery uses")
	fs.StringVar(&f.deliveryKey, "delivery-key", "", "the key an assign delivery sets, e.g. model_reasoning_effort")
}

func (f *settingFlags) body(fs *flag.FlagSet) map[string]any {
	out := map[string]any{}
	setFlags(fs, map[string]func(){
		"agent":        func() { out["agent"] = f.agent },
		"field":        func() { out["field"] = f.field },
		"role":         func() { out["role"] = f.role },
		"type":         func() { out["type"] = f.valueType },
		"title":        func() { out["title"] = f.title },
		"description":  func() { out["description"] = f.description },
		"format":       func() { out["format"] = f.format },
		"default":      func() { out["default"] = f.defaultValue },
		"delivery":     func() { out["delivery"] = f.delivery },
		"delivery-arg": func() { out["deliveryArg"] = f.deliveryArg },
		"delivery-key": func() { out["deliveryKey"] = f.deliveryKey },
		"choices":      func() { out["choices"] = splitList(f.choices) },
	})
	return out
}

func agentSettingAdd() clientCommand {
	var fields settingFlags
	var parsed *flag.FlagSet
	return clientCommand{
		Verb:    "agent",
		Name:    "setting-add",
		Summary: "Add a launch setting to an agent's create form",
		Setup: func(fs *flag.FlagSet) {
			fields.register(fs, true)
			parsed = fs
		},
		Run: func(call clientCall) error {
			if err := call.none(); err != nil {
				return err
			}
			return sendRow(call, http.MethodPost, agentSettingsPath, fields.body(parsed))
		},
	}
}

func agentSettingSet() clientCommand {
	var fields settingFlags
	var parsed *flag.FlagSet
	return clientCommand{
		Verb:    "agent",
		Name:    "setting-set",
		Args:    "<setting-id>",
		Summary: "Change one launch setting, leaving unmentioned fields alone",
		Setup: func(fs *flag.FlagSet) {
			fields.register(fs, false)
			parsed = fs
		},
		Run: func(call clientCall) error {
			id, err := call.one("a setting id")
			if err != nil {
				return err
			}
			body := fields.body(parsed)
			if len(body) == 0 {
				return &usageError{message: "name at least one field to change"}
			}
			return sendRow(call, http.MethodPatch, agentSettingsPath+"/"+id, body)
		},
	}
}

func agentSettingRemove() clientCommand {
	return clientCommand{
		Verb:    "agent",
		Name:    "setting-remove",
		Aliases: []string{"setting-delete"},
		Args:    "<setting-id>",
		Summary: "Remove a launch setting from an agent's create form",
		Run: func(call clientCall) error {
			id, err := call.one("a setting id")
			if err != nil {
				return err
			}
			return sendRow(call, http.MethodDelete, agentSettingsPath+"/"+id, nil)
		},
	}
}

func agentModelList() clientCommand {
	var agentRef string
	return clientCommand{
		Verb:    "agent",
		Name:    "model-list",
		Summary: "List the models each agent offers in a new session's Model field",
		Setup: func(fs *flag.FlagSet) {
			fs.StringVar(&agentRef, "agent", "", "only this agent's models")
		},
		Run: func(call clientCall) error {
			if err := call.none(); err != nil {
				return err
			}
			payload, err := requestJSON(call.Ctx, call.Client, http.MethodGet, agentModelsPath, nil)
			if err != nil {
				return err
			}
			if call.Options.asJSON && agentRef == "" {
				writeRaw(call.Env.Stdout, payload)
				return nil
			}
			var body struct {
				Entries []json.RawMessage `json:"entries"`
			}
			if err := decodePayload(payload, &body); err != nil {
				return err
			}
			var entries []agentModelRow
			var raw []json.RawMessage
			for _, object := range body.Entries {
				var entry agentModelRow
				if err := decodePayload(object, &entry); err != nil {
					return err
				}
				if agentRef != "" && entry.Agent != agentRef {
					continue
				}
				entries = append(entries, entry)
				raw = append(raw, object)
			}
			if call.Options.asJSON {
				// Filtered, but still the daemon's envelope and the daemon's bytes, so
				// a script reads the same shape with or without --agent.
				writeRawEnvelope(call.Env.Stdout, "entries", raw)
				return nil
			}
			rows := make([][]string, 0, len(entries))
			for _, entry := range entries {
				rows = append(rows, []string{entry.ID, entry.Agent, entry.Model})
			}
			writeTable(call.Env.Stdout, []string{"ID", "AGENT", "MODEL"}, rows)
			return nil
		},
	}
}

func agentModelAdd() clientCommand {
	return clientCommand{
		Verb:    "agent",
		Name:    "model-add",
		Args:    "<agent-id> <model>",
		Summary: "Offer another model for an agent",
		Run: func(call clientCall) error {
			agent, model, err := call.two("an agent id", "a model")
			if err != nil {
				return err
			}
			return sendRow(call, http.MethodPost, agentModelsPath,
				map[string]any{"agent": agent, "model": model})
		},
	}
}

func agentModelSet() clientCommand {
	return clientCommand{
		Verb:    "agent",
		Name:    "model-set",
		Args:    "<model-id> <model>",
		Summary: "Correct an offered model, keeping its place in the menu",
		Run: func(call clientCall) error {
			id, model, err := call.two("a model row id", "a model")
			if err != nil {
				return err
			}
			return sendRow(call, http.MethodPatch, agentModelsPath+"/"+id,
				map[string]any{"model": model})
		},
	}
}

func agentModelRemove() clientCommand {
	return clientCommand{
		Verb:    "agent",
		Name:    "model-remove",
		Aliases: []string{"model-delete"},
		Args:    "<model-id>",
		Summary: "Stop offering a model",
		Run: func(call clientCall) error {
			id, err := call.one("a model row id")
			if err != nil {
				return err
			}
			return sendRow(call, http.MethodDelete, agentModelsPath+"/"+id, nil)
		},
	}
}

// sendRow performs one write and reports what came back: the daemon's own row as JSON
// when asked for it, and otherwise a line naming what changed. A 204 carries no body,
// which is what a delete answers with.
func sendRow(call clientCall, method, path string, body map[string]any) error {
	var payload any
	if body != nil {
		payload = body
	}
	out, err := requestJSON(call.Ctx, call.Client, method, path, payload)
	if err != nil {
		return err
	}
	if call.Options.asJSON {
		if len(strings.TrimSpace(string(out))) == 0 {
			return nil
		}
		writeRaw(call.Env.Stdout, out)
		return nil
	}
	if len(strings.TrimSpace(string(out))) == 0 {
		fmt.Fprintln(call.Env.Stdout, "done")
		return nil
	}
	var row struct {
		ID string `json:"id"`
	}
	if err := decodePayload(out, &row); err != nil || row.ID == "" {
		fmt.Fprintln(call.Env.Stdout, "done")
		return nil
	}
	fmt.Fprintln(call.Env.Stdout, row.ID)
	return nil
}

// setFlags runs the action for each flag the caller actually set. It is what keeps a
// PATCH from restating defaults: a flag nobody passed must not appear in the body,
// because there the absence of a key means "keep what is stored" and `false` would
// mean "turn it off".
func setFlags(fs *flag.FlagSet, actions map[string]func()) {
	if fs == nil {
		return
	}
	fs.Visit(func(f *flag.Flag) {
		if action, ok := actions[f.Name]; ok {
			action()
		}
	})
}

// splitList parses a comma-separated flag into the list the API takes, dropping blanks
// so `--choices ""` clears the enum rather than storing one empty choice. The result
// is never nil: an empty list must encode as `[]`, because the daemon reads `null`
// as "not supplied" and would keep the stored choices.
func splitList(value string) []string {
	out := []string{}
	for _, part := range strings.Split(value, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}
