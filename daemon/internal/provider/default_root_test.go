package provider

import (
	"path/filepath"
	"testing"

	"github.com/eclipsesource/homeshell/daemon/internal/persist"
)

func TestDefaultWorkstreamsRootIsSharedButNotExpandedByCustomLocation(t *testing.T) {
	previous := persist.HomeDir()
	base := t.TempDir()
	persist.SetHomeDir(base)
	t.Cleanup(func() { persist.SetHomeDir(previous) })
	want := filepath.Join(base, "workstreams")
	c := newCore()
	if got, err := DefaultWorkstreamsRoot(); err != nil || c.root != want || got != want {
		t.Fatal("providers disagree on default managed root")
	}
	custom := filepath.Join(t.TempDir(), "custom")
	c.location = func() string { return custom }
	got, err := c.resolveRoot(t.TempDir())
	if err != nil || got != custom {
		t.Fatalf("custom location=%s err=%v", got, err)
	}
	if got, _ := DefaultWorkstreamsRoot(); got != want {
		t.Fatal("user location changed the daemon-owned default root")
	}
}
