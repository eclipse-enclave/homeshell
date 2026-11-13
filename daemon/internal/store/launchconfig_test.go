//go:build dev

package store

import (
	"encoding/json"
	"maps"
	"testing"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/persist"
)

func TestLaunchConfigRoundTripsAndSurvivesReprojection(t *testing.T) {
	s := titledStore(t)
	config := json.RawMessage(`{"model":"opus","sandbox":{"features":["playwright"]}}`)
	if err := s.SetLaunchConfig("p1", "sess-1", domain.LaunchConfig{ProviderID: "enclave", OptionID: "claude", Config: config}); err != nil {
		t.Fatalf("SetLaunchConfig: %v", err)
	}
	// The session is re-reported by its provider, as every reconcile cycle does.
	if err := s.ReplaceWorkstreams("p1", nil, []domain.Workstream{{
		ID: "ws-1", Name: "ws", Provider: "core", StrategyID: "in-place", WorkingDir: "/tmp/p1",
		Sessions: []domain.Session{{ID: "sess-1", Title: "claude session", Agent: "claude", Provider: "enclave"}},
	}}); err != nil {
		t.Fatalf("ReplaceWorkstreams: %v", err)
	}
	got, ok := s.LaunchConfig("sess-1")
	if !ok {
		t.Fatal("LaunchConfig: none after a reprojection; want the recorded one")
	}
	if got.ProviderID != "enclave" || got.OptionID != "claude" || string(got.Config) != string(config) {
		t.Errorf("LaunchConfig = %+v; want enclave/claude with %s", got, config)
	}
}

func TestLaunchConfigUnknownSession(t *testing.T) {
	s := titledStore(t)
	if _, ok := s.LaunchConfig("sess-1"); ok {
		t.Error("LaunchConfig of a session nothing recorded: want false")
	}
}

func TestReprojectionForgetsLaunchConfigsOfVanishedSessions(t *testing.T) {
	s := titledStore(t)
	if err := s.SetLaunchConfig("p1", "sess-1", domain.LaunchConfig{ProviderID: "enclave", OptionID: "claude"}); err != nil {
		t.Fatalf("SetLaunchConfig: %v", err)
	}
	if err := s.ReplaceWorkstreams("p1", nil, []domain.Workstream{{
		ID: "ws-1", Name: "ws", Provider: "core", StrategyID: "in-place", WorkingDir: "/tmp/p1",
	}}); err != nil {
		t.Fatalf("ReplaceWorkstreams: %v", err)
	}
	if _, ok := s.LaunchConfig("sess-1"); ok {
		t.Error("LaunchConfig of a session no provider reports any more: want it forgotten")
	}
	if _, ok := Saved(t, s).LaunchConfigs["sess-1"]; ok {
		t.Error("the forgotten launch config is still in the saved state")
	}
}

func TestRemoveSessionForgetsItsLaunchConfig(t *testing.T) {
	s := titledStore(t)
	if err := s.SetLaunchConfig("p1", "sess-1", domain.LaunchConfig{ProviderID: "enclave", OptionID: "claude"}); err != nil {
		t.Fatalf("SetLaunchConfig: %v", err)
	}
	if err := s.RemoveSession("p1", "ws-1", "sess-1"); err != nil {
		t.Fatalf("RemoveSession: %v", err)
	}
	if _, ok := s.LaunchConfig("sess-1"); ok {
		t.Error("LaunchConfig of a removed session: want it forgotten")
	}
}

func TestRemoveProjectForgetsItsLaunchConfigs(t *testing.T) {
	s := titledStore(t)
	if err := s.SetLaunchConfig("p1", "sess-1", domain.LaunchConfig{ProviderID: "enclave", OptionID: "claude"}); err != nil {
		t.Fatalf("SetLaunchConfig: %v", err)
	}
	if _, err := s.RemoveProject("p1"); err != nil {
		t.Fatalf("RemoveProject: %v", err)
	}
	if _, ok := s.LaunchConfig("sess-1"); ok {
		t.Error("LaunchConfig of a removed project's session: want it forgotten")
	}
	assertNoPersistedLaunchConfig(t, s, "sess-1")
}

