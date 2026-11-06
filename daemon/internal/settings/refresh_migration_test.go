package settings

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRefreshMigration(t *testing.T) {
	for _, tt := range []struct {
		version  int
		interval string
		want     float64
	}{
		{1, `"refreshIntervalSec":5`, 0.5},
		{1, `"refreshIntervalSec":0.25`, 0.25},
		{1, `"refreshIntervalSec":2`, 2},
		{1, ``, 0.5},
		{2, `"refreshIntervalSec":5`, 5},
	} {
		t.Run(fmt.Sprintf("v%d-%s", tt.version, tt.interval), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.json")
			input := fmt.Sprintf(`{"schemaVersion":%d,"logLevel":"debug","settings":{"providers":{%s},"sessions":{"previewLines":7},"projects":{"sidebarSort":"name"}}}`, tt.version, tt.interval)
			if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
				t.Fatal(err)
			}
			store, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			got := store.Get()
			if got.Providers.RefreshIntervalSec != tt.want || got.Sessions.PreviewLines != 7 || got.Projects.SidebarSort != "name" || store.LogLevel() != "debug" {
				t.Fatalf("migrated=%+v log=%s", got, store.LogLevel())
			}
			if data, err := os.ReadFile(path); err != nil || string(data) != input {
				t.Fatalf("load rewrote the settings file: %s (%v)", data, err)
			}
			// Choosing the old default after upgrading is a supported preference.
			got.Providers.RefreshIntervalSec = 5
			if _, err := store.Set(got); err != nil {
				t.Fatal(err)
			}
			reloaded, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if reloaded.Get().Providers.RefreshIntervalSec != 5 {
				t.Fatal("re-migrated an explicit preference")
			}
		})
	}
}

func TestRefreshDefault(t *testing.T) {
	if Defaults().Providers.RefreshIntervalSec != 0.5 {
		t.Fatal("refresh default must be half a second")
	}
}

// The next save, of any setting, writes v2 with the migrated interval.
func TestMigrationPersistsOnNextSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	input := `{"schemaVersion":1,"settings":{"providers":{"refreshIntervalSec":5},"sessions":{"previewLines":7}}}`
	if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	next := store.Get()
	next.Sessions.PreviewLines = 9
	if _, err := store.Set(next); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var disk diskFile
	if err := json.Unmarshal(data, &disk); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if disk.SchemaVersion != schemaVersion || reloaded.Get().Providers.RefreshIntervalSec != 0.5 {
		t.Fatalf("save after migration: version=%d settings=%+v", disk.SchemaVersion, reloaded.Get())
	}
}

func TestUnsupportedVersionListsAcceptedSchemas(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"schemaVersion":3}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "expected 1 or 2") {
		t.Fatalf("incorrect supported schema versions: %v", err)
	}
}
