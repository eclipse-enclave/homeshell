// Package agentspec is the launch plumbing shared by every provider that starts a
// coding agent: what an agent is, which settings a new session of it offers, and
// how a chosen value reaches the agent's command line.
//
// It exists for the same reason internal/agentmodel does — more than one provider
// launches the same agent, and `claude` must mean the same thing whether the Local
// provider runs it on the host or Enclave runs it in a container — and it is
// the level above: agentmodel decides *which models* a session offers, this decides
// *which settings* it offers at all.
//
// **The descriptions themselves are not here.** They are user-authored rows in the
// catalog file (internal/agentcatalog),
// reached through the Source port below. They used to be Go — the `agents` slice,
// three JSON-Schema string constants and one argv-building function per agent in
// provider/local — which meant a flag a CLI added, or a wording fix, was a
// HomeShell release. What is left here is what both providers
// need and neither owns: the vocabulary (Kind, the roles), and the two generators
// that turn rows into a session schema (Schema) and into a launch (Deliver).
package agentspec

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Kind is how a setting's chosen value reaches the agent. It is the field that
// makes a launch knob storable at all: without it a row could say what to ask the
// user, but only Go could say what to do with the answer.
type Kind string

const (
	// Flag passes `<arg> <value>` — `--model claude-opus-5`.
	Flag Kind = "flag"
	// BoolFlag passes `<arg>` and only when the value is true — `--auto-approve`.
	BoolFlag Kind = "bool-flag"
	// Assign passes `<arg> <key>=<value>` — Codex's `-c model_reasoning_effort=high`,
	// a setting the CLI takes as a config override rather than as a flag of its own.
	Assign Kind = "assign"
	// Env starts the agent with `<arg>=<value>` in its environment, for a CLI that
	// has no flag for the setting at all (vibe reads VIBE_ACTIVE_MODEL). How that
	// reaches the process is the provider's business: the Local provider prefixes
	// the argv with `env`, Enclave passes `--pass-env`.
	Env Kind = "env"
	// Positional passes the bare value, after every flag.
	Positional Kind = "positional"
)

// The roles: the meaning the daemon itself attaches to a setting, as opposed to the
// ordinary ones it only passes through (role "").
//
// A role is what lets two agents take the same thing differently without the daemon
// knowing which is which: Gemini's initial prompt is `-i <text>` and Claude's is a
// trailing positional, and both are one RolePrompt row with a different Kind.
const (
	// RoleModel is the model the session runs. Its value comes from the session
	// record rather than from the raw config (CreateSession lifts it out), and its
	// choices come from the model catalog or the CLI's own answer rather than from
	// the row's Choices.
	RoleModel = "model"
	// RolePrompt is an initial task for the agent. Delivered on a **fresh launch
	// only** — a resumed conversation must not be re-seeded with the prompt that
	// started it.
	RolePrompt = "prompt"
	// RoleTitle is the session title, which comes from the session rather than from
	// the create form: a RoleTitle row renders no field at all, and like RolePrompt it
	// is delivered on a fresh launch only, because a resumed session already has its
	// name.
	RoleTitle = "title"
)

// The value types a setting can hold — the two shapes the create forms render (a
// box or a select, and a checkbox).
const (
	TypeString  = "string"
	TypeBoolean = "boolean"
)

// Setting is one launch knob on one agent: what the create form renders, and how
// the value the user chose reaches the CLI.
type Setting struct {
	ID    string `json:"id"`
	Agent string `json:"agent"`
	// Field is the session-config property name, and so the JSON key the client
	// submits. Unique per agent.
	Field string `json:"field"`
	// Role is the meaning the daemon attaches to this knob, empty for one it only
	// passes through. See RoleModel / RolePrompt / RoleTitle.
	Role string `json:"role"`
	// Type is TypeString or TypeBoolean.
	Type string `json:"type"`
	// Title, Description and Format are presentation: the label, the help text, and
	// a rendering hint the client honors ("textarea" for a multi-line box).
	Title       string `json:"title"`
	Description string `json:"description"`
	Format      string `json:"format"`
	// Choices become the field's enum. Empty means free text — the honest shape when
	// the accepted values live somewhere HomeShell cannot see.
	Choices []string `json:"choices"`
	// Default is the schema default, empty for "leave unset" — which is what makes
	// the agent apply its own choice instead of the form silently picking one.
	Default string `json:"default"`
	// Delivery, DeliveryArg and DeliveryKey are how the value reaches the agent: the
	// Kind, the flag or variable name it uses, and (for Assign only) the config key
	// being assigned.
	Delivery    Kind   `json:"delivery"`
	DeliveryArg string `json:"deliveryArg"`
	DeliveryKey string `json:"deliveryKey"`
}

