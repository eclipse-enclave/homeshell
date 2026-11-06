// Package agentcatalog is the user-editable catalog of agents: which agents
// HomeShell knows how to launch, which settings a new session of each one
// offers, and which models each agent's Model field lists.
//
// The catalog is one JSON file (agents.json) in the data directory, held in
// memory and rewritten atomically on every edit. It used to be three SQLite
// tables; it is a struct now because it is a few kilobytes with one writer,
// read in full at startup and served from memory. Reading it is the
// agentspec.Source and agentmodel.Catalog side of Store, which the providers
// hold; editing it is the rest, which /api/agents, /api/agent-settings and
// /api/agent-models drive.
//
// A fresh install starts from Defaults, written once; from then on the rows are
// the user's, and HomeShell only ever wrote the first draft. A release that
// describes new agents or models appends them through a schema version bump
// (see upgrades): bumping schemaVersion is what makes the append happen, so it
// is never done silently on every load.
package agentcatalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/eclipsesource/homeshell/daemon/internal/agentspec"
	"github.com/eclipsesource/homeshell/daemon/internal/persist"
)

// FileName is the catalog file inside the data directory.
const FileName = "agents.json"

// schemaVersion is the catalog file's schema. Bump it when a release must
// change existing files (new default agents or models, a backfilled field),
// and add the step to upgrades.
const schemaVersion = 1

// upgrades holds, per schema version, the step that brings a file from the
// version before it. A step names what it adds (appendDefaultAgents,
// appendDefaultModels) rather than diffing against Defaults, so an older
// default the user deleted stays deleted.
var upgrades = map[int]func(File) File{}

// Model is one offered model: the agent it belongs to and the model ID handed to
// that agent.
//
// Model is the value passed on the agent's command line, never a display name.
// Nothing here checks that it exists: the agent is the only authority on that,
// and a wrong ID fails at launch with the agent's own message rather than being
// second-guessed by a list the daemon keeps.
type Model struct {
	ID    string `json:"id"`
	Agent string `json:"agent"`
	Model string `json:"model"`
}

// File is the catalog: the agents in selector order, each with its settings in
// form order, and the models in the order they were added. Its disk format is
// fileRecord (record.go).
type File struct {
	SchemaVersion int
	Agents        []agentspec.Agent
	Models        []Model
}

var (
	// ErrNotFound reports an agent or setting that is not in the catalog.
	ErrNotFound = errors.New("agent description not found")
	// ErrDuplicate reports a taken agent id or effective Enclave tool, or a second
	// setting writing the same config field on one agent. Two rows on one field
	// would put it in the create form twice, so it is refused rather than merged.
	ErrDuplicate = errors.New("that agent or setting already exists")
	// ErrUnknownAgent reports a setting or model whose agent has no description. A
	// knob on an agent nothing can launch would never be read, so it is refused at
	// the door rather than stored and ignored.
	ErrUnknownAgent = errors.New("no such agent")
	// ErrInvalid reports a row the daemon will not store because nothing could act
	// on it: an unknown delivery kind or value type, or a delivery that needs an
	// argument and has none.
	ErrInvalid = errors.New("invalid agent setting")
	// ErrModelNotFound reports a model entry that is not in the catalog.
	ErrModelNotFound = errors.New("agent model not found")
	// ErrModelDuplicate reports a model already offered for that agent. Offering
	// one twice would put it in the menu twice, so it is refused rather than merged.
	ErrModelDuplicate = errors.New("that model is already offered for this agent")
)

// Store is the catalog in memory, saved to its file on every edit. Safe for
// concurrent use.
type Store struct {
	mu   sync.Mutex
	path string
	file File
}

// Load reads the catalog at path. A missing file is a fresh install: the
// defaults are written and served. A file that does not parse, carries a
// schema version this build does not know, or breaks a rule the edit methods
// enforce (validateFile), is an error, so a damaged catalog aborts startup
// rather than being silently replaced by the defaults.
func Load(path string) (*Store, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		store := &Store{path: path, file: Defaults()}
		if err := store.save(store.file); err != nil {
			return nil, fmt.Errorf("writing the default agent catalog: %w", err)
		}
		return store, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	record, err := decodeFileRecord(data)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	file := record.file()
	switch {
	case file.SchemaVersion <= 0:
		return nil, fmt.Errorf("%s: missing schemaVersion", path)
	case file.SchemaVersion > schemaVersion:
		return nil, fmt.Errorf("%s: schema version %d is newer than this build's %d", path, file.SchemaVersion, schemaVersion)
	}
	upgraded := file.SchemaVersion < schemaVersion
	store := &Store{path: path, file: normalize(upgrade(file, upgrades))}
	if err := validateFile(store.file); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if upgraded {
		if err := store.save(store.file); err != nil {
			return nil, fmt.Errorf("upgrading %s: %w", path, err)
		}
	}
	return store, nil
}

