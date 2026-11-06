package cli

// The daemon's state as the CLI reads it: one GET /api/projects payload, plus the
// lookups that turn a reference a user typed into the project/workstream/session
// IDs the API's paths are built from.
//
// The types below mirror the JSON of that endpoint rather than importing the
// daemon's domain package: a client verb is a pure API client, so this
// surface survives a daemon rewrite exactly as the e2e suite does. Every state
// here is *read* — aggregation happens once, in the daemon, and the CLI only
// prints what it is given.
//
// Each type also keeps the raw JSON object it decoded from, so `--json` on a
// filtered list prints the daemon's own bytes, fields this CLI does not know
// included.
//
// They are named api* because this package also holds the `serve` verb, which
// imports the persist/{project,workstream,session} packages — the bare names are
// taken.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/eclipsesource/homeshell/daemon/internal/apiclient"
)

// snapshot is the whole payload of GET /api/projects.
type snapshot struct {
	Projects []apiProject `json:"projects"`
	raw      []byte
}

// projectFields is the decode target for a project. It is a separate type so
// apiProject's UnmarshalJSON can capture the raw object and then decode into these
// fields without recursing into itself.
type projectFields struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Path        string          `json:"path"`
	GitBacked   bool            `json:"gitBacked"`
	State       string          `json:"state"`
	Effective   string          `json:"effectiveState"`
	Workstreams []apiWorkstream `json:"workstreams"`
}

type apiProject struct {
	projectFields
	raw json.RawMessage
}

func (p *apiProject) UnmarshalJSON(data []byte) error {
	p.raw = bytes.Clone(data)
	return json.Unmarshal(data, &p.projectFields)
}

type workstreamFields struct {
	ID            string       `json:"id"`
	Name          string       `json:"name"`
	Provider      string       `json:"provider"`
	StrategyID    string       `json:"strategyId"`
	Strategy      string       `json:"strategy"`
	Branch        string       `json:"branch"`
	WorkingDir    string       `json:"workingDir"`
	State         string       `json:"state"`
	RuntimeState  string       `json:"runtimeState"`
	Effective     string       `json:"effectiveState"`
	StatusMessage string       `json:"statusMessage"`
	OpenModes     []openMode   `json:"openModes"`
	Materialized  bool         `json:"materialized"`
	Sessions      []apiSession `json:"sessions"`
}

type apiWorkstream struct {
	workstreamFields
	raw json.RawMessage
}

func (w *apiWorkstream) UnmarshalJSON(data []byte) error {
	w.raw = bytes.Clone(data)
	return json.Unmarshal(data, &w.workstreamFields)
}

// openMode is one way the daemon offers to hand a workstream to the user. The ID
// is echoed back on `workstream open`; the label is the daemon's to author,
// so the CLI only ever displays it.
type openMode struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type sessionFields struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Agent    string `json:"agent"`
	Provider string `json:"provider"`
	State    string `json:"state"`
}

type apiSession struct {
	sessionFields
	raw json.RawMessage
}

func (s *apiSession) UnmarshalJSON(data []byte) error {
	s.raw = bytes.Clone(data)
	return json.Unmarshal(data, &s.sessionFields)
}

// fetchSnapshot reads the daemon's full state. Every list verb reads this one
// payload and filters it, so the CLI never asks the daemon to compute a view for
// it and never computes one itself.
func fetchSnapshot(ctx context.Context, client *apiclient.Client) (*snapshot, error) {
	payload, err := requestJSON(ctx, client, http.MethodGet, "/api/projects", nil)
	if err != nil {
		return nil, err
	}
	var snap snapshot
	if err := json.Unmarshal(payload, &snap); err != nil {
		return nil, fmt.Errorf("decoding GET /api/projects: %w", err)
	}
	snap.raw = payload
	return &snap, nil
}

// --- lookups ---

// workstreamEntry is a workstream together with the project that owns it: the API
// addresses a workstream as project + workstream, so a lookup has to yield both.
type workstreamEntry struct {
	Project    *apiProject
	Workstream *apiWorkstream
}

// sessionEntry is a session with the workstream and project that contain it, for
// the same reason.
type sessionEntry struct {
	Project    *apiProject
	Workstream *apiWorkstream
	Session    *apiSession
}