// Agent is one launchable agent: what to run, what to call it, and what it can be
// asked to do. Its Settings are in the order the create form shows them, which is
// also the order their flags reach the command line.
type Agent struct {
	// ID is the agent id every provider shares — the same key as an agent_models
	// row's agent, so the Local provider and Enclave cannot offer different models
	// (or different settings) for the same agent.
	ID string `json:"id"`
	// Binary is the local executable, independent of the agent and detection IDs.
	Binary string `json:"binary"`
	// RuleSetID overrides the detection manifest ID; empty uses ID. Custom launch
	// wrappers can therefore share a standard agent's rules without sharing its ID.
	RuleSetID string `json:"ruleSetId,omitempty"`
	// EnclaveTool binds an Enclave tool/profile to this agent; empty uses ID.
	EnclaveTool string `json:"enclaveTool,omitempty"`
	// Display is the user-facing agent name reported on a session.
	Display string `json:"display"`
	// SessionIDFlag hands the CLI a session id we generated; ResumeFlag resumes that
	// id later. Both empty means the agent takes no id of ours, which is what "not
	// resumable" means here — see Resumable.
	SessionIDFlag string `json:"sessionIdFlag"`
	ResumeFlag    string `json:"resumeFlag"`
	// Transcript marks an agent that writes a Claude-format JSONL transcript per
	// session. Its absence is how a resume tells a session that never answered
	// (nothing to resume, so it launches fresh under the same id) from one that did.
	Transcript bool `json:"transcript"`
	// ListModels is the argv suffix that makes this CLI print the models it has,
	// appended to Binary. Empty for a CLI that cannot answer.
	ListModels []string `json:"listModels"`
	// Settings are this agent's launch knobs, in form order.
	Settings []Setting `json:"settings"`
}

// DetectionID is the manifest to evaluate, never the executable to launch.
func (a Agent) DetectionID() string {
	if a.RuleSetID != "" {
		return a.RuleSetID
	}
	return a.ID
}

// EnclaveToolName is the provider-specific tool/profile bound to this agent.
func (a Agent) EnclaveToolName() string {
	if a.EnclaveTool != "" {
		return a.EnclaveTool
	}
	return a.ID
}

// Resumable reports whether this agent can be resumed by an id we control, which is
// exactly whether it has a resume flag: a session of an agent with none is reported
// gone when its terminal is lost rather than silently relaunched. Derived rather
// than stored so the flag and the capability cannot disagree.
func (a Agent) Resumable() bool { return a.ResumeFlag != "" }

// ModelSetting is the agent's RoleModel setting, if it has one. A provider needs it
// to know whether a chosen model can be delivered at all before it offers one.
func (a Agent) ModelSetting() (Setting, bool) {
	for _, s := range a.Settings {
		if s.Role == RoleModel {
			return s, true
		}
	}
	return Setting{}, false
}

// Source is the port onto the stored agent descriptions, implemented by
// internal/agentcatalog over the catalog file. A provider holds the port rather
// than the table so it can be given a stub, and so a daemon with no persistence
// wired simply has none.
//
// Neither method returns an error: the caller is assembling a create form or a
// launch, and an unreadable description means "this agent is not on offer" — the
// same answer as an agent whose binary is not installed. Whoever implements it logs
// the reason.
type Source interface {
	// Agents is every described agent, in selector order. Empty when there are none
	// or the store cannot be read.
	Agents() []Agent
	// Agent is one description by agent id.
	Agent(id string) (Agent, bool)
}

// Kinds is the set of delivery kinds a caller can honor. It exists because the two
// providers do not have the same reach: one builds the whole command line, the other
// only appends to a command line the agent's own launcher owns. Passing the set in
// keeps a setting the caller cannot deliver *out of the form too*, so the user is
// never offered a knob that would quietly do nothing.
type Kinds map[Kind]bool

// Can reports whether this caller can deliver that kind.
func (k Kinds) Can(kind Kind) bool { return k[kind] }

var (
	// Host is every kind, for a provider that builds the agent's whole command line
	// and controls its environment (provider/local).
	Host = Kinds{Flag: true, BoolFlag: true, Assign: true, Env: true, Positional: true}
	// Appended is every kind except Positional, for a provider that only appends to a
	// command line something else assembles (provider/enclave, where `enclave run …
	// -- <args>` appends onto the tool's own argv). Flags and variables are verified
	// there; a bare positional is not, and a wrong guess turns a session into a launch
	// failure rather than a knob that misbehaves.
	Appended = Kinds{Flag: true, BoolFlag: true, Assign: true, Env: true}
)

