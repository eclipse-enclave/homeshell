//go:build dev

package enclave

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/provider/enclave/sandboxschema"
	"github.com/eclipsesource/homeshell/daemon/internal/provider/workstream"
	"github.com/eclipsesource/homeshell/daemon/internal/store"
)

func TestInventoryDirs(t *testing.T) {
	tests := []struct {
		name        string
		workstreams []domain.Workstream
		want        []string
	}{
		{"no workstreams", nil, []string{"/work/app"}},
		{"in-place and worktree add nothing", []domain.Workstream{
			{StrategyID: "in-place", WorkingDir: "/work/app"},
			{StrategyID: "worktree", WorkingDir: "/work/wt"},
		}, []string{"/work/app"}},
		{"a copy adds its folder", []domain.Workstream{
			{StrategyID: string(workstream.Copy), WorkingDir: "/work/copy"},
		}, []string{"/work/app", "/work/copy"}},
		{"a copy in the project folder is not added twice", []domain.Workstream{
			{StrategyID: string(workstream.Copy), WorkingDir: "/work/app"},
		}, []string{"/work/app"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := inventoryDirs(domain.Project{Path: "/work/app", Workstreams: test.workstreams})
			if !slices.Equal(got, test.want) {
				t.Errorf("inventoryDirs = %q, want %q", got, test.want)
			}
		})
	}
}

func TestInventoryListsEachDirectoryOncePerCheck(t *testing.T) {
	cli := &fakeCLI{available: true}
	enclaveProvider := newForTest(t, cli)
	enclaveProvider.SetStore(store.NewTestStore(t, []domain.Project{
		{ID: "a", Path: "/work/a", Workstreams: []domain.Workstream{{ID: "c", StrategyID: string(workstream.Copy), WorkingDir: "/work/a-copy"}}},
		{ID: "b", Path: "/work/b"},
	}))
	enclaveProvider.checkInventory(context.Background())
	if cli.listCalls != 3 {
		t.Errorf("enclave ps ran %d times for one check; want 3, one per directory", cli.listCalls)
	}
}

func TestOutdatedSettingsOfASessionInACopyFolder(t *testing.T) {
	cli := &fakeCLI{available: true, runResult: restartContainer}
	enclaveProvider := sandboxProvider(t, cli)
	enclaveProvider.SetStore(store.NewTestStore(t, []domain.Project{{
		ID: "proj-1", Path: "/tmp/project",
		Workstreams: []domain.Workstream{{ID: "ws1", Provider: "core", StrategyID: string(workstream.Copy), WorkingDir: "/tmp/copy"}},
	}}))
	cli.containers = map[string][]Container{"/tmp/copy": {{
		Name: restartContainer, Tool: "claude", Directory: "/tmp/copy", Status: "running",
		Options: map[string]json.RawMessage{"features": json.RawMessage(`["devtools","node-dev"]`)},
	}}}
	if err := enclaveProvider.store.SetLaunchConfig("proj-1", restartContainer, domain.LaunchConfig{ProviderID: "enclave", OptionID: "claude"}); err != nil {
		t.Fatal(err)
	}
	cli.configWith = func(string, []string) ResolvedConfig {
		return ResolvedConfig{Valid: true, Options: sandboxschema.EffectiveConfig{"features": option(`["devtools"]`, "global")}}
	}
	if !enclaveProvider.checkInventory(context.Background()) {
		t.Fatal("check found nothing new; want the session in the copy folder out of date")
	}
	if got := enclaveProvider.bridges.outdated[restartContainer]; !slices.Equal(got, []string{"features"}) {
		t.Errorf("outdated = %q, want features", got)
	}
}

func TestInventoryIntervalFromTheEnvironment(t *testing.T) {
	for value, want := range map[string]time.Duration{"250": 250 * time.Millisecond, "": 0, "x": 0, "0": 0} {
		if got := inventoryIntervalFrom(value); got != want {
			t.Errorf("inventoryIntervalFrom(%q) = %v, want %v", value, got, want)
		}
	}
	enclaveProvider := &Provider{}
	if got := enclaveProvider.inventoryInterval(); got != driftCheckInterval {
		t.Errorf("default interval = %v, want %v", got, driftCheckInterval)
	}
}