// A session another project still lists keeps what it was started with, and its
// custom title, when the project that recorded them is removed: they move to the
// other project, in memory and in the database.
func TestRemoveProjectMovesTheRecordsOfASessionAnotherProjectLists(t *testing.T) {
	shared := domain.Session{ID: "sess-1", Title: "claude session", Agent: "claude", Provider: "enclave"}
	s := NewTestStore(t, []domain.Project{
		{ID: "p1", Name: "p1", Path: "/tmp/shared", Workstreams: []domain.Workstream{{
			ID: "ws-1", Name: "ws", Provider: "core", StrategyID: "in-place", WorkingDir: "/tmp/shared",
			Sessions: []domain.Session{shared},
		}}},
		{ID: "p2", Name: "p2", Path: "/tmp/shared"},
	})
	// Projected into the other project sharing its directory, as discovery does.
	if err := s.ReplaceWorkstreams("p2", nil, []domain.Workstream{{
		ID: "ws-2", Name: "ws", Provider: "core", StrategyID: "in-place", WorkingDir: "/tmp/shared",
		Sessions: []domain.Session{shared},
	}}); err != nil {
		t.Fatalf("ReplaceWorkstreams: %v", err)
	}
	if err := s.SetLaunchConfig("p1", "sess-1", domain.LaunchConfig{ProviderID: "enclave", OptionID: "claude"}); err != nil {
		t.Fatalf("SetLaunchConfig: %v", err)
	}
	if err := s.RenameSession("p1", "ws-1", "sess-1", "named"); err != nil {
		t.Fatalf("RenameSession: %v", err)
	}
	rehomed, err := s.RemoveProject("p1")
	if err != nil {
		t.Fatalf("RemoveProject: %v", err)
	}
	if want := map[string]SessionHome{"sess-1": {ProjectID: "p2", WorkstreamID: "ws-2"}}; !maps.Equal(rehomed, want) {
		t.Errorf("RemoveProject rehomed %v; want %v", rehomed, want)
	}

	if got, ok := s.LaunchConfig("sess-1"); !ok || got.OptionID != "claude" {
		t.Errorf("LaunchConfig = %+v, %v; want the recorded one kept", got, ok)
	}
	if launches := Saved(t, s).LaunchConfigs; launches["sess-1"].ProjectID != "p2" {
		t.Errorf("persisted launch config = %+v; want it moved to p2", launches["sess-1"])
	}
	if titles := Saved(t, s).SessionTitles; titles["sess-1"] != (persist.CustomTitle{ProjectID: "p2", Title: "named"}) {
		t.Errorf("persisted title = %+v; want it moved to p2", titles["sess-1"])
	}
	// The other project's next projection shows the title, and keeps both.
	if err := s.ReplaceWorkstreams("p2", nil, []domain.Workstream{{
		ID: "ws-2", Name: "ws", Provider: "core", StrategyID: "in-place", WorkingDir: "/tmp/shared",
		Sessions: []domain.Session{shared},
	}}); err != nil {
		t.Fatalf("ReplaceWorkstreams: %v", err)
	}
	if title := sessionTitle(t, s, "sess-1"); title != "named" {
		t.Errorf("title = %q; want the custom one kept", title)
	}
	if _, ok := s.LaunchConfig("sess-1"); !ok {
		t.Error("LaunchConfig after the other project's projection: want it kept")
	}

	// Removing the other project too takes them with it.
	if rehomed, err := s.RemoveProject("p2"); err != nil || len(rehomed) != 0 {
		t.Fatalf("RemoveProject = %v, %v; want nothing rehomed", rehomed, err)
	}
	if _, ok := s.LaunchConfig("sess-1"); ok {
		t.Error("LaunchConfig after both projects went: want it forgotten")
	}
	assertNoPersistedLaunchConfig(t, s, "sess-1")
}

func TestRemoveWorkstreamForgetsItsLaunchConfigs(t *testing.T) {
	s := titledStore(t)
	if err := s.SetLaunchConfig("p1", "sess-1", domain.LaunchConfig{ProviderID: "enclave", OptionID: "claude"}); err != nil {
		t.Fatalf("SetLaunchConfig: %v", err)
	}
	if err := s.RemoveWorkstream("p1", "ws-1"); err != nil {
		t.Fatalf("RemoveWorkstream: %v", err)
	}
	if _, ok := s.LaunchConfig("sess-1"); ok {
		t.Error("LaunchConfig of a removed workstream's session: want it forgotten")
	}
	assertNoPersistedLaunchConfig(t, s, "sess-1")
}

// assertNoPersistedLaunchConfig fails when the saved state still holds a launch
// config for the session.
func assertNoPersistedLaunchConfig(t *testing.T, s *Store, sessionID string) {
	t.Helper()
	if _, ok := Saved(t, s).LaunchConfigs[sessionID]; ok {
		t.Errorf("the launch config of %s is still in the saved state", sessionID)
	}
}

