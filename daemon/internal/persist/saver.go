package persist

import (
	"bytes"
	"os"
)

// Saver is the store's one persistence port: it is handed the whole snapshot
// after every change that is meant to outlive a restart.
type Saver interface {
	Save(snapshot Snapshot) error
}

// FileSaver writes snapshots to the state file, atomically and only when the
// encoded bytes differ from what it last wrote (or found on disk), so a
// reconcile cycle that changes nothing writes nothing. Callers serialize their
// calls; the store does so under its own lock.
type FileSaver struct {
	path string
	last []byte
}

// NewFileSaver returns a saver for the state file at path. What the file holds
// now counts as the last write, so a daemon whose first snapshot matches the
// file it loaded does not rewrite it.
func NewFileSaver(path string) *FileSaver {
	saver := &FileSaver{path: path}
	// An unreadable file leaves last empty; the first Save will find out for real.
	if data, err := os.ReadFile(path); err == nil {
		saver.last = data
	}
	return saver
}

// Path is the file this saver writes.
func (f *FileSaver) Path() string { return f.path }

// Save writes the snapshot if it differs from the last one written.
func (f *FileSaver) Save(snapshot Snapshot) error {
	data, err := encodeState(snapshot)
	if err != nil {
		return err
	}
	if bytes.Equal(data, f.last) {
		return nil
	}
	if err := WriteFileAtomic(f.path, data); err != nil {
		return err
	}
	f.last = data
	return nil
}

// MemorySaver keeps the last snapshot in memory, for demo mode (throwaway data)
// and for tests, which read Last to see what would have reached disk.
type MemorySaver struct {
	Last  Snapshot
	Saves int
	// Err, when set, is returned by every Save, for tests of the failure paths.
	Err error
}

// Save records the snapshot.
func (m *MemorySaver) Save(snapshot Snapshot) error {
	if m.Err != nil {
		return m.Err
	}
	m.Last = snapshot
	m.Saves++
	return nil
}