// NewMemory is a catalog that is never written, for demo mode and tests.
func NewMemory(file File) *Store {
	return &Store{file: normalize(file)}
}

// upgrade brings a file from an older schema to the current one by running each
// version's step in turn. A user's edits, deletions and order are theirs: a step
// only adds what its version introduced.
func upgrade(file File, steps map[int]func(File) File) File {
	for file.SchemaVersion < schemaVersion {
		file.SchemaVersion++
		if step := steps[file.SchemaVersion]; step != nil {
			file = step(file)
		}
	}
	return file
}

// appendDefaultAgents adds the named default agents at the end of the selector,
// settings and models included. One that would collide with a user's row (id,
// Enclave tool or a setting id) is skipped: the user's row wins.
func appendDefaultAgents(file File, agentIDs ...string) File {
	defaults := Defaults()
	for _, id := range agentIDs {
		agentIndex := indexOfAgent(defaults, id)
		if agentIndex < 0 {
			continue
		}
		agent := defaults.Agents[agentIndex]
		if indexOfAgent(file, agent.ID) >= 0 || enclaveToolTaken(file, agent) ||
			slices.ContainsFunc(agent.Settings, func(setting agentspec.Setting) bool { return settingTaken(file, setting.ID) }) {
			continue
		}
		file.Agents = append(file.Agents, agent)
		for _, model := range defaults.Models {
			if model.Agent == agent.ID && indexOfModel(file, model.ID) < 0 && !modelOffered(file, model.Agent, model.Model, "") {
				file.Models = append(file.Models, model)
			}
		}
	}
	return file
}

// appendDefaultModels adds the named default model entries to agents the file
// already has, at the end of the list. One whose agent is gone, whose id is taken,
// or that the agent already offers is skipped.
func appendDefaultModels(file File, modelIDs ...string) File {
	defaults := Defaults()
	for _, id := range modelIDs {
		modelIndex := indexOfModel(defaults, id)
		if modelIndex < 0 {
			continue
		}
		model := defaults.Models[modelIndex]
		if indexOfAgent(file, model.Agent) < 0 || indexOfModel(file, model.ID) >= 0 || modelOffered(file, model.Agent, model.Model, "") {
			continue
		}
		file.Models = append(file.Models, model)
	}
	return file
}

// validateFile applies the rules the edit methods enforce one row at a time to a
// whole file, for one that was edited by hand.
func validateFile(file File) error {
	agentIDs, tools, settingIDs := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, agent := range file.Agents {
		if err := validateAgent(agent); err != nil {
			return err
		}
		if agentIDs[agent.ID] {
			return fmt.Errorf("%w: agent %q", ErrDuplicate, agent.ID)
		}
		if tools[agent.EnclaveToolName()] {
			return fmt.Errorf("%w: Enclave tool %q", ErrDuplicate, agent.EnclaveToolName())
		}
		agentIDs[agent.ID], tools[agent.EnclaveToolName()] = true, true
		fields := map[string]bool{}
		for _, setting := range agent.Settings {
			if err := validateSetting(setting); err != nil {
				return err
			}
			if setting.Agent != agent.ID {
				return fmt.Errorf("%w: setting %q names agent %q but is listed under %q", ErrInvalid, setting.ID, setting.Agent, agent.ID)
			}
			if settingIDs[setting.ID] {
				return fmt.Errorf("%w: setting %q", ErrDuplicate, setting.ID)
			}
			if fields[setting.Field] {
				return fmt.Errorf("%w: %s already has a %q setting", ErrDuplicate, agent.ID, setting.Field)
			}
			settingIDs[setting.ID], fields[setting.Field] = true, true
		}
	}
	modelIDs, offered := map[string]bool{}, map[[2]string]bool{}
	for _, entry := range file.Models {
		if !agentIDs[entry.Agent] {
			return fmt.Errorf("%w: model %q belongs to %q", ErrUnknownAgent, entry.ID, entry.Agent)
		}
		if modelIDs[entry.ID] {
			return fmt.Errorf("%w: id %q", ErrModelDuplicate, entry.ID)
		}
		key := [2]string{entry.Agent, entry.Model}
		if offered[key] {
			return fmt.Errorf("%w: %s offers %q twice", ErrModelDuplicate, entry.Agent, entry.Model)
		}
		modelIDs[entry.ID], offered[key] = true, true
	}
	return nil
}

