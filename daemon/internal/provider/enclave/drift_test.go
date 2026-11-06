//go:build dev

package enclave

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/provider"
	"github.com/eclipsesource/homeshell/daemon/internal/provider/enclave/sandboxschema"
)

func TestChangedOptions(t *testing.T) {
	current := sandboxschema.EffectiveConfig{
		"features":       option(`["devtools","node-dev"]`, "default"),
		"allow_domains":  option(`["api.example.com"]`, "global"),
		"playwright_mcp": option(`false`, "default"),
		"base_image":     option(`null`, "default"),
	}
	running := map[string]json.RawMessage{
		"features":       json.RawMessage(`[ "devtools", "node-dev" ]`),
		"allow_domains":  json.RawMessage(`["api.example.com","cdn.example.com"]`),
		"playwright_mcp": json.RawMessage(`true`),
		"base_image":     json.RawMessage(`"debian"`),
		"timezone":       json.RawMessage(`"UTC"`),
	}
	if got, want := changedOptions(running, current), []string{"allow_domains", "playwright_mcp"}; !slices.Equal(got, want) {
		t.Errorf("changedOptions = %q; want %q, and an option either side does not know left out", got, want)
	}
}

// driftProvider is sandboxProvider listing one container of its workstream,
// running with options, whose launch HomeShell recorded when launch is set.
func driftProvider(t *testing.T, options map[string]json.RawMessage, launch *domain.LaunchConfig) (*Provider, *fakeCLI) {
	t.Helper()
	cli := &fakeCLI{available: true, runResult: restartContainer}
	enclaveProvider := sandboxProvider(t, cli)
	cli.containers = map[string][]Container{"/tmp/project": {{
		Name: restartContainer, Tool: "claude", Directory: "/tmp/project/wt", Status: "running", Options: options,
	}}}
	container := cli.containers["/tmp/project"][0]
	enclaveProvider.bridges.records[container.Name] = bridgeRecord{Container: container}
	if launch != nil {
		if err := enclaveProvider.store.SetLaunchConfig("proj-1", restartContainer, *launch); err != nil {
			t.Fatal(err)
		}
	}
	return enclaveProvider, cli
}

func discoveredOutdated(t *testing.T, enclaveProvider *Provider) []string {
	t.Helper()
	found, err := enclaveProvider.DiscoverSessions(context.Background(), provider.ProjectRef{ID: "proj-1", Path: "/tmp/project"})
	if err != nil || len(found) != 1 {
		t.Fatalf("DiscoverSessions = %v, %v; want the one session", found, err)
	}
	return found[0].Session.OutdatedSettings
}

// A session HomeShell started is compared with what Enclave resolves for the
// flags a restart with its recorded settings would pass.
func TestOutdatedSettingsAreThoseARestartWouldChange(t *testing.T) {
	enclaveProvider, cli := driftProvider(t,
		map[string]json.RawMessage{"allow_domains": json.RawMessage(`["api.example.com","cdn.example.com"]`), "features": json.RawMessage(`["devtools","node-dev"]`)},
		&domain.LaunchConfig{ProviderID: "enclave", OptionID: "claude", Config: json.RawMessage(`{"enclave":{"allowDomains":["cdn.example.com"]}}`)})
	var asked []string
	cli.configWith = func(_ string, flags []string) ResolvedConfig {
		asked = flags
		return ResolvedConfig{Valid: true, Options: sandboxschema.EffectiveConfig{
			"allow_domains": option(`["api.example.com","cdn.example.com"]`, "cli"),
			"features":      option(`["devtools"]`, "global"),
		}}
	}

	if !enclaveProvider.refreshOutdated(context.Background()) {
		t.Error("refreshOutdated found nothing new; want the session out of date")
	}
	if want := []string{"--allow-domain", "api.example.com", "--allow-domain", "cdn.example.com"}; !slices.Equal(asked, want) {
		t.Errorf("config flags = %q; want the restart's %q", asked, want)
	}
	if got := discoveredOutdated(t, enclaveProvider); !slices.Equal(got, []string{"features"}) {
		t.Errorf("discovered OutdatedSettings = %q; want features", got)
	}
	session, err := enclaveProvider.SessionState(context.Background(), provider.SessionRef{ProjectID: "proj-1", WorkstreamID: "ws1", SessionID: restartContainer})
	if err != nil || !slices.Equal(session.OutdatedSettings, []string{"features"}) {
		t.Errorf("SessionState = %+v, %v; want features out of date", session, err)
	}
	if enclaveProvider.refreshOutdated(context.Background()) {
		t.Error("refreshOutdated reported a change when the answer was the same")
	}

	if err := restartNow(enclaveProvider, restartRequest(`{}`, false)); err != nil {
		t.Fatalf("restart: %v", err)
	}
	if got := discoveredOutdated(t, enclaveProvider); got != nil {
		t.Errorf("OutdatedSettings after a restart = %q; want none until asked again", got)
	}
}

