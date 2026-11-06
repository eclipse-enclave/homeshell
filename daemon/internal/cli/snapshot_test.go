package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

// decodeTestSnapshot parses the shared fixture the way fetchSnapshot does.
func decodeTestSnapshot(t *testing.T) *snapshot {
	t.Helper()
	var snap snapshot
	if err := json.Unmarshal([]byte(testSnapshotJSON), &snap); err != nil {
		t.Fatalf("decoding the fixture: %v", err)
	}
	snap.raw = []byte(testSnapshotJSON)
	return &snap
}

func TestSnapshotDecodesTheDaemonPayload(t *testing.T) {
	snap := decodeTestSnapshot(t)

	if len(snap.Projects) != 2 {
		t.Fatalf("got %d projects, want 2", len(snap.Projects))
	}
	alpha := snap.Projects[0]
	if alpha.ID != "proj-1" || alpha.Name != "alpha" || alpha.Effective != "running" {
		t.Errorf("project = %+v, want proj-1/alpha/running", alpha.projectFields)
	}
	if len(alpha.Workstreams) != 1 {
		t.Fatalf("got %d workstreams in alpha, want 1", len(alpha.Workstreams))
	}
	feature := alpha.Workstreams[0]
	if feature.Strategy != "worktree" || len(feature.OpenModes) != 2 {
		t.Errorf("workstream = %+v, want the worktree strategy and two open modes", feature.workstreamFields)
	}
	if len(feature.Sessions) != 1 || feature.Sessions[0].Agent != "Claude Code" {
		t.Errorf("sessions = %+v, want one Claude Code session", feature.Sessions)
	}
}

// The raw object each type keeps is what --json prints, so it must be the bytes
// the daemon sent rather than a re-encoding — a field this CLI has no struct field
// for still has to survive.
func TestSnapshotKeepsRawObjects(t *testing.T) {
	const payload = `{"projects":[{"id":"proj-1","name":"alpha","futureField":{"deep":true},` +
		`"workstreams":[{"id":"ws-1","name":"feature","alsoNew":7,` +
		`"sessions":[{"id":"sess-1","title":"t","brandNew":"x"}]}]}]}`
	var snap snapshot
	if err := json.Unmarshal([]byte(payload), &snap); err != nil {
		t.Fatal(err)
	}

	project := snap.Projects[0]
	if !strings.Contains(string(project.raw), `"futureField":{"deep":true}`) {
		t.Errorf("project raw = %s, want the unknown field preserved", project.raw)
	}
	workstream := project.Workstreams[0]
	if !strings.Contains(string(workstream.raw), `"alsoNew":7`) {
		t.Errorf("workstream raw = %s, want the unknown field preserved", workstream.raw)
	}
	session := workstream.Sessions[0]
	if !strings.Contains(string(session.raw), `"brandNew":"x"`) {
		t.Errorf("session raw = %s, want the unknown field preserved", session.raw)
	}
}

func TestFindProject(t *testing.T) {
	snap := decodeTestSnapshot(t)

	tests := []struct {
		name    string
		ref     string
		wantID  string
		wantErr string
	}{
		{name: "by ID", ref: "proj-2", wantID: "proj-2"},
		{name: "by name", ref: "alpha", wantID: "proj-1"},
		{name: "unknown", ref: "gamma", wantErr: `no project matches "gamma"`},
		{name: "empty", ref: "", wantErr: "a project is required"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			found, err := snap.findProject(tt.ref)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected an error, got %+v", found.projectFields)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error = %q, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if found.ID != tt.wantID {
				t.Errorf("project ID = %q, want %q", found.ID, tt.wantID)
			}
		})
	}
}

func TestFindWorkstream(t *testing.T) {
	snap := decodeTestSnapshot(t)

	tests := []struct {
		name       string
		ref        string
		projectRef string
		wantID     string
		wantErr    []string
	}{
		{name: "by ID", ref: "ws-2", wantID: "ws-2"},
		{name: "by unique name", ref: "extra", wantID: "ws-3"},
		{
			// "feature" exists in both projects: guessing would remove the wrong
			// workstream, so this has to be refused with the way to disambiguate.
			name:    "an ambiguous name is refused with the IDs",
			ref:     "feature",
			wantErr: []string{"2 workstreams are named", "ws-1", "ws-2", "by ID"},
		},
		{name: "a name is unique once scoped to a project", ref: "feature", projectRef: "beta", wantID: "ws-2"},
		{name: "scoped out of the project it lives in", ref: "ws-1", projectRef: "beta", wantErr: []string{`no workstream matches "ws-1"`}},
		{name: "an unknown project scope", ref: "ws-1", projectRef: "gamma", wantErr: []string{`no project matches "gamma"`}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			found, err := snap.findWorkstream(tt.ref, tt.projectRef)
			if len(tt.wantErr) > 0 {
				if err == nil {
					t.Fatalf("expected an error, got %s", found.Workstream.ID)
				}
				for _, want := range tt.wantErr {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error = %q, want it to contain %q", err, want)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if found.Workstream.ID != tt.wantID {
				t.Errorf("workstream ID = %q, want %q", found.Workstream.ID, tt.wantID)
			}
		})
	}
}

