package persist

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
)

// fixtureEndedAt is when fullSnapshot's session ended.
var fixtureEndedAt = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func fullSnapshot() Snapshot {
	endedAt := fixtureEndedAt
	return Snapshot{
		Projects: []ProjectRecord{{
			ID: "p1", Name: "alpha", Path: "/alpha", GitBacked: true,
			Workstreams: []WorkstreamRecord{{
				ID: "ws-1", Name: "feature", Provider: "core", StrategyID: "worktree", Branch: "feature/x",
				WorkingDir: "/alpha/.ws/x", ProviderData: json.RawMessage(`{"k":1}`), CreatorData: json.RawMessage(`{"c":"d"}`),
				Sessions: []SessionRecord{{
					ID: "sess-1", Title: "claude session", Agent: "claude", Provider: "enclave",
					ProviderData: json.RawMessage(`{"container":"abc"}`),
					EndedAt:      &endedAt,
				}},
			}},
		}},
		SessionTitles: map[string]CustomTitle{"sess-1": {ProjectID: "p1", Title: "Port the CLI"}},
		LaunchConfigs: map[string]LaunchConfig{"sess-1": {ProjectID: "p1", ProviderID: "enclave", OptionID: "claude", Config: json.RawMessage(`{"model":"opus"}`)}},
	}
}

func TestStateRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), StateFileName)
	saver := NewFileSaver(path)
	want := fullSnapshot()
	if err := saver.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := LoadState(path)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("LoadState = %+v\nwant %+v", got, want)
	}
}

func TestLoadStateOfAMissingFileIsEmpty(t *testing.T) {
	got, err := LoadState(filepath.Join(t.TempDir(), StateFileName))
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if !reflect.DeepEqual(got, EmptySnapshot()) {
		t.Errorf("LoadState of a missing file = %+v; want an empty snapshot", got)
	}
	if got.Projects == nil || got.SessionTitles == nil || got.LaunchConfigs == nil {
		t.Error("an empty snapshot must have its collections allocated")
	}
}

func TestLoadStateRefusesABadFile(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"corrupt JSON", "{not json", "parsing"},
		{"wrong schema version", `{"schemaVersion":99,"state":{}}`, "schema version 99"},
		{"missing schema version", `{"state":{}}`, "schema version 0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), StateFileName)
			if err := os.WriteFile(path, []byte(tt.content), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := LoadState(path)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("LoadState error = %v; want one mentioning %q", err, tt.want)
			}
		})
	}
}