// Schema is the session-config JSON Schema for an agent: one property per setting
// the caller can actually deliver, in row order.
//
// models are the choices for the RoleModel field, already decided by the caller
// (agentmodel.Offered — what the CLI reported, else the user's catalog). They win
// over the row's own Choices, which is what keeps a model list that a CLI answers
// for from being overridden by a stale row; a row's Choices are still honored when
// there is nothing to offer.
//
// It returns nil when nothing is left to ask about — the same "this option takes no
// configuration" a client already understands, and what an agent with no rows (an
// Enclave profile that runs no agent CLI) yields.
//
// The document is written property by property rather than marshalled from a map,
// because the create forms render fields in declaration order and Go marshals map
// keys alphabetically. Every string still goes through a JSON encoder: these are the
// user's rows now, so nothing here may assume a title or a model id is quote-free.
func Schema(a Agent, models []string, can Kinds) json.RawMessage {
	var props bytes.Buffer
	for _, s := range a.Settings {
		if !renders(s, can) {
			continue
		}
		if props.Len() > 0 {
			props.WriteByte(',')
		}
		props.Write(encode(s.Field))
		props.WriteByte(':')
		props.Write(property(s, models))
	}
	if props.Len() == 0 {
		return nil
	}
	var doc bytes.Buffer
	doc.WriteString(`{"type":"object","additionalProperties":false,"properties":{`)
	doc.Write(props.Bytes())
	doc.WriteString(`}}`)
	return json.RawMessage(doc.Bytes())
}

// renders reports whether a setting becomes a field in the create form. A RoleTitle
// setting never does — its value is the session's, not the user's — and neither does
// one this caller could not deliver, nor one whose type it cannot render.
func renders(s Setting, can Kinds) bool {
	if s.Role == RoleTitle {
		return false
	}
	if !can.Can(s.Delivery) {
		return false
	}
	return s.Type == TypeString || s.Type == TypeBoolean
}

// property is one setting's JSON Schema, in the subset the create forms render:
// type, title, description, format, enum, default.
func property(s Setting, models []string) []byte {
	var b bytes.Buffer
	b.WriteString(`{"type":`)
	b.Write(encode(s.Type))
	b.WriteString(`,"title":`)
	b.Write(encode(s.Title))
	if s.Description != "" {
		b.WriteString(`,"description":`)
		b.Write(encode(s.Description))
	}
	if s.Format != "" {
		b.WriteString(`,"format":`)
		b.Write(encode(s.Format))
	}
	if choices := enumFor(s, models); len(choices) > 0 {
		b.WriteString(`,"enum":`)
		b.Write(encode(choices))
	}
	if def, ok := defaultFor(s); ok {
		b.WriteString(`,"default":`)
		b.Write(encode(def))
	}
	b.WriteByte('}')
	return b.Bytes()
}

// enumFor is the field's choices: the caller's model list for the model field
// (falling back to the row when it offered none), the row's own for every other.
// Only a string field gets an enum — a checkbox already has its two values.
func enumFor(s Setting, models []string) []string {
	if s.Type != TypeString {
		return nil
	}
	if s.Role == RoleModel && len(models) > 0 {
		return models
	}
	return s.Choices
}

// defaultFor is the field's schema default, typed to match the field: a boolean
// default is a JSON boolean, not the string "false". Empty means no default, which
// leaves the field unset so the agent applies its own choice.
func defaultFor(s Setting) (any, bool) {
	if s.Default == "" {
		return nil, false
	}
	if s.Type == TypeBoolean {
		return s.Default == "true", true
	}
	return s.Default, true
}

// encode marshals a value for embedding in the schema. A Go string, []string or
// bool always marshals, so an error is unreachable; `null` is the inert fallback
// rather than a panic, because a create form is not worth a crashed daemon.
func encode(v any) []byte {
	out, err := json.Marshal(v)
	if err != nil {
		return []byte(`null`)
	}
	return out
}

// Launch is everything a delivery needs that does not come from the settings
// themselves: the values the daemon owns, and whether this is a resume.
type Launch struct {
	// Model is the model the session runs, lifted onto the session record when it was
	// created (RoleModel).
	Model string
	// SessionID is the id we generated and hand the CLI (Agent.SessionIDFlag), and
	// resume by later (Agent.ResumeFlag).
	SessionID string
	// Title is the session title (RoleTitle).
	Title string
	// Config is the validated session config the create form submitted.
	Config json.RawMessage
	// Resume selects a resume launch — continue the conversation by id — over a fresh
	// one. It drops the two settings that *seed* a session, the initial prompt and the
	// title, so a resumed conversation is neither re-prompted nor re-named.
	Resume bool
}

// Command is what a launch delivers: arguments for the agent's own command line, in
// order, and variables the agent process must start with.
//
// The binary is deliberately not in Args. A provider that runs the agent itself puts
// it there (and turns Env into whatever its launcher needs); one that appends to a
// command line it does not own passes Args through and Env by its own mechanism.
type Command struct {
	Args []string
	Env  map[string]string
}

