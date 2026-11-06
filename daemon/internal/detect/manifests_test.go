package detect

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

type manifestLock struct {
	Tag           string `json:"tag"`
	Commit        string `json:"commit"`
	EngineVersion int    `json:"engine_version"`
	Files         map[string]struct {
		SHA256 string `json:"sha256"`
	} `json:"files"`
}

func readLock(t *testing.T) manifestLock {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("manifests", "herdr", "manifests.lock"))
	if err != nil {
		t.Fatal(err)
	}
	var lock manifestLock
	if err := json.Unmarshal(data, &lock); err != nil {
		t.Fatal(err)
	}
	return lock
}

// TestVendoredManifestProvenance guards the source pin and ensures herdr's
// license ships next to the vendored manifests (Apache-2.0 §4).
func TestVendoredManifestProvenance(t *testing.T) {
	lock := readLock(t)
	if lock.Tag == "" || lock.Commit == "" {
		t.Error("manifests.lock does not identify an upstream tag and commit")
	}
	files, err := filepath.Glob(filepath.Join("manifests", "herdr", "*.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != len(lock.Files) {
		t.Fatalf("%d manifests, %d pinned hashes", len(files), len(lock.Files))
	}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := fmt.Sprintf("%x", sha256.Sum256(data)), lock.Files[filepath.Base(file)].SHA256; got != want {
			t.Errorf("%s hash = %s, want %s", file, got, want)
		}
	}
	if lock.EngineVersion != engineVersion {
		t.Errorf("manifests were vendored for engine %d, this engine is %d", lock.EngineVersion, engineVersion)
	}
	if _, err := os.Stat(filepath.Join("manifests", "herdr", "LICENSE")); err != nil {
		t.Errorf("manifests/herdr/LICENSE: %v", err)
	}
}

func TestEveryVendoredManifestLoads(t *testing.T) {
	tomlFiles, err := filepath.Glob(filepath.Join("manifests", "herdr", "*.toml"))
	if err != nil {
		t.Fatal(err)
	}
	allRuleSets, err := loadRuleSets(manifestFiles)
	if err != nil {
		t.Fatal(err)
	}
	if len(allRuleSets) != len(tomlFiles) {
		t.Fatalf("validated %d rule sets, vendored %d manifests", len(allRuleSets), len(tomlFiles))
	}
	for _, rs := range allRuleSets {
		if len(rs.rules) == 0 {
			t.Errorf("%s loaded without rules", rs.id)
		}
	}
	if _, enabled := enabledRuleSets(); len(enabled) != 17 {
		t.Fatalf("enabled %d runtime rule sets, want 17 screen-authoritative agents", len(enabled))
	}
}

