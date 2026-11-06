package store

import (
	"fmt"
	"slices"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/persist"
)

// SetLaunchConfig records what a session was (re)started with, so a restart can
// replay it and the restart form can open pre-filled. Kept beside the projection
// like a custom title, and for the same reason: session records are rewritten on
// every reconcile cycle.
func (s *Store) SetLaunchConfig(projectID, sessionID string, launch domain.LaunchConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Nothing would ever forget a record of a project that is not there: pruning
	// and removal both go by a project the store holds.
	if !slices.ContainsFunc(s.projects, func(project domain.Project) bool { return project.ID == projectID }) {
		return fmt.Errorf("project %q not found", projectID)
	}
	s.recordLaunchConfigLocked(projectID, sessionID, launch)
	if err := s.saveLocked(); err != nil {
		return fmt.Errorf("persisting session launch config: %w", err)
	}
	return nil
}

// recordLaunchConfigLocked writes the record in memory; the caller persists.
func (s *Store) recordLaunchConfigLocked(projectID, sessionID string, launch domain.LaunchConfig) {
	s.launchConfigs[sessionID] = persist.LaunchConfig{ProjectID: projectID, ProviderID: launch.ProviderID, OptionID: launch.OptionID, Config: launch.Config}
}

// LaunchConfig returns what a session was last started with, and false for a
// session HomeShell has no record of — one it adopted rather than started, or one
// started before launch configs were recorded.
func (s *Store) LaunchConfig(sessionID string) (domain.LaunchConfig, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	launch, ok := s.launchConfigs[sessionID]
	return domain.LaunchConfig{ProviderID: launch.ProviderID, OptionID: launch.OptionID, Config: launch.Config}, ok
}

// ForgetLaunchConfig drops what a session was started with, for a record that
// turned out not to be true.
func (s *Store) ForgetLaunchConfig(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.forgetLaunchConfigLocked(sessionID)
	s.persistLocked()
}

// pruneLaunchConfigsLocked forgets the launch configs of this project's sessions
// that are not listed in the projection about to replace it; see
// pruneTitlesLocked, whose reasoning applies unchanged.
func (s *Store) pruneLaunchConfigsLocked(projectID string, listed map[string]bool) (pruned bool) {
	for sessionID, launch := range s.launchConfigs {
		if launch.ProjectID != projectID || listed[sessionID] {
			continue
		}
		s.forgetLaunchConfigLocked(sessionID)
		pruned = true
	}
	return pruned
}

// forgetLaunchConfigLocked drops one session's launch config, for a session that
// is gone. The caller persists.
func (s *Store) forgetLaunchConfigLocked(sessionID string) {
	delete(s.launchConfigs, sessionID)
}
