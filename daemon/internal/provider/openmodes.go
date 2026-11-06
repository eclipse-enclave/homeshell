package provider

import (
	"os"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/settings"
)

// OpenModesFor derives the ways a workstream can be handed to the user right
// now: the user's "open with" rows, plus the links a live session's provider
// declares. Must stay cheap and must not probe: it runs for every workstream on
// every projection cycle.
func (s *Service) OpenModesFor(ws domain.Workstream) []domain.OpenMode {
	return openModes(ws, s.registry, s.openWithEntries(), materialized(ws.WorkingDir))
}

// annotate fills the fields only the daemon host can answer, at projection time:
// a project's workstreams' open modes and the other actions available right now,
// and which of their sessions can be restarted. It takes the whole projection so
// the user's "open with" list is read once per cycle, and answers "are the files
// on this host" once per workstream — both derivations need it, and it is a stat
// call on every workstream every cycle.
func (s *Service) annotate(projectID string, workstreams []domain.Workstream) {
	entries := s.openWithEntries()
	for i := range workstreams {
		hasDir := materialized(workstreams[i].WorkingDir)
		workstreams[i].OpenModes = openModes(workstreams[i], s.registry, entries, hasDir)
		workstreams[i].Actions = actions(hasDir)
		workstreams[i].ProviderLabel = providerLabel(workstreams[i].Provider, s.registry)
		for j := range workstreams[i].Sessions {
			session := &workstreams[i].Sessions[j]
			ref := SessionRef{ProjectID: projectID, WorkstreamID: workstreams[i].ID, SessionID: session.ID}
			session.Restartable = s.restartable(ref, *session, "")
		}
	}
}

// providerLabel is the label of the workstream or session provider providerID
// names (an adopted workstream carries its first session's); the core, being
// the default, gets none.
func providerLabel(providerID string, registry *Registry) string {
	if providerID == CoreProviderID {
		return ""
	}
	if workstreamProvider, ok := registry.WorkstreamProvider(providerID); ok {
		return workstreamProvider.Label()
	}
	if sessionProvider, ok := registry.SessionProvider(providerID); ok {
		return sessionProvider.Label()
	}
	return ""
}

// actions derives the workstream-level actions beside the open modes. The note
// lives among the workstream's files, so without them there is nowhere to keep
// one and the action is not offered.
func actions(hasDir bool) []domain.Action {
	list := []domain.Action{}
	if hasDir {
		list = append(list, domain.Action{
			ID:          domain.ActionNote,
			Label:       "Notes",
			Description: "Open this workstream's notes in your desktop's default application",
		})
	}
	return list
}

// openModes is OpenModesFor's testable core: reg supplies the session providers
// whose shells and link declarations become modes, entries the user's "open
// with" list, and hasDir whether the working directory is really on this host
// (passed in so a projection cycle stats each workstream once).
func openModes(ws domain.Workstream, reg *Registry, entries []settings.OpenWithEntry, hasDir bool) []domain.OpenMode {
	modes := []domain.OpenMode{}
	// The user's rows, in list order. Each opens the folder on this host (the
	// terminal row, with an empty command, a shell in it), so none is offered
	// without one.
	if hasDir {
		for _, entry := range entries {
			modes = append(modes, domain.OpenMode{
				ID: domain.OpenWithModeFor(entry.ID), Label: entry.Label,
				Default: entry.ID == settings.DefaultTerminalEntryID,
			})
		}
	}
	if reg == nil {
		return modes
	}
	// A shell inside each live session that runs in an environment of its own,
	// in session order: sessions in different containers are different places.
	shells := shellSessions(ws, reg)
	for _, sess := range shells {
		modes = append(modes, domain.OpenMode{
			ID: domain.SessionShellModeFor(sess.ID), Label: shellLabel(sess, shells),
		})
	}
	// The links live sessions' providers declare, in session order, one mode per
	// distinct link id. Resolving the URL happens only at open time.
	seenLinks := map[string]bool{}
	for _, sess := range ws.Sessions {
		if sess.State == domain.SessionStopped {
			continue
		}
		p, ok := reg.SessionProvider(sess.Provider)
		if !ok {
			continue
		}
		opener, ok := p.(LinkOpener)
		if !ok {
			continue
		}
		for _, link := range opener.Links(sess) {
			if link.ID == "" || seenLinks[link.ID] {
				continue
			}
			seenLinks[link.ID] = true
			modes = append(modes, domain.OpenMode{
				ID: domain.LinkModeFor(link.ID), Label: link.Label, OpensInBrowser: true,
			})
		}
	}
	return modes
}

// shellSessions lists the workstream's sessions a shell can be opened inside:
// live ones (hasEnvironment) whose provider runs them in an environment of
// their own.
func shellSessions(ws domain.Workstream, reg *Registry) []domain.Session {
	var out []domain.Session
	for _, sess := range ws.Sessions {
		if !hasEnvironment(sess.State) {
			continue
		}
		p, ok := reg.SessionProvider(sess.Provider)
		if !ok {
			continue
		}
		if _, ok := p.(ShellOpener); ok {
			out = append(out, sess)
		}
	}
	return out
}

// hasEnvironment reports whether a session in this state has a runtime behind
// it. An ended one (done, stopped, a failed create) has none, and a starting one
// is still the placeholder for a container being built.
func hasEnvironment(state domain.SessionState) bool {
	return !state.Terminal() && state != domain.SessionStarting
}

// shellLabel names the shell mode for sess by its session title, or its ID when
// it has none. Two sessions of one agent share a title, so a title that is not
// unique among shells carries the session ID too.
func shellLabel(sess domain.Session, shells []domain.Session) string {
	if sess.Title == "" {
		return "Shell in " + sess.ID
	}
	for _, other := range shells {
		if other.ID != sess.ID && other.Title == sess.Title {
			return "Shell in " + sess.Title + " (" + sess.ID + ")"
		}
	}
	return "Shell in " + sess.Title
}

// materialized reports whether the working directory exists on this host.
func materialized(dir string) bool {
	if dir == "" {
		return false
	}
	info, err := os.Stat(dir)
	return err == nil && info.IsDir()
}
