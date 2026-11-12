package persist

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
)

// StateFileName is the JSON file inside the data directory that holds the
// daemon's persisted state: projects, workstreams, last-known sessions, custom
// session titles and session launch configs.
const StateFileName = "state.json"

// stateSchemaVersion is the state file's schema. A file with another version
// refuses to load rather than being half-read.
const stateSchemaVersion = 1

// Snapshot is what the store persists: the user-authored tree plus the
// last-known sessions, without any live or placeholder state. It is the one
// projection of the store that reaches disk, so a field is persisted exactly
// when a record below carries it.
type Snapshot struct {
	Projects []ProjectRecord `json:"projects"`
	// SessionTitles are the user-chosen session titles, keyed by session ID.
	SessionTitles map[string]CustomTitle `json:"sessionTitles"`
	// LaunchConfigs are what each session was last started with, keyed by
	// session ID.
	LaunchConfigs map[string]LaunchConfig `json:"launchConfigs"`
}

// ProjectRecord is the persisted shape of a project.
type ProjectRecord struct {
	ID          string             `json:"id"`
	Name        string             `json:"name"`
	Path        string             `json:"path"`
	GitBacked   bool               `json:"gitBacked"`
	Workstreams []WorkstreamRecord `json:"workstreams"`
}

// WorkstreamRecord is the persisted shape of a workstream: its identity and
// what its creator and provider recorded, never its runtime state.
type WorkstreamRecord struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Provider     string          `json:"provider"`
	StrategyID   string          `json:"strategyId"`
	Branch       string          `json:"branch,omitempty"`
	WorkingDir   string          `json:"workingDir,omitempty"`
	ProviderData json.RawMessage `json:"providerData,omitempty"`
	CreatorData  json.RawMessage `json:"creatorData,omitempty"`
	Sessions     []SessionRecord `json:"sessions"`
}

// SessionRecord is the persisted shape of a session. Provider is kept because
// the reconciler routes and retains a restored session by it; a session that
// loads without one is dropped as a ghost.
type SessionRecord struct {
	ID           string          `json:"id"`
	Title        string          `json:"title"`
	Agent        string          `json:"agent"`
	Provider     string          `json:"provider,omitempty"`
	ProviderData json.RawMessage `json:"providerData,omitempty"`
	EndedAt      *time.Time      `json:"endedAt,omitempty"`
}

// CustomTitle is a user-chosen display title for a session, with the project it
// belongs to: the scope a removed project takes its titles with, and the one the
// store's prune walks.
type CustomTitle struct {
	ProjectID string `json:"projectId"`
	Title     string `json:"title"`
}

// LaunchConfig is what a session was last started with: the provider and option
// that started it and the create-form config the user submitted. The project is
// the scope a removed project takes it with, like a CustomTitle's.
type LaunchConfig struct {
	ProjectID  string          `json:"projectId"`
	ProviderID string          `json:"providerId"`
	OptionID   string          `json:"optionId"`
	Config     json.RawMessage `json:"config,omitempty"`
}

// stateFile is the on-disk envelope around a Snapshot.
type stateFile struct {
	SchemaVersion int      `json:"schemaVersion"`
	State         Snapshot `json:"state"`
}

// EmptySnapshot is a snapshot with nothing in it and every collection
// allocated, so it marshals to the same bytes as a store that has nothing.
func EmptySnapshot() Snapshot {
	return Snapshot{
		Projects:      []ProjectRecord{},
		SessionTitles: map[string]CustomTitle{},
		LaunchConfigs: map[string]LaunchConfig{},
	}
}