// Deliver builds the launch for an agent from its settings and the values chosen
// for them.
//
// Args order is the rows' order for flags, then every positional — the only ordering
// that carries meaning for these CLIs — with the session-id (or resume) flag first,
// right after the binary the caller prepends. An **empty value is never delivered**:
// unset means "the agent decides", and `--model ""` is not the same as leaving
// `--model` out.
//
// A setting whose kind the caller cannot honor is skipped here exactly as it was
// skipped in Schema, so the two cannot disagree about which knobs exist.
func Deliver(a Agent, l Launch, can Kinds) Command {
	cmd := Command{}
	if flag, id := identity(a, l); flag != "" {
		cmd.Args = append(cmd.Args, flag, id)
	}
	var positionals []string
	for _, s := range a.Settings {
		if !can.Can(s.Delivery) {
			continue
		}
		value, ok := valueOf(s, l)
		if !ok {
			continue
		}
		switch s.Delivery {
		case Flag:
			cmd.Args = append(cmd.Args, s.DeliveryArg, value)
		case BoolFlag:
			cmd.Args = append(cmd.Args, s.DeliveryArg)
		case Assign:
			cmd.Args = append(cmd.Args, s.DeliveryArg, s.DeliveryKey+"="+value)
		case Env:
			if cmd.Env == nil {
				cmd.Env = map[string]string{}
			}
			cmd.Env[s.DeliveryArg] = value
		case Positional:
			positionals = append(positionals, value)
		}
	}
	cmd.Args = append(cmd.Args, positionals...)
	return cmd
}

// identity is the session-id half of a launch: the resume flag on a resume, the
// session-id flag on a fresh launch, and nothing for an agent that takes no id of
// ours or a launch that has none to give.
func identity(a Agent, l Launch) (flag, id string) {
	if l.SessionID == "" {
		return "", ""
	}
	if l.Resume {
		return a.ResumeFlag, l.SessionID
	}
	return a.SessionIDFlag, l.SessionID
}

// valueOf is the value to deliver for one setting, and whether there is one at all.
// The daemon's own roles come from the Launch; every other setting is read from the
// submitted config by its field name.
func valueOf(s Setting, l Launch) (string, bool) {
	switch s.Role {
	case RoleModel:
		return l.Model, l.Model != ""
	case RoleTitle:
		if l.Resume {
			return "", false
		}
		return l.Title, l.Title != ""
	case RolePrompt:
		if l.Resume {
			return "", false
		}
	}
	return configValue(s, l.Config)
}

// configValue reads one setting out of a session config, honoring its declared type:
// a boolean setting delivers only when it is true, and a value of the wrong JSON
// type is treated as absent rather than coerced — the schema said what it must be,
// and a launch is not the place to argue.
func configValue(s Setting, config json.RawMessage) (string, bool) {
	if len(bytes.TrimSpace(config)) == 0 {
		return "", false
	}
	var values map[string]any
	if err := json.Unmarshal(config, &values); err != nil {
		return "", false
	}
	raw, present := values[s.Field]
	if !present {
		return "", false
	}
	switch s.Type {
	case TypeBoolean:
		on, ok := raw.(bool)
		// "true" rather than "" so a boolean is still deliverable by a kind that
		// carries a value; BoolFlag, the kind it normally has, ignores it.
		return "true", ok && on
	default:
		text, ok := raw.(string)
		return text, ok && text != ""
	}
}

// ModelOf reads the chosen model out of a session config: the value of the agent's
// RoleModel field, or "" for an agent that has no model setting at all — so a caller
// cannot act on a model that was never on offer.
//
// It exists because the model is the one setting the daemon lifts out of the config
// onto the session record (a resume re-passes what the session launched with), and
// which field holds it is now a row rather than a Go field name.
func ModelOf(a Agent, config json.RawMessage) string {
	setting, ok := a.ModelSetting()
	if !ok {
		return ""
	}
	model, _ := configValue(setting, config)
	return model
}

// ListModelsArgv is the command that makes an agent's CLI print the models it has,
// or nil for one that cannot be asked. Both providers run it — the Local provider on
// the host, Enclave inside a running container of the tool — so the argv is built
// here rather than assembled the same way twice.
func ListModelsArgv(a Agent) []string {
	if len(a.ListModels) == 0 {
		return nil
	}
	return append([]string{a.Binary}, a.ListModels...)
}

// SplitArgv parses a stored space-separated argv suffix (an agents row's
// list_models). Empty or blank yields nil, which is what "this CLI cannot be asked"
// looks like everywhere else.
func SplitArgv(s string) []string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return nil
	}
	return fields
}