// findProject resolves a project reference: an ID as printed by `project list`,
// or the project's name.
func (s *snapshot) findProject(ref string) (*apiProject, error) {
	return resolveRef("project", ref, s.Projects, func(candidate *apiProject) (string, string) {
		return candidate.ID, candidate.Name
	})
}

// workstreams flattens the snapshot's workstreams, optionally narrowed to one
// project. The order is the daemon's, so `list` output matches the UI's ordering.
func (s *snapshot) workstreams(projectRef string) ([]workstreamEntry, error) {
	projects := make([]*apiProject, 0, len(s.Projects))
	if projectRef != "" {
		owner, err := s.findProject(projectRef)
		if err != nil {
			return nil, err
		}
		projects = append(projects, owner)
	} else {
		for i := range s.Projects {
			projects = append(projects, &s.Projects[i])
		}
	}
	var entries []workstreamEntry
	for _, owner := range projects {
		for i := range owner.Workstreams {
			entries = append(entries, workstreamEntry{Project: owner, Workstream: &owner.Workstreams[i]})
		}
	}
	return entries, nil
}

// sessions flattens the snapshot's sessions, optionally narrowed to one
// workstream and/or project.
func (s *snapshot) sessions(projectRef, workstreamRef string) ([]sessionEntry, error) {
	candidates, err := s.workstreams(projectRef)
	if err != nil {
		return nil, err
	}
	if workstreamRef != "" {
		match, err := findWorkstreamIn(candidates, workstreamRef)
		if err != nil {
			return nil, err
		}
		candidates = []workstreamEntry{match}
	}
	var entries []sessionEntry
	for _, candidate := range candidates {
		for i := range candidate.Workstream.Sessions {
			entries = append(entries, sessionEntry{
				Project:    candidate.Project,
				Workstream: candidate.Workstream,
				Session:    &candidate.Workstream.Sessions[i],
			})
		}
	}
	return entries, nil
}

// findWorkstream resolves a workstream reference (ID or name), optionally scoped
// to a project when the same name is used in several.
func (s *snapshot) findWorkstream(ref, projectRef string) (workstreamEntry, error) {
	candidates, err := s.workstreams(projectRef)
	if err != nil {
		return workstreamEntry{}, err
	}
	return findWorkstreamIn(candidates, ref)
}

func findWorkstreamIn(candidates []workstreamEntry, ref string) (workstreamEntry, error) {
	match, err := resolveRef("workstream", ref, candidates, func(candidate *workstreamEntry) (string, string) {
		return candidate.Workstream.ID, candidate.Workstream.Name
	})
	if err != nil {
		return workstreamEntry{}, err
	}
	return *match, nil
}

// findSession resolves a session reference: its ID, or its title (which is what
// `session list` shows and what the agent reports live).
func (s *snapshot) findSession(ref, projectRef, workstreamRef string) (sessionEntry, error) {
	candidates, err := s.sessions(projectRef, workstreamRef)
	if err != nil {
		return sessionEntry{}, err
	}
	match, err := resolveRef("session", ref, candidates, func(candidate *sessionEntry) (string, string) {
		return candidate.Session.ID, candidate.Session.Title
	})
	if err != nil {
		return sessionEntry{}, err
	}
	return *match, nil
}

// resolveRef matches a reference a user typed against a set of candidates: an
// exact ID wins outright, else an exact name, and an ambiguous name is refused
// with the IDs to disambiguate with. Both are accepted because IDs are what the
// list verbs print and names are what a person remembers — and IDs are unique
// while names are not, which is why an ID match short-circuits.
func resolveRef[T any](kind, ref string, candidates []T, identify func(candidate *T) (id, name string)) (*T, error) {
	if strings.TrimSpace(ref) == "" {
		return nil, missingArgError("a " + kind)
	}
	var named []*T
	for i := range candidates {
		id, name := identify(&candidates[i])
		if id == ref {
			return &candidates[i], nil
		}
		if name == ref {
			named = append(named, &candidates[i])
		}
	}
	switch len(named) {
	case 0:
		return nil, fmt.Errorf("no %s matches %q — run \"homeshell %s list\" to see the ones the daemon knows", kind, ref, kind)
	case 1:
		return named[0], nil
	default:
		ids := make([]string, 0, len(named))
		for _, match := range named {
			id, _ := identify(match)
			ids = append(ids, id)
		}
		return nil, fmt.Errorf("%d %ss are named %q (%s) — name one by ID", len(named), kind, ref, strings.Join(ids, ", "))
	}
}