// A lookup has to yield the owning project too: the API addresses a workstream as
// project + workstream, so losing the owner means building the wrong path.
func TestFindWorkstreamCarriesItsProject(t *testing.T) {
	snap := decodeTestSnapshot(t)

	found, err := snap.findWorkstream("ws-3", "")
	if err != nil {
		t.Fatal(err)
	}
	if found.Project.ID != "proj-2" {
		t.Errorf("owning project = %q, want proj-2", found.Project.ID)
	}
	if got := workstreamPath(found); got != "/api/projects/proj-2/workstreams/ws-3" {
		t.Errorf("path = %q, want the project and workstream in it", got)
	}
}

func TestFindSession(t *testing.T) {
	snap := decodeTestSnapshot(t)

	tests := []struct {
		name          string
		ref           string
		projectRef    string
		workstreamRef string
		wantID        string
		wantPath      string
		wantErr       string
	}{
		{
			name: "by ID", ref: "sess-1", wantID: "sess-1",
			wantPath: "/api/projects/proj-1/workstreams/ws-1/sessions/sess-1",
		},
		{
			name: "by the title the agent reports", ref: "Implement it", wantID: "sess-1",
			wantPath: "/api/projects/proj-1/workstreams/ws-1/sessions/sess-1",
		},
		{
			name: "scoped to a workstream", ref: "sess-2", workstreamRef: "extra", wantID: "sess-2",
			wantPath: "/api/projects/proj-2/workstreams/ws-3/sessions/sess-2",
		},
		{name: "unknown", ref: "sess-9", wantErr: `no session matches "sess-9"`},
		{name: "outside the workstream scope", ref: "sess-1", workstreamRef: "extra", wantErr: `no session matches "sess-1"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			found, err := snap.findSession(tt.ref, tt.projectRef, tt.workstreamRef)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected an error, got %s", found.Session.ID)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error = %q, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if found.Session.ID != tt.wantID {
				t.Errorf("session ID = %q, want %q", found.Session.ID, tt.wantID)
			}
			if got := sessionPath(found); got != tt.wantPath {
				t.Errorf("path = %q, want %q", got, tt.wantPath)
			}
		})
	}
}

// An empty title must not match every untitled session: sess-2 has none, so an
// empty reference is a usage error rather than a silent hit.
func TestFindSessionRejectsAnEmptyReference(t *testing.T) {
	snap := decodeTestSnapshot(t)

	if _, err := snap.findSession("", "", ""); err == nil {
		t.Fatal("expected an error for an empty session reference")
	}
}

func TestFlattening(t *testing.T) {
	snap := decodeTestSnapshot(t)

	t.Run("workstreams across every project keep the daemon's order", func(t *testing.T) {
		entries, err := snap.workstreams("")
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, entry := range entries {
			ids = append(ids, entry.Workstream.ID)
		}
		if strings.Join(ids, ",") != "ws-1,ws-2,ws-3" {
			t.Errorf("workstreams = %v, want ws-1,ws-2,ws-3", ids)
		}
	})

	t.Run("workstreams narrowed to a project", func(t *testing.T) {
		entries, err := snap.workstreams("alpha")
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 || entries[0].Workstream.ID != "ws-1" {
			t.Errorf("workstreams = %+v, want only ws-1", entries)
		}
	})

	t.Run("sessions across every workstream", func(t *testing.T) {
		entries, err := snap.sessions("", "")
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 2 {
			t.Fatalf("got %d sessions, want 2", len(entries))
		}
		if entries[0].Workstream.ID != "ws-1" || entries[1].Workstream.ID != "ws-3" {
			t.Errorf("sessions came from %q and %q, want ws-1 and ws-3",
				entries[0].Workstream.ID, entries[1].Workstream.ID)
		}
	})

	t.Run("a workstream with no sessions yields none", func(t *testing.T) {
		entries, err := snap.sessions("", "ws-2")
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Errorf("got %d sessions, want none", len(entries))
		}
	})
}
