//go:build dev

package store

import (
	"testing"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/persist"
)

// NewTestStore creates a Store that persists into a MemorySaver, for any test
// package that needs a Store without wiring real persistence. The seed is
// saved once, so Saved reflects it before the first mutation, the way a store
// restored from disk would.
func NewTestStore(t *testing.T, projects []domain.Project, opts ...Option) *Store {
	t.Helper()
	testStore := New(projects, &persist.MemorySaver{}, opts...)
	testStore.mu.Lock()
	defer testStore.mu.Unlock()
	if err := testStore.saveLocked(); err != nil {
		t.Fatalf("saving the seed: %v", err)
	}
	return testStore
}

// Saved is the last snapshot a store built by NewTestStore handed its saver:
// what would be on disk now.
func Saved(t *testing.T, testStore *Store) persist.Snapshot {
	t.Helper()
	saver, ok := testStore.saver.(*persist.MemorySaver)
	if !ok {
		t.Fatalf("store saver is %T, want *persist.MemorySaver (use NewTestStore)", testStore.saver)
	}
	return saver.Last
}