// normalize gives every collection a non-nil value and drops empty choice lists,
// so a file read from disk serves the same JSON the API promises: an array for
// settings (never null) and null for free-text choices.
func normalize(file File) File {
	if file.Agents == nil {
		file.Agents = []agentspec.Agent{}
	}
	if file.Models == nil {
		file.Models = []Model{}
	}
	for i := range file.Agents {
		file.Agents[i] = normalizeAgent(file.Agents[i])
	}
	return file
}

func normalizeAgent(agent agentspec.Agent) agentspec.Agent {
	if agent.Settings == nil {
		agent.Settings = []agentspec.Setting{}
	}
	if len(agent.ListModels) == 0 {
		agent.ListModels = nil
	}
	for i := range agent.Settings {
		if len(agent.Settings[i].Choices) == 0 {
			agent.Settings[i].Choices = nil
		}
	}
	return agent
}

// save writes the file atomically, or does nothing for an in-memory catalog.
func (s *Store) save(file File) error {
	if s.path == "" {
		return nil
	}
	data, err := json.MarshalIndent(recordFile(file), "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling the agent catalog: %w", err)
	}
	return persist.WriteFileAtomic(s.path, append(data, '\n'))
}

// mutate applies change to a copy of the catalog, saves it, and only then makes
// it current, so a failed write leaves memory and disk agreeing on the old one.
func (s *Store) mutate(change func(file *File) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := cloneFile(s.file)
	if err := change(&next); err != nil {
		return err
	}
	if err := s.save(next); err != nil {
		return err
	}
	s.file = next
	return nil
}

func cloneFile(file File) File {
	out := File{SchemaVersion: file.SchemaVersion, Agents: make([]agentspec.Agent, 0, len(file.Agents)), Models: slices.Clone(file.Models)}
	for _, agent := range file.Agents {
		out.Agents = append(out.Agents, cloneAgent(agent))
	}
	return out
}

// cloneAgent gives an agent its own slices, so a caller editing what it was
// handed cannot reach into the catalog.
func cloneAgent(agent agentspec.Agent) agentspec.Agent {
	agent.ListModels = slices.Clone(agent.ListModels)
	settings := make([]agentspec.Setting, 0, len(agent.Settings))
	for _, setting := range agent.Settings {
		setting.Choices = slices.Clone(setting.Choices)
		settings = append(settings, setting)
	}
	agent.Settings = settings
	return agent
}

// Agents is every described agent in selector order, with its settings
// (implements agentspec.Source). Never nil: this is served as JSON, and a client
// promised an array should not have to tell `[]` from `null`.
func (s *Store) Agents() []agentspec.Agent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneFile(s.file).Agents
}

// Agent is one description by agent id (implements agentspec.Source).
func (s *Store) Agent(id string) (agentspec.Agent, bool) {
	agent, err := s.AgentByID(id)
	return agent, err == nil
}

// AgentByID returns one agent with its settings, reporting ErrNotFound for an id
// the catalog does not hold.
func (s *Store) AgentByID(id string) (agentspec.Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if agentIndex := indexOfAgent(s.file, id); agentIndex >= 0 {
		return cloneAgent(s.file.Agents[agentIndex]), nil
	}
	return agentspec.Agent{}, fmt.Errorf("%w: agent %q", ErrNotFound, id)
}

func indexOfAgent(file File, id string) int {
	return slices.IndexFunc(file.Agents, func(agent agentspec.Agent) bool { return agent.ID == id })
}

// enclaveToolTaken reports whether another agent already answers to the
// effective Enclave tool of agent: the explicit binding, or the id for an agent
// without one. Two agents on one tool would make a container's sessions
// ambiguous, so the second is refused.
func enclaveToolTaken(file File, agent agentspec.Agent) bool {
	tool := agent.EnclaveToolName()
	for _, existing := range file.Agents {
		if existing.ID != agent.ID && existing.EnclaveToolName() == tool {
			return true
		}
	}
	return false
}

