package persist

import (
	"strings"
	"testing"
)

func TestDefaultDir(t *testing.T) {
	// With XDG_DATA_HOME set.
	t.Setenv("XDG_DATA_HOME", "/custom/data")
	dir, err := DefaultDir()
	if err != nil {
		t.Fatalf("DefaultDir with XDG: %v", err)
	}
	if dir != "/custom/data/homeshell" {
		t.Errorf("got %q, want /custom/data/homeshell", dir)
	}

	// Without XDG_DATA_HOME — falls back to platform default.
	t.Setenv("XDG_DATA_HOME", "")
	dir, err = DefaultDir()
	if err != nil {
		t.Fatalf("DefaultDir without XDG: %v", err)
	}
	if !strings.HasSuffix(dir, "homeshell") {
		t.Errorf("got %q, want path ending in homeshell", dir)
	}
}

func TestSetHomeDirOverridesDefault(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/should/be/ignored")
	SetHomeDir("/explicit/home")
	t.Cleanup(func() { SetHomeDir("") })

	dir, err := DefaultDir()
	if err != nil {
		t.Fatalf("DefaultDir with SetHomeDir: %v", err)
	}
	if dir != "/explicit/home" {
		t.Errorf("got %q, want /explicit/home", dir)
	}

	// Clearing reverts to normal behavior.
	SetHomeDir("")
	dir, err = DefaultDir()
	if err != nil {
		t.Fatalf("DefaultDir after clear: %v", err)
	}
	if dir != "/should/be/ignored/homeshell" {
		t.Errorf("got %q, want /should/be/ignored/homeshell", dir)
	}
}