// A session in the default workstreams root started with --add-sensitive-dir,
// which Enclave records as add_sensitive_dirs, so a restart's flags must carry
// it too, or every such session reads as out of date.
func TestOutdatedSettingsAskWithTheSensitiveDirARestartWouldPass(t *testing.T) {
	root := t.TempDir()
	workingDir := filepath.Join(root, "ws-1")
	if err := os.Mkdir(workingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	enclaveProvider, cli := driftProvider(t,
		map[string]json.RawMessage{"add_sensitive_dirs": json.RawMessage(`["` + workingDir + `"]`)},
		&domain.LaunchConfig{ProviderID: "enclave", OptionID: "claude", Config: json.RawMessage(`{}`)})
	enclaveProvider.workstreamsRoot = func() (string, error) { return root, nil }
	cli.containers["/tmp/project"][0].Directory = workingDir
	var asked []string
	cli.configWith = func(_ string, flags []string) ResolvedConfig {
		asked = flags
		dirs := []string{}
		for i, flag := range flags {
			if flag == "--add-sensitive-dir" && i+1 < len(flags) {
				dirs = append(dirs, flags[i+1])
			}
		}
		value, _ := json.Marshal(dirs)
		return ResolvedConfig{Valid: true, Options: sandboxschema.EffectiveConfig{"add_sensitive_dirs": option(string(value), "cli")}}
	}

	if enclaveProvider.refreshOutdated(context.Background()) {
		t.Errorf("refreshOutdated found %q out of date; want nothing", enclaveProvider.bridges.outdated)
	}
	if want := []string{"--add-sensitive-dir", workingDir}; !slices.Equal(asked, want) {
		t.Errorf("config flags = %q; want the restart's %q", asked, want)
	}
}

// Only sessions HomeShell started, with recorded options, are compared.
func TestOutdatedSettingsOnlyOfSessionsHomeShellStarted(t *testing.T) {
	recorded := map[string]json.RawMessage{"features": json.RawMessage(`["x"]`)}
	tests := []struct {
		name    string
		options map[string]json.RawMessage
		launch  *domain.LaunchConfig
	}{
		{name: "a session HomeShell did not start", options: recorded},
		{name: "a session another provider started", options: recorded, launch: &domain.LaunchConfig{ProviderID: "local", OptionID: "claude"}},
		{name: "a container without recorded options", launch: &domain.LaunchConfig{ProviderID: "enclave", OptionID: "claude"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			enclaveProvider, cli := driftProvider(t, tt.options, tt.launch)
			cli.configWith = func(string, []string) ResolvedConfig {
				return ResolvedConfig{Valid: true, Options: sandboxschema.EffectiveConfig{"features": option(`["devtools"]`, "default")}}
			}
			enclaveProvider.refreshOutdated(context.Background())
			if got := discoveredOutdated(t, enclaveProvider); got != nil {
				t.Errorf("OutdatedSettings = %q; want none", got)
			}
		})
	}
}

// A session restarted while the check runs was listed as it was before, so
// what the check found about it is not kept.
func TestOutdatedSettingsOfASessionRestartedMeanwhileAreDropped(t *testing.T) {
	enclaveProvider, cli := driftProvider(t,
		map[string]json.RawMessage{"features": json.RawMessage(`["devtools","node-dev"]`)},
		&domain.LaunchConfig{ProviderID: "enclave", OptionID: "claude"})
	cli.configWith = func(string, []string) ResolvedConfig {
		return ResolvedConfig{Valid: true, Options: sandboxschema.EffectiveConfig{"features": option(`["devtools"]`, "global")}}
	}
	cli.onList = func() {
		cli.onList = nil
		if err := restartNow(enclaveProvider, restartRequest(`{}`, false)); err != nil {
			t.Errorf("restart: %v", err)
		}
	}
	if enclaveProvider.refreshOutdated(context.Background()) {
		t.Error("refreshOutdated kept what it found about a session restarted meanwhile")
	}
	if !enclaveProvider.refreshOutdated(context.Background()) {
		t.Error("the next check found nothing; want the session it lists out of date")
	}
}

// A project that cannot list does not keep the others from being checked.
func TestOutdatedSettingsDespiteAProjectThatCannotList(t *testing.T) {
	enclaveProvider, cli := driftProvider(t,
		map[string]json.RawMessage{"features": json.RawMessage(`["devtools","node-dev"]`)},
		&domain.LaunchConfig{ProviderID: "enclave", OptionID: "claude"})
	cli.configWith = func(string, []string) ResolvedConfig {
		return ResolvedConfig{Valid: true, Options: sandboxschema.EffectiveConfig{"features": option(`["devtools"]`, "global")}}
	}
	if err := enclaveProvider.store.AddProject(&domain.Project{ID: "proj-2", Path: "/tmp/gone", Workstreams: []domain.Workstream{}}); err != nil {
		t.Fatal(err)
	}
	cli.listErrIn = map[string]error{"/tmp/gone": errors.New("no such directory")}
	if !enclaveProvider.refreshOutdated(context.Background()) || !slices.Equal(discoveredOutdated(t, enclaveProvider), []string{"features"}) {
		t.Error("a project that cannot list kept the session of another from being checked")
	}
}

// A session whose comparison cannot be made, or whose project cannot list,
// keeps what the last check found.
func TestOutdatedSettingsKeepTheLastResultWhenTheyCannotBeTold(t *testing.T) {
	enclaveProvider, cli := driftProvider(t,
		map[string]json.RawMessage{"features": json.RawMessage(`["devtools","node-dev"]`)},
		&domain.LaunchConfig{ProviderID: "enclave", OptionID: "claude"})
	cli.configWith = func(string, []string) ResolvedConfig {
		return ResolvedConfig{Valid: true, Options: sandboxschema.EffectiveConfig{"features": option(`["devtools"]`, "global")}}
	}
	enclaveProvider.refreshOutdated(context.Background())

	cli.configWith = func(string, []string) ResolvedConfig { return ResolvedConfig{Error: "timed out"} }
	if enclaveProvider.refreshOutdated(context.Background()) || !slices.Equal(discoveredOutdated(t, enclaveProvider), []string{"features"}) {
		t.Error("a comparison that could not be made dropped the last result")
	}

	if err := enclaveProvider.store.AddProject(&domain.Project{ID: "proj-2", Path: "/tmp/other", Workstreams: []domain.Workstream{}}); err != nil {
		t.Fatal(err)
	}
	cli.listErrIn = map[string]error{"/tmp/project": errors.New("no such directory")}
	changed := enclaveProvider.refreshOutdated(context.Background())
	enclaveProvider.bridges.mu.Lock()
	kept := enclaveProvider.bridges.outdated[restartContainer]
	enclaveProvider.bridges.mu.Unlock()
	if changed || !slices.Equal(kept, []string{"features"}) {
		t.Error("a project that could not list dropped the last result of its session")
	}
}

// A restart that could not stop the container leaves the session as it was,
// but what a check found meanwhile may compare it with the settings it was to
// be restarted with, so it is not kept; what was found before stands.
func TestOutdatedSettingsFoundDuringAnAbandonedRestartAreDropped(t *testing.T) {
	enclaveProvider, cli := driftProvider(t,
		map[string]json.RawMessage{"features": json.RawMessage(`["devtools","node-dev"]`)},
		&domain.LaunchConfig{ProviderID: "enclave", OptionID: "claude"})
	cli.configWith = func(string, []string) ResolvedConfig {
		return ResolvedConfig{Valid: true, Options: sandboxschema.EffectiveConfig{"features": option(`["devtools"]`, "global")}}
	}
	cli.onList = func() {
		cli.onList = nil
		cli.stopErr = errors.New("docker is not running")
		_ = restartNow(enclaveProvider, restartRequest(`{}`, false))
	}
	if enclaveProvider.refreshOutdated(context.Background()) {
		t.Error("refreshOutdated kept what it found while a restart was abandoned")
	}

	// What a check found before, about the container left as it was, stands.
	enclaveProvider.refreshOutdated(context.Background())
	_ = restartNow(enclaveProvider, restartRequest(`{}`, false))
	if got := discoveredOutdated(t, enclaveProvider); !slices.Equal(got, []string{"features"}) {
		t.Errorf("OutdatedSettings after an abandoned restart = %q; want features, as before", got)
	}
}

// A restart records its settings before it marks the session restarting, and
// an abandoned one restores them only after it gave the session back; a check
// that read them in between keeps what it found before.
func TestOutdatedSettingsOfASessionWhoseSettingsChangedMeanwhileStand(t *testing.T) {
	launch := domain.LaunchConfig{ProviderID: "enclave", OptionID: "claude"}
	enclaveProvider, cli := driftProvider(t, map[string]json.RawMessage{"features": json.RawMessage(`["devtools","node-dev"]`)}, &launch)
	cli.configWith = func(string, []string) ResolvedConfig {
		return ResolvedConfig{Valid: true, Options: sandboxschema.EffectiveConfig{"features": option(`["devtools"]`, "global")}}
	}
	enclaveProvider.refreshOutdated(context.Background())

	cli.configWith = func(string, []string) ResolvedConfig {
		restored := launch
		restored.Config = json.RawMessage(`{"enclave":{"hostConfig":true}}`)
		if err := enclaveProvider.store.SetLaunchConfig("proj-1", restartContainer, restored); err != nil {
			t.Error(err)
		}
		return ResolvedConfig{Valid: true, Options: sandboxschema.EffectiveConfig{"features": option(`["devtools","node-dev"]`, "global")}}
	}
	if enclaveProvider.refreshOutdated(context.Background()) {
		t.Error("a check kept what it found against settings that changed while it ran")
	}
	if got := discoveredOutdated(t, enclaveProvider); !slices.Equal(got, []string{"features"}) {
		t.Errorf("OutdatedSettings = %q; want the last result, features", got)
	}
}

// Watching checks at once, rather than a driftCheckInterval after the daemon
// starts.
func TestWatchChecksOutdatedSettingsAtOnce(t *testing.T) {
	enclaveProvider, cli := driftProvider(t,
		map[string]json.RawMessage{"features": json.RawMessage(`["devtools","node-dev"]`)},
		&domain.LaunchConfig{ProviderID: "enclave", OptionID: "claude"})
	cli.configWith = func(string, []string) ResolvedConfig {
		return ResolvedConfig{Valid: true, Options: sandboxschema.EffectiveConfig{"features": option(`["devtools"]`, "global")}}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	changed := make(chan struct{}, 1)
	go enclaveProvider.Watch(ctx, sinkFunc(func() {
		select {
		case changed <- struct{}{}:
		default:
		}
	}))
	select {
	case <-changed:
	case <-time.After(5 * time.Second):
		t.Fatal("the watch did not report the out-of-date session at once")
	}
	if got := discoveredOutdated(t, enclaveProvider); !slices.Equal(got, []string{"features"}) {
		t.Errorf("OutdatedSettings = %q; want features", got)
	}
}

type sinkFunc func()

func (f sinkFunc) Changed() { f() }

// A session being restarted keeps what the last check found: the settings it
// is to be restarted with are recorded before it runs with them.
func TestOutdatedSettingsOfASessionBeingRestartedStand(t *testing.T) {
	enclaveProvider, cli := driftProvider(t,
		map[string]json.RawMessage{"features": json.RawMessage(`["devtools","node-dev"]`)},
		&domain.LaunchConfig{ProviderID: "enclave", OptionID: "claude"})
	cli.configWith = func(string, []string) ResolvedConfig {
		return ResolvedConfig{Valid: true, Options: sandboxschema.EffectiveConfig{"features": option(`["devtools"]`, "global")}}
	}
	enclaveProvider.refreshOutdated(context.Background())
	enclaveProvider.bridges.mu.Lock()
	enclaveProvider.bridges.restarting[restartContainer] = restartProjection{workingDir: "/tmp/project/wt"}
	enclaveProvider.bridges.mu.Unlock()
	cli.configWith = func(string, []string) ResolvedConfig {
		return ResolvedConfig{Valid: true, Options: sandboxschema.EffectiveConfig{"features": option(`["devtools","node-dev"]`, "global")}}
	}
	if enclaveProvider.refreshOutdated(context.Background()) {
		t.Error("a check compared a session being restarted against the settings it is to be restarted with")
	}
	enclaveProvider.bridges.mu.Lock()
	got := enclaveProvider.bridges.outdated[restartContainer]
	enclaveProvider.bridges.mu.Unlock()
	if !slices.Equal(got, []string{"features"}) {
		t.Errorf("outdated = %q; want the last result, features", got)
	}
}

// A check asks Enclave's config afresh, as a restart would, but once per
// directory and tool rather than once per session in it. The feature catalogue
// stays cached, for the create form too.
func TestOutdatedSettingsAskTheBaseOncePerCheck(t *testing.T) {
	launch := domain.LaunchConfig{ProviderID: "enclave", OptionID: "claude", Config: json.RawMessage(`{"enclave":{"allowDomains":["cdn.example.com"]}}`)}
	enclaveProvider, cli := driftProvider(t, map[string]json.RawMessage{"features": json.RawMessage(`["devtools"]`)}, &launch)
	first := cli.containers["/tmp/project"][0]
	for _, name := range []string{"enclave-claude-abc123abc123-second", "enclave-claude-abc123abc123-third"} {
		other := first
		other.Name = name
		cli.containers["/tmp/project"] = append(cli.containers["/tmp/project"], other)
		if err := enclaveProvider.store.SetLaunchConfig("proj-1", name, launch); err != nil {
			t.Fatal(err)
		}
	}
	flagged := 0
	cli.configWith = func(string, []string) ResolvedConfig {
		flagged++
		return ResolvedConfig{Valid: true, Options: sandboxschema.EffectiveConfig{"features": option(`["devtools"]`, "default")}}
	}

	for check, wantFeatures := range []int{1, 0} {
		cli.configCalls, cli.featureCalls, flagged = 0, 0, 0
		enclaveProvider.refreshOutdated(context.Background())
		if flagged != 3 {
			t.Fatalf("check %d resolved %d sessions; want all 3", check+1, flagged)
		}
		if base := cli.configCalls - flagged; base != 1 || cli.featureCalls != wantFeatures {
			t.Errorf("check %d asked the base config %d and the features %d times; want once and %d", check+1, base, cli.featureCalls, wantFeatures)
		}
	}
}