// InsertAgent adds an agent description at the end of the selector order, with
// no settings yet: those are added one at a time (InsertSetting).
func (s *Store) InsertAgent(agent agentspec.Agent) error {
	if err := validateAgent(agent); err != nil {
		return err
	}
	return s.mutate(func(file *File) error {
		if indexOfAgent(*file, agent.ID) >= 0 {
			return fmt.Errorf("%w: agent %q", ErrDuplicate, agent.ID)
		}
		if enclaveToolTaken(*file, agent) {
			return fmt.Errorf("%w: Enclave tool %q", ErrDuplicate, agent.EnclaveToolName())
		}
		agent.Settings = []agentspec.Setting{}
		file.Agents = append(file.Agents, normalizeAgent(cloneAgent(agent)))
		return nil
	})
}

// UpdateAgent replaces an agent's description, keeping its id, its position and
// its settings: correcting a binary or a display name must not move it in the
// selector or touch its form.
func (s *Store) UpdateAgent(agent agentspec.Agent) error {
	if err := validateAgent(agent); err != nil {
		return err
	}
	return s.mutate(func(file *File) error {
		agentIndex := indexOfAgent(*file, agent.ID)
		if agentIndex < 0 {
			return fmt.Errorf("%w: agent %q", ErrNotFound, agent.ID)
		}
		if enclaveToolTaken(*file, agent) {
			return fmt.Errorf("%w: Enclave tool %q", ErrDuplicate, agent.EnclaveToolName())
		}
		agent.Settings = file.Agents[agentIndex].Settings
		file.Agents[agentIndex] = normalizeAgent(cloneAgent(agent))
		return nil
	})
}

// DeleteAgent removes an agent description and, with it, every setting and
// model on it: the knobs and the menu of an agent nothing can launch have
// nowhere to appear. Deleting one that is already gone reports ErrNotFound
// rather than succeeding quietly: the caller is naming a specific row, and a
// wrong id is worth hearing about.
func (s *Store) DeleteAgent(id string) error {
	return s.mutate(func(file *File) error {
		agentIndex := indexOfAgent(*file, id)
		if agentIndex < 0 {
			return fmt.Errorf("%w: agent %q", ErrNotFound, id)
		}
		file.Agents = slices.Delete(file.Agents, agentIndex, agentIndex+1)
		file.Models = slices.DeleteFunc(file.Models, func(model Model) bool { return model.Agent == id })
		return nil
	})
}

// locateSetting finds a setting by id: the index of its agent and its index in
// that agent's settings, or -1, -1.
func locateSetting(file File, id string) (int, int) {
	for i, agent := range file.Agents {
		for j, setting := range agent.Settings {
			if setting.ID == id {
				return i, j
			}
		}
	}
	return -1, -1
}

// settingTaken reports whether any agent already has a setting with this id.
func settingTaken(file File, id string) bool {
	agentIndex, _ := locateSetting(file, id)
	return agentIndex >= 0
}

// fieldTaken reports whether another setting of the agent already writes field.
func fieldTaken(agent agentspec.Agent, field, exceptID string) bool {
	for _, existing := range agent.Settings {
		if existing.ID != exceptID && existing.Field == field {
			return true
		}
	}
	return false
}

// SettingByID reads one setting, reporting ErrNotFound for an id the catalog does
// not hold.
func (s *Store) SettingByID(id string) (agentspec.Setting, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	agentIndex, settingIndex := locateSetting(s.file, id)
	if agentIndex < 0 {
		return agentspec.Setting{}, fmt.Errorf("%w: setting %q", ErrNotFound, id)
	}
	setting := s.file.Agents[agentIndex].Settings[settingIndex]
	setting.Choices = slices.Clone(setting.Choices)
	return setting, nil
}

// InsertSetting adds a launch knob at the end of that agent's form.
func (s *Store) InsertSetting(setting agentspec.Setting) error {
	if err := validateSetting(setting); err != nil {
		return err
	}
	return s.mutate(func(file *File) error {
		agentIndex := indexOfAgent(*file, setting.Agent)
		if agentIndex < 0 {
			return fmt.Errorf("%w: %q", ErrUnknownAgent, setting.Agent)
		}
		if settingTaken(*file, setting.ID) {
			return fmt.Errorf("%w: setting %q", ErrDuplicate, setting.ID)
		}
		if fieldTaken(file.Agents[agentIndex], setting.Field, setting.ID) {
			return fmt.Errorf("%w: %s already has a %q setting", ErrDuplicate, setting.Agent, setting.Field)
		}
		file.Agents[agentIndex].Settings = append(file.Agents[agentIndex].Settings, normalizeSetting(setting))
		return nil
	})
}