// LoadState reads the state file at path. A missing file is an empty snapshot,
// the state of a fresh install; a file that does not parse or carries another
// schema version is an error, so a damaged file aborts startup rather than
// being silently replaced by nothing.
func LoadState(path string) (Snapshot, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return EmptySnapshot(), nil
	}
	if err != nil {
		return Snapshot{}, fmt.Errorf("reading %s: %w", path, err)
	}
	var file stateFile
	if err := json.Unmarshal(data, &file); err != nil {
		return Snapshot{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	if file.SchemaVersion != stateSchemaVersion {
		return Snapshot{}, fmt.Errorf("%s: schema version %d, want %d", path, file.SchemaVersion, stateSchemaVersion)
	}
	return normalize(file.State), nil
}

// normalize gives every collection a non-nil value and every raw JSON field
// its compact form, so a snapshot read from a file compares and marshals like
// one the store built: the file is indented for people, but what the providers
// report is compact, and a restored record must equal a re-reported one.
func normalize(snapshot Snapshot) Snapshot {
	if snapshot.Projects == nil {
		snapshot.Projects = []ProjectRecord{}
	}
	if snapshot.SessionTitles == nil {
		snapshot.SessionTitles = map[string]CustomTitle{}
	}
	if snapshot.LaunchConfigs == nil {
		snapshot.LaunchConfigs = map[string]LaunchConfig{}
	}
	for i := range snapshot.Projects {
		if snapshot.Projects[i].Workstreams == nil {
			snapshot.Projects[i].Workstreams = []WorkstreamRecord{}
		}
		for j := range snapshot.Projects[i].Workstreams {
			workstream := &snapshot.Projects[i].Workstreams[j]
			workstream.ProviderData = compact(workstream.ProviderData)
			workstream.CreatorData = compact(workstream.CreatorData)
			if workstream.Sessions == nil {
				workstream.Sessions = []SessionRecord{}
			}
			for k := range workstream.Sessions {
				workstream.Sessions[k].ProviderData = compact(workstream.Sessions[k].ProviderData)
			}
		}
	}
	for sessionID, launch := range snapshot.LaunchConfigs {
		launch.Config = compact(launch.Config)
		snapshot.LaunchConfigs[sessionID] = launch
	}
	return snapshot
}

// compact is raw JSON without insignificant whitespace, or nil for none. The
// file was parsed already, so what is here is valid JSON and Compact cannot fail.
func compact(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return raw
	}
	return buf.Bytes()
}

// encodeState is the bytes of a snapshot on disk: indented like settings.json,
// and deterministic (struct field order, sorted map keys), so an unchanged
// snapshot encodes to identical bytes.
func encodeState(snapshot Snapshot) ([]byte, error) {
	data, err := json.MarshalIndent(stateFile{SchemaVersion: stateSchemaVersion, State: snapshot}, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshaling state: %w", err)
	}
	return append(data, '\n'), nil
}

// DomainProjects is the domain tree the snapshot describes, the shape the store is
// built from. Every session starts as SessionIdle: live states come from the
// providers, not from storage.
func (s Snapshot) DomainProjects() []domain.Project {
	projects := make([]domain.Project, 0, len(s.Projects))
	for _, record := range s.Projects {
		project := domain.Project{
			ID:          record.ID,
			Name:        record.Name,
			Path:        record.Path,
			GitBacked:   record.GitBacked,
			Workstreams: make([]domain.Workstream, 0, len(record.Workstreams)),
		}
		for _, workstreamRecord := range record.Workstreams {
			workstream := domain.Workstream{
				ID:           workstreamRecord.ID,
				Name:         workstreamRecord.Name,
				Provider:     workstreamRecord.Provider,
				StrategyID:   workstreamRecord.StrategyID,
				Branch:       workstreamRecord.Branch,
				WorkingDir:   workstreamRecord.WorkingDir,
				ProviderData: workstreamRecord.ProviderData,
				CreatorData:  workstreamRecord.CreatorData,
				Sessions:     make([]domain.Session, 0, len(workstreamRecord.Sessions)),
			}
			for _, sessionRecord := range workstreamRecord.Sessions {
				workstream.Sessions = append(workstream.Sessions, domain.Session{
					ID:           sessionRecord.ID,
					Title:        sessionRecord.Title,
					Agent:        sessionRecord.Agent,
					Provider:     sessionRecord.Provider,
					ProviderData: sessionRecord.ProviderData,
					State:        domain.SessionIdle,
					EndedAt:      sessionRecord.EndedAt,
				})
			}
			project.Workstreams = append(project.Workstreams, workstream)
		}
		projects = append(projects, project)
	}
	return projects
}

// RecordProject is the persisted shape of a project, without its workstreams:
// the caller decides which of those are written (the store skips placeholders).
func RecordProject(project domain.Project) ProjectRecord {
	return ProjectRecord{
		ID:          project.ID,
		Name:        project.Name,
		Path:        project.Path,
		GitBacked:   project.GitBacked,
		Workstreams: []WorkstreamRecord{},
	}
}

// RecordWorkstream is the persisted shape of a workstream, without its
// sessions, for the same reason RecordProject leaves out the workstreams.
func RecordWorkstream(workstream domain.Workstream) WorkstreamRecord {
	return WorkstreamRecord{
		ID:           workstream.ID,
		Name:         workstream.Name,
		Provider:     workstream.Provider,
		StrategyID:   workstream.StrategyID,
		Branch:       workstream.Branch,
		WorkingDir:   workstream.WorkingDir,
		ProviderData: workstream.ProviderData,
		CreatorData:  workstream.CreatorData,
		Sessions:     []SessionRecord{},
	}
}

// RecordSession is the persisted shape of a session.
func RecordSession(session domain.Session) SessionRecord {
	return SessionRecord{
		ID:           session.ID,
		Title:        session.Title,
		Agent:        session.Agent,
		Provider:     session.Provider,
		ProviderData: session.ProviderData,
		EndedAt:      session.EndedAt,
	}
}