func TestValidateManifestRejects(t *testing.T) {
	tests := []struct {
		name     string
		manifest string
		want     string
	}{
		{"engine too new", "id = \"x\"\nmin_engine_version = 4\n[[rules]]\nid = \"r\"\nstate = \"idle\"\ncontains = [\"a\"]\n", "requires engine 4"},
		{"no rules", "id = \"x\"\n", "at least one rule"},
		{"empty rule id", "id = \"x\"\n[[rules]]\nid = \" \"\nstate = \"idle\"\ncontains = [\"a\"]\n", "rule id must not be empty"},
		{"empty gate", "id = \"x\"\n[[rules]]\nid = \"r\"\nstate = \"idle\"\n", "positive matcher"},
		{"not-only gate", "id = \"x\"\n[[rules]]\nid = \"r\"\nstate = \"idle\"\nnot = [{ contains = [\"a\"] }]\n", "positive matcher"},
		{"empty not gate", "id = \"x\"\n[[rules]]\nid = \"r\"\nstate = \"idle\"\ncontains = [\"a\"]\nnot = [{}]\n", "not gate must contain a matcher"},
		{"unknown region", "id = \"x\"\n[[rules]]\nid = \"r\"\nstate = \"idle\"\nregion = \"nonsense\"\ncontains = [\"a\"]\n", "unknown region"},
		{"unknown state", "id = \"x\"\n[[rules]]\nid = \"r\"\nstate = \"busy\"\ncontains = [\"a\"]\n", "unknown state"},
		{"skip without unknown", "id = \"x\"\n[[rules]]\nid = \"r\"\nstate = \"idle\"\nskip_state_update = true\ncontains = [\"a\"]\n", "skip_state_update"},
		{"top region below engine 3", "id = \"x\"\nmin_engine_version = 2\n[[rules]]\nid = \"r\"\nstate = \"idle\"\nregion = \"top_non_empty_lines(3)\"\ncontains = [\"a\"]\n", "top_non_empty_lines"},
		{"unknown key", "id = \"x\"\n[[rules]]\nid = \"r\"\nstate = \"idle\"\ncontain = [\"a\"]\n", "unknown field"},
		{"matcher too long", "id = \"x\"\n[[rules]]\nid = \"r\"\nstate = \"idle\"\ncontains = [\"" + strings.Repeat("a", maxMatcherChars+1) + "\"]\n", "max length"},
		{"gate too deep", "id = \"x\"\n[[rules]]\nid = \"r\"\nstate = \"idle\"\nall = [" + strings.Repeat("{ all = [", maxGateDepth) + "{ contains = [\"a\"] }" + strings.Repeat("] }", maxGateDepth) + "]\n", "max gate depth"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseRuleSet([]byte(tt.manifest))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("parseRuleSet error = %v, want one containing %q", err, tt.want)
			}
		})
	}
}

func TestLoadRuleSetsAppliesOverlays(t *testing.T) {
	vendored := "id = \"agent\"\n[[rules]]\nid = \"idle\"\nstate = \"idle\"\ncontains = [\"ready\"]\n"
	tests := []struct {
		name    string
		overlay string
		want    string
	}{
		{"appends", "id = \"agent\"\n\n[[rules]]\nid = \"extra\"\nstate = \"blocked\"\npriority = 10\ncontains = [\"sign in\"]\n", ""},
		{"rule id collides", "id = \"agent\"\n[[rules]]\nid = \"idle\"\nstate = \"blocked\"\ncontains = [\"x\"]\n", "collides"},
		{"overlay ids collide", "id = \"agent\"\n[[rules]]\nid = \"extra\"\nstate = \"blocked\"\ncontains = [\"x\"]\n[[rules]]\nid = \"extra\"\nstate = \"blocked\"\ncontains = [\"y\"]\n", "collides"},
		{"wrong id", "id = \"other\"\n[[rules]]\nid = \"extra\"\nstate = \"blocked\"\ncontains = [\"x\"]\n", "applied to"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files := fstest.MapFS{
				"manifests/herdr/agent.toml": {Data: []byte(vendored)},
				"manifests/local/agent.toml": {Data: []byte(tt.overlay)},
			}
			sets, err := loadRuleSets(files)
			if tt.want != "" {
				if err == nil || !strings.Contains(err.Error(), tt.want) {
					t.Fatalf("loadRuleSets error = %v, want %q", err, tt.want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			res := sets[0].evaluate(Snapshot{Screen: "ready\nplease sign in\n"})
			if res.MatchedRule != "extra" {
				t.Errorf("matched %q, want the overlay rule", res.MatchedRule)
			}
		})
	}
	t.Run("overlay without vendored manifest", func(t *testing.T) {
		files := fstest.MapFS{
			"manifests/herdr/agent.toml": {Data: []byte(vendored)},
			"manifests/local/ghost.toml": {Data: []byte("id = \"ghost\"\n[[rules]]\nid = \"r\"\nstate = \"idle\"\ncontains = [\"a\"]\n")},
		}
		if _, err := loadRuleSets(files); err == nil {
			t.Fatal("expected an error for an overlay with no vendored manifest")
		}
	})
}