// UpdateSetting replaces a setting, keeping its id, its agent and its position:
// rewording a label or fixing a flag must not move the field in the form or hand
// the knob to another agent. A missing row and a colliding field are told apart.
func (s *Store) UpdateSetting(setting agentspec.Setting) error {
	return s.mutate(func(file *File) error {
		agentIndex, settingIndex := locateSetting(*file, setting.ID)
		if agentIndex < 0 {
			return fmt.Errorf("%w: setting %q", ErrNotFound, setting.ID)
		}
		setting.Agent = file.Agents[agentIndex].ID
		if err := validateSetting(setting); err != nil {
			return err
		}
		if fieldTaken(file.Agents[agentIndex], setting.Field, setting.ID) {
			return fmt.Errorf("%w: %s already has a %q setting", ErrDuplicate, setting.Agent, setting.Field)
		}
		file.Agents[agentIndex].Settings[settingIndex] = normalizeSetting(setting)
		return nil
	})
}

// DeleteSetting removes a launch knob.
func (s *Store) DeleteSetting(id string) error {
	return s.mutate(func(file *File) error {
		agentIndex, settingIndex := locateSetting(*file, id)
		if agentIndex < 0 {
			return fmt.Errorf("%w: setting %q", ErrNotFound, id)
		}
		file.Agents[agentIndex].Settings = slices.Delete(file.Agents[agentIndex].Settings, settingIndex, settingIndex+1)
		return nil
	})
}

func normalizeSetting(setting agentspec.Setting) agentspec.Setting {
	setting.Choices = slices.Clone(setting.Choices)
	if len(setting.Choices) == 0 {
		setting.Choices = nil
	}
	return setting
}

// Models is what an agent offers, in order: the read the providers make when
// they build a session's Model field (implements agentmodel.Catalog).
func (s *Store) Models(agent string) []string {
	entries := s.ListModelsFor(agent)
	models := make([]string, 0, len(entries))
	for _, entry := range entries {
		models = append(models, entry.Model)
	}
	return models
}

// ListModels returns every model entry, grouped by agent and in the order each
// agent's models are offered in. Never nil: this is served as JSON.
func (s *Store) ListModels() []Model {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries := slices.Clone(s.file.Models)
	slices.SortStableFunc(entries, func(a, b Model) int { return strings.Compare(a.Agent, b.Agent) })
	return entries
}

// ListModelsFor returns one agent's model entries, in the order they are offered.
func (s *Store) ListModelsFor(agent string) []Model {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries := []Model{}
	for _, entry := range s.file.Models {
		if entry.Agent == agent {
			entries = append(entries, entry)
		}
	}
	return entries
}

func indexOfModel(file File, id string) int {
	return slices.IndexFunc(file.Models, func(model Model) bool { return model.ID == id })
}

// modelOffered reports whether the agent already offers model, other than in
// the entry exceptID.
func modelOffered(file File, agent, model, exceptID string) bool {
	for _, existing := range file.Models {
		if existing.ID != exceptID && existing.Agent == agent && existing.Model == model {
			return true
		}
	}
	return false
}

// InsertModel adds a model to an agent's list, at the end of it. The agent must
// be described (ErrUnknownAgent), and the model must not be one it already
// offers (ErrModelDuplicate).
func (s *Store) InsertModel(entry Model) error {
	return s.mutate(func(file *File) error {
		if indexOfAgent(*file, entry.Agent) < 0 {
			return fmt.Errorf("%w: %q", ErrUnknownAgent, entry.Agent)
		}
		if indexOfModel(*file, entry.ID) >= 0 {
			return fmt.Errorf("%w: id %q", ErrModelDuplicate, entry.ID)
		}
		if modelOffered(*file, entry.Agent, entry.Model, entry.ID) {
			return ErrModelDuplicate
		}
		file.Models = append(file.Models, entry)
		return nil
	})
}