func TestLoadStateAllocatesNestedCollections(t *testing.T) {
	path := filepath.Join(t.TempDir(), StateFileName)
	content := `{"schemaVersion":1,"state":{"projects":[{"id":"p1","name":"a","path":"/a","workstreams":[{"id":"ws-1","name":"ws","provider":"core","strategyId":"in-place"}]}]}}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadState(path)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got.Projects[0].Workstreams[0].Sessions == nil || got.SessionTitles == nil || got.LaunchConfigs == nil {
		t.Errorf("LoadState left a collection nil: %+v", got)
	}
}

// Two identical snapshots write once; a changed one writes again. The file's
// modification is observed through its bytes rather than its mtime, which is
// too coarse on some filesystems.
func TestFileSaverWritesOnlyChanges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, StateFileName)
	saver := NewFileSaver(path)
	first := fullSnapshot()
	if err := saver.Save(first); err != nil {
		t.Fatalf("first Save: %v", err)
	}
	written, _ := os.ReadFile(path)
	if err := os.WriteFile(path, []byte("sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := saver.Save(fullSnapshot()); err != nil {
		t.Fatalf("second Save: %v", err)
	}
	if data, _ := os.ReadFile(path); string(data) != "sentinel" {
		t.Error("an unchanged snapshot was written again")
	}
	changed := fullSnapshot()
	changed.Projects[0].Name = "beta"
	if err := saver.Save(changed); err != nil {
		t.Fatalf("third Save: %v", err)
	}
	data, _ := os.ReadFile(path)
	if string(data) == "sentinel" || string(data) == string(written) {
		t.Error("a changed snapshot was not written")
	}
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Errorf("temp file %s survived a successful save", entry.Name())
		}
	}
}

// A saver over a file that already holds the snapshot does not rewrite it:
// what is on disk counts as the last write.
func TestFileSaverStartsFromTheFileOnDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), StateFileName)
	if err := NewFileSaver(path).Save(fullSnapshot()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	before, _ := os.Stat(path)
	if err := os.Chtimes(path, before.ModTime(), before.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".marker", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := NewFileSaver(path).Save(fullSnapshot()); err != nil {
		t.Fatalf("Save after reopen: %v", err)
	}
	after, _ := os.Stat(path)
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("a fresh saver rewrote a file that already held the snapshot")
	}
}

func TestMemorySaverRecordsAndFails(t *testing.T) {
	saver := &MemorySaver{}
	if err := saver.Save(fullSnapshot()); err != nil || saver.Saves != 1 || len(saver.Last.Projects) != 1 {
		t.Errorf("Save = %v, Saves = %d, Last = %+v", err, saver.Saves, saver.Last)
	}
	saver.Err = os.ErrPermission
	if err := saver.Save(EmptySnapshot()); err == nil || saver.Saves != 1 {
		t.Errorf("a failing saver returned %v and counted %d saves", err, saver.Saves)
	}
}

// The record types are the one decision about what is persisted. A domain
// field is either carried by a record or listed here as deliberately dropped,
// so adding one to the domain is a conscious choice at this table.
func TestSnapshotDropsOnlyLiveState(t *testing.T) {
	dropped := map[string][]string{
		"Project":    {"State", "RuntimeState", "Effective", "UntrackedWorktrees"},
		"Workstream": {"ProviderLabel", "Strategy", "InProjectFolder", "State", "RuntimeState", "Effective", "StatusMessage", "OpenModes", "Actions", "PlaceholderID", "CreateFailed"},
		"Session":    {"State", "Ended", "Preview", "Error", "PlaceholderID", "Restartable", "OutdatedSettings", "ObservationData"},
	}
	tests := []struct {
		name   string
		domain reflect.Type
		record reflect.Type
	}{
		{"Project", reflect.TypeOf(domain.Project{}), reflect.TypeOf(ProjectRecord{})},
		{"Workstream", reflect.TypeOf(domain.Workstream{}), reflect.TypeOf(WorkstreamRecord{})},
		{"Session", reflect.TypeOf(domain.Session{}), reflect.TypeOf(SessionRecord{})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorded := map[string]bool{}
			for i := 0; i < tt.record.NumField(); i++ {
				recorded[tt.record.Field(i).Name] = true
			}
			for i := 0; i < tt.domain.NumField(); i++ {
				name := tt.domain.Field(i).Name
				listed := false
				for _, drop := range dropped[tt.name] {
					if drop == name {
						listed = true
					}
				}
				switch {
				case recorded[name] && listed:
					t.Errorf("%s.%s is both persisted and listed as dropped", tt.name, name)
				case !recorded[name] && !listed:
					t.Errorf("%s.%s is neither persisted nor listed as dropped: decide, and add it to one side", tt.name, name)
				}
			}
		})
	}
}

func TestDomainProjectsRestoreSessionsIdle(t *testing.T) {
	projects := fullSnapshot().DomainProjects()
	if len(projects) != 1 || len(projects[0].Workstreams) != 1 || len(projects[0].Workstreams[0].Sessions) != 1 {
		t.Fatalf("DomainProjects = %+v", projects)
	}
	session := projects[0].Workstreams[0].Sessions[0]
	if session.State != domain.SessionIdle || session.Provider != "enclave" || string(session.ProviderData) != `{"container":"abc"}` ||
		session.EndedAt == nil || !session.EndedAt.Equal(fixtureEndedAt) {
		t.Errorf("restored session = %+v; want idle, with its provider, data and end", session)
	}
	ws := projects[0].Workstreams[0]
	if ws.Branch != "feature/x" || string(ws.CreatorData) != `{"c":"d"}` || string(ws.ProviderData) != `{"k":1}` {
		t.Errorf("restored workstream = %+v", ws)
	}
	empty := Snapshot{Projects: []ProjectRecord{{ID: "p", Workstreams: []WorkstreamRecord{{ID: "ws"}}}}}.DomainProjects()
	if empty[0].Workstreams == nil || empty[0].Workstreams[0].Sessions == nil {
		t.Error("restored collections must be allocated, not nil")
	}
}

func TestRecordsRoundTripThroughDomain(t *testing.T) {
	want := fullSnapshot()
	projects := want.DomainProjects()
	got := Snapshot{Projects: []ProjectRecord{}, SessionTitles: want.SessionTitles, LaunchConfigs: want.LaunchConfigs}
	for _, project := range projects {
		record := RecordProject(project)
		for _, workstream := range project.Workstreams {
			workstreamRecord := RecordWorkstream(workstream)
			for _, session := range workstream.Sessions {
				workstreamRecord.Sessions = append(workstreamRecord.Sessions, RecordSession(session))
			}
			record.Workstreams = append(record.Workstreams, workstreamRecord)
		}
		got.Projects = append(got.Projects, record)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip = %+v\nwant %+v", got, want)
	}
}

func TestWriteFileAtomicCreatesTheDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "deeper", "file.json")
	if err := WriteFileAtomic(path, []byte("x")); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "x" {
		t.Errorf("file = %q, %v", data, err)
	}
}

func TestWriteFileAtomicReplacesWithoutLeftovers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.json")
	for _, content := range []string{"first", "second"} {
		if err := WriteFileAtomic(path, []byte(content)); err != nil {
			t.Fatalf("WriteFileAtomic(%q): %v", content, err)
		}
		if data, err := os.ReadFile(path); err != nil || string(data) != content {
			t.Errorf("file = %q, %v; want %q", data, err, content)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "file.json" {
		t.Errorf("directory holds %v, want only file.json", entries)
	}
}

func TestWriteFileAtomicFailsWhenDirectoryIsAFile(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(filepath.Join(blocker, "file.json"), []byte("x")); err == nil {
		t.Error("WriteFileAtomic through a file succeeded")
	}
}