func TestSetLaunchConfigOfAnUnknownProject(t *testing.T) {
	s := titledStore(t)
	if err := s.SetLaunchConfig("p-gone", "sess-9", domain.LaunchConfig{ProviderID: "enclave", OptionID: "claude"}); err == nil {
		t.Error("SetLaunchConfig for a project the store does not hold: want an error")
	}
	if _, ok := s.LaunchConfig("sess-9"); ok {
		t.Error("LaunchConfig recorded for a project the store does not hold")
	}
}

// A projection computed before a session was promoted does not list it. That
// swap must not delete what the session was started with; the next projection,
// computed after the promotion, settles it like any other session.
func TestStaleProjectionKeepsAJustPromotedSessionsLaunchConfig(t *testing.T) {
	s := ephemeralFixture(t)
	// What the providers reported before the create: the workstream, no sessions.
	staleProjection := func() []domain.Workstream {
		return []domain.Workstream{{ID: "ws-1", Name: "ws", Provider: "core", StrategyID: "in-place"}}
	}
	if err := s.AddEphemeralSession("p1", "ws-1", domain.Session{ID: "starting-abc", State: domain.SessionStarting}); err != nil {
		t.Fatalf("AddEphemeralSession: %v", err)
	}
	launch := domain.LaunchConfig{ProviderID: "enclave", OptionID: "claude", Config: json.RawMessage(`{"model":"opus"}`)}
	real := domain.Session{ID: "sess-1", Agent: "claude", Provider: "enclave", State: domain.SessionRunning}
	if err := s.PromoteEphemeralSession("p1", "ws-1", "starting-abc", real, launch); err != nil {
		t.Fatalf("PromoteEphemeralSession: %v", err)
	}

	if err := s.ReplaceWorkstreams("p1", nil, staleProjection()); err != nil {
		t.Fatalf("ReplaceWorkstreams (stale): %v", err)
	}
	if got, ok := s.LaunchConfig("sess-1"); !ok || string(got.Config) != `{"model":"opus"}` {
		t.Errorf("LaunchConfig after a stale projection = %+v, %v; want the recorded one", got, ok)
	}
	if launches := Saved(t, s).LaunchConfigs; launches["sess-1"].OptionID != "claude" {
		t.Error("a stale projection deleted the launch config from the saved state")
	}
	if ws, _ := s.Workstream("p1", "ws-1"); !hasSession(ws.Sessions, "sess-1") {
		t.Errorf("sessions after a stale projection = %+v; want the promoted one kept", ws.Sessions)
	}

	// The session has really gone: the next projection drops it, and its record.
	if err := s.ReplaceWorkstreams("p1", nil, staleProjection()); err != nil {
		t.Fatalf("ReplaceWorkstreams: %v", err)
	}
	if _, ok := s.LaunchConfig("sess-1"); ok {
		t.Error("LaunchConfig of a session no projection lists: want it forgotten")
	}
	assertNoPersistedLaunchConfig(t, s, "sess-1")
}

func TestLaunchConfigIsReloadedAtStartup(t *testing.T) {
	s := titledStore(t)
	if err := s.SetLaunchConfig("p1", "sess-1", domain.LaunchConfig{ProviderID: "enclave", OptionID: "claude", Config: json.RawMessage(`{"model":"opus"}`)}); err != nil {
		t.Fatalf("SetLaunchConfig: %v", err)
	}
	restarted := Restore(Saved(t, s), &persist.MemorySaver{})
	if got, ok := restarted.LaunchConfig("sess-1"); !ok || string(got.Config) != `{"model":"opus"}` {
		t.Errorf("LaunchConfig after a restart = %+v, %v; want the recorded one", got, ok)
	}
}

func TestForgetLaunchConfig(t *testing.T) {
	s := titledStore(t)
	if err := s.SetLaunchConfig("p1", "sess-1", domain.LaunchConfig{ProviderID: "enclave", OptionID: "claude"}); err != nil {
		t.Fatalf("SetLaunchConfig: %v", err)
	}
	s.ForgetLaunchConfig("sess-1")
	if _, ok := s.LaunchConfig("sess-1"); ok {
		t.Error("LaunchConfig after ForgetLaunchConfig: want none")
	}
	assertNoPersistedLaunchConfig(t, s, "sess-1")
}