// UpdateModel replaces an entry's model, keeping its agent and its position:
// correcting a model ID must not move it in the menu. It returns the stored
// entry, so a caller echoing it reports the agent the entry belongs to rather
// than only what it sent. A missing entry and a colliding model are told apart.
func (s *Store) UpdateModel(id, model string) (Model, error) {
	var updated Model
	err := s.mutate(func(file *File) error {
		modelIndex := indexOfModel(*file, id)
		if modelIndex < 0 {
			return ErrModelNotFound
		}
		if modelOffered(*file, file.Models[modelIndex].Agent, model, id) {
			return fmt.Errorf("%w: %s already offers %q", ErrModelDuplicate, file.Models[modelIndex].Agent, model)
		}
		file.Models[modelIndex].Model = model
		updated = file.Models[modelIndex]
		return nil
	})
	return updated, err
}

// DeleteModel removes an entry. Deleting one that is already gone reports
// ErrModelNotFound rather than succeeding quietly.
func (s *Store) DeleteModel(id string) error {
	return s.mutate(func(file *File) error {
		modelIndex := indexOfModel(*file, id)
		if modelIndex < 0 {
			return ErrModelNotFound
		}
		file.Models = slices.Delete(file.Models, modelIndex, modelIndex+1)
		return nil
	})
}

// validateAgent refuses a description nothing could launch. Everything else about
// an agent is the user's business: a binary that is not installed is simply never
// offered, which is not an error but the normal state of most of this catalog.
func validateAgent(agent agentspec.Agent) error {
	if strings.TrimSpace(agent.ID) == "" {
		return fmt.Errorf("%w: an agent needs an id", ErrInvalid)
	}
	if strings.TrimSpace(agent.Binary) == "" {
		return fmt.Errorf("%w: agent %q needs a binary to run", ErrInvalid, agent.ID)
	}
	if strings.TrimSpace(agent.Display) == "" {
		return fmt.Errorf("%w: agent %q needs a display name", ErrInvalid, agent.ID)
	}
	return nil
}

// validateSetting refuses a knob nothing could act on: an unknown value type or
// delivery kind (the providers would skip it, so the form would offer a field that
// does nothing), and a delivery that names no flag or variable.
//
// It deliberately does not check that a flag exists on the CLI or that a choice is
// one the agent accepts. The agent is the authority on both, and a wrong one fails
// at launch with the agent's own message rather than being second-guessed here.
func validateSetting(setting agentspec.Setting) error {
	if strings.TrimSpace(setting.ID) == "" {
		return fmt.Errorf("%w: a setting needs an id", ErrInvalid)
	}
	if strings.TrimSpace(setting.Field) == "" {
		return fmt.Errorf("%w: setting %q needs a config field name", ErrInvalid, setting.ID)
	}
	if strings.TrimSpace(setting.Title) == "" {
		return fmt.Errorf("%w: setting %q needs a title", ErrInvalid, setting.ID)
	}
	switch setting.Type {
	case agentspec.TypeString, agentspec.TypeBoolean:
	default:
		return fmt.Errorf("%w: setting %q has value type %q, which is neither %q nor %q",
			ErrInvalid, setting.ID, setting.Type, agentspec.TypeString, agentspec.TypeBoolean)
	}
	switch setting.Role {
	case "", agentspec.RoleModel, agentspec.RolePrompt, agentspec.RoleTitle:
	default:
		return fmt.Errorf("%w: setting %q has role %q, which the daemon does not know",
			ErrInvalid, setting.ID, setting.Role)
	}
	switch setting.Delivery {
	case agentspec.Flag, agentspec.BoolFlag, agentspec.Env:
		if strings.TrimSpace(setting.DeliveryArg) == "" {
			return fmt.Errorf("%w: setting %q delivers by %q, which needs a flag or variable name",
				ErrInvalid, setting.ID, setting.Delivery)
		}
	case agentspec.Assign:
		if strings.TrimSpace(setting.DeliveryArg) == "" || strings.TrimSpace(setting.DeliveryKey) == "" {
			return fmt.Errorf("%w: setting %q delivers by %q, which needs both a flag and a key to assign",
				ErrInvalid, setting.ID, setting.Delivery)
		}
	case agentspec.Positional:
	default:
		return fmt.Errorf("%w: setting %q delivers by %q, which is not a delivery the daemon knows",
			ErrInvalid, setting.ID, setting.Delivery)
	}
	return nil
}
