// Package store holds the daemon's in-memory view of the projects and fans
// out change events to subscribers (the SSE handlers). Every mutation
// recomputes the derived states and broadcasts the full state, so clients
// never merge patches — they replace what they have.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"maps"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/git"
	"github.com/eclipsesource/homeshell/daemon/internal/persist"
)

// ErrDuplicateWorkingDir is returned by AddWorkstream when a workstream with the
// same non-empty working directory already exists in the project. WorkingDir is
// the session-discovery join key, so it must be unique within a project.
var ErrDuplicateWorkingDir = errors.New("working directory taken")

// ErrDuplicateName is returned when a workstream would take a name another in the
// same project already answers to. Per project, trimmed and case-insensitive.
//
// It guards the doors a *user* comes through (AddWorkstream, UpdateWorkstream)
// and not ReplaceProjection: an adopted workstream is named after its
// directory's base, so two discovered directories can collide, and refusing to
// project one would hide a live session behind a label clash.
var ErrDuplicateName = errors.New("a workstream with this name already exists in this project")

// ErrDuplicateSessionTitle is returned when a rename would give a session the
// title another in the same workstream is already showing. ErrDuplicateName one
// level down, with the same comparison.
//
// It binds the rename alone: a provider naming two of its own sessions "Claude
// session" is by design, and ResetSessionTitle must always be available or a
// rename would be a one-way door. Compared against what each row is currently
// showing, since two identical rows on screen is the thing being prevented.
var ErrDuplicateSessionTitle = errors.New("a session in this workstream is already called that")

// PlaceholderIDPrefix marks a creation-in-progress workstream. The store needs it
// because the name rule does: a placeholder holds the name its real workstream is
// about to take, so counting it would make every named create collide with
// itself. provider.PlaceholderIDPrefix is defined as this.
const PlaceholderIDPrefix = "creating-"

// StartingIDPrefix marks the placeholder session the daemon holds while a provider
// brings the real one up. Two of the store's rules turn on it, both in
// ReplaceProjection: a placeholder is never written to disk, and a projection
// that does not know about one must not drop it. provider.StartingIDPrefix is
// defined as this.
const StartingIDPrefix = "starting-"

// isStartingPlaceholder reports whether a session is the daemon's own
// creation bookkeeping rather than one a provider is reporting.
// provider.IsStartingPlaceholder is the same predicate for callers elsewhere.
func isStartingPlaceholder(sess domain.Session) bool {
	return strings.HasPrefix(sess.ID, StartingIDPrefix)
}

// NameTaken reports the workstream among workstreams, a project's, that already
// answers to name, ignoring exceptIDs — the workstream being inserted or renamed.
// It is the store's name rule, for callers that check a project's workstreams
// before handing them to the store.
//
// A creation placeholder counts only while its create is still running, and that
// is what makes this check a **reservation** rather than a snapshot. Two
// concurrent creates of one name must not both be accepted: the loser would fail
// minutes later, after its directory had been materialized, on a clash the user
// was never warned about. A placeholder left in error state is the opposite case
// — the attempt it stood for is over, so holding its name hostage would refuse
// the retry — and so is the caller's own earlier self, which exceptIDs cover.
//
// An empty name never collides: it is not a name yet, and the provider fills one
// in during creation.
func NameTaken(workstreams []domain.Workstream, name string, exceptIDs ...string) (domain.Workstream, bool) {
	want := strings.TrimSpace(name)
	if want == "" {
		return domain.Workstream{}, false
	}
	for _, existing := range workstreams {
		if slices.Contains(exceptIDs, existing.ID) {
			continue
		}
		if strings.HasPrefix(existing.ID, PlaceholderIDPrefix) && existing.RuntimeState != domain.RuntimeCreating {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(existing.Name), want) {
			return existing, true
		}
	}
	return domain.Workstream{}, false
}

// currentSessionTitleLocked is the title a session is showing right now, or ""
// if it is not in this workstream (setSessionTitleLocked reports that).
func currentSessionTitleLocked(workstream *domain.Workstream, sessionID string) string {
	for _, sess := range workstream.Sessions {
		if sess.ID == sessionID {
			return sess.Title
		}
	}
	return ""
}

// sessionTitleTakenLocked reports the session in workstream already showing
// title, ignoring exceptID (the one being renamed).
func sessionTitleTakenLocked(workstream *domain.Workstream, title, exceptID string) (domain.Session, bool) {
	want := strings.TrimSpace(title)
	if want == "" {
		return domain.Session{}, false
	}
	for _, existing := range workstream.Sessions {
		if existing.ID == exceptID {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(existing.Title), want) {
			return existing, true
		}
	}
	return domain.Session{}, false
}

// Event is one server→client push: a monotonically increasing id and a
// pre-marshaled JSON payload.
type Event struct {
	ID   uint64
	Type string
	Data []byte
}

// subscriberBuffer is how far a subscriber may fall behind before it is
// dropped. A dropped client reconnects and starts from a fresh snapshot.
const subscriberBuffer = 16

// Store is safe for concurrent use.
type Store struct {
	mu       sync.Mutex
	projects []domain.Project
	lastID   uint64
	subs     map[chan Event]struct{}
	// saver receives the persisted projection of the store (snapshotLocked)
	// after every change meant to outlive a restart. Nil in tests that only
	// read the store.
	saver persist.Saver
	// unsaved is set while the last write failed, so RetryUnsavedWrite knows
	// there is something to retry.
	unsaved  bool
	detector git.Detector
	// sessionTitles are the user-chosen session titles, keyed by session ID. A
	// session's title otherwise belongs to its provider, which re-reports it on
	// every discovery cycle — so a rename has to live outside the projection and be
	// applied over it (applyTitlesLocked). Loaded once at startup; written only
	// when somebody renames.
	sessionTitles map[string]persist.CustomTitle
	// launchConfigs are what each session was last started with, keyed by session
	// ID (launchconfig.go). Loaded once at startup; written on create and restart.
	launchConfigs map[string]persist.LaunchConfig
	// unprojectedPromotions are the sessions and workstreams promoted from a
	// placeholder, and the workstreams added, since their project was last
	// re-projected, keyed by their ID to their project's. The projection in
	// flight when one was promoted or added may predate it, so the next swap
	// carries it over (retainInFlightLocked) rather than dropping it — and a
	// session what it was started with, a workstream the name it holds; the swap
	// after that decides.
	unprojectedPromotions map[string]string
	// unprojectedRemovals are the workstreams removed since their project was
	// last re-projected, keyed by their ID to their project's. The projection in
	// flight when one was removed may still hold it, so the next swap drops it
	// rather than bringing it back.
	unprojectedRemovals map[string]string
	// now is the clock that stamps Session.EndedAt; tests replace it.
	now func() time.Time
	// unobserved are the IDs of sessions restored from disk that no projection has
	// reported on yet. Their restored state is a placeholder (idle), so the first
	// observation says nothing about when they ended (stampEndedAtLocked).
	unobserved map[string]bool
}

// New creates a store with the given initial projects, persisting through saver.
// The projects may carry live state (demo mode seeds runtime states); what is
// persisted is still only the snapshot projection.
func New(projects []domain.Project, saver persist.Saver, opts ...Option) *Store {
	for i := range projects {
		projects[i].Recompute()
	}
	s := &Store{
		projects:              projects,
		saver:                 saver,
		subs:                  map[chan Event]struct{}{},
		sessionTitles:         map[string]persist.CustomTitle{},
		launchConfigs:         map[string]persist.LaunchConfig{},
		unprojectedPromotions: map[string]string{},
		unprojectedRemovals:   map[string]string{},
		now:                   time.Now,
		unobserved:            map[string]bool{},
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Restore creates a store from a persisted snapshot: the projects it describes,
// and the custom titles and launch configs that are read back before the first
// projection, which is what makes a rename outlive a restart.
func Restore(snapshot persist.Snapshot, saver persist.Saver, opts ...Option) *Store {
	restored := New(snapshot.DomainProjects(), saver, opts...)
	if snapshot.SessionTitles != nil {
		restored.sessionTitles = maps.Clone(snapshot.SessionTitles)
	}
	if snapshot.LaunchConfigs != nil {
		restored.launchConfigs = maps.Clone(snapshot.LaunchConfigs)
	}
	// Titles for the sessions restored from persistence: this is the one
	// projection the reconciler has not run yet. No lock is taken (or needed),
	// nothing else holds this store until it is returned.
	for i := range restored.projects {
		sessions := projectSessions(&restored.projects[i])
		restored.applyTitlesLocked(sessions)
		for _, session := range sessions {
			restored.unobserved[session.ID] = true
		}
	}
	return restored
}

// snapshotLocked is the persisted projection of the store: every project with
// its workstreams and last-known sessions, minus the daemon's own creation
// bookkeeping, plus the custom titles and launch configs. It is the one place
// that decides what reaches disk.
//
// A creation placeholder is left out because it stands for a create that only
// exists in this process, and the record cannot hold what makes it a
// placeholder (no runtime state, no status message): a daemon killed mid-create
// would otherwise restore it as an ordinary workstream with no working directory
// and no account of itself, which nothing can finish, refresh or explain. A
// starting placeholder session is left out for the same reason
// (AddEphemeralSession says why at length): a restart must forget it.
func (s *Store) snapshotLocked() persist.Snapshot {
	snapshot := persist.Snapshot{
		Projects:      make([]persist.ProjectRecord, 0, len(s.projects)),
		SessionTitles: maps.Clone(s.sessionTitles),
		LaunchConfigs: maps.Clone(s.launchConfigs),
	}
	for _, project := range s.projects {
		record := persist.RecordProject(project)
		for _, workstream := range project.Workstreams {
			if strings.HasPrefix(workstream.ID, PlaceholderIDPrefix) {
				continue
			}
			workstreamRecord := persist.RecordWorkstream(workstream)
			for _, session := range workstream.Sessions {
				if isStartingPlaceholder(session) {
					continue
				}
				workstreamRecord.Sessions = append(workstreamRecord.Sessions, persist.RecordSession(session))
			}
			record.Workstreams = append(record.Workstreams, workstreamRecord)
		}
		snapshot.Projects = append(snapshot.Projects, record)
	}
	return snapshot
}

// saveLocked hands the current snapshot to the saver. The in-memory state is
// never rolled back here: the change is live either way, and the cost of a
// failed write is that change being lost at the next restart. Callers that
// need the other direction (ResetSessionTitle) roll back themselves.
func (s *Store) saveLocked() error {
	if s.saver == nil {
		return nil
	}
	if err := s.saver.Save(s.snapshotLocked()); err != nil {
		s.unsaved = true
		return fmt.Errorf("persisting state: %w", err)
	}
	s.unsaved = false
	return nil
}

// persistLocked is saveLocked for the mutations whose caller cannot act on a
// failed write: it is logged, and the change stays live.
func (s *Store) persistLocked() {
	if err := s.saveLocked(); err != nil {
		slog.Error("persisting state", "error", err)
	}
}

// RetryUnsavedWrite writes the state again if the last write failed. The
// reconciler calls it every tick, so a failed write is not left waiting for an
// unrelated change (or forever, when the change removed the last project).
func (s *Store) RetryUnsavedWrite() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.unsaved {
		s.persistLocked()
	}
}

// applyTitlesLocked overwrites each session's provider-reported title with the
// user's, where one was set. It edits the sessions in place, so callers pass the
// sessions they are about to store rather than a copy handed to a reader.
func (s *Store) applyTitlesLocked(sessions sessionWalk) {
	for _, session := range sessions {
		if custom, ok := s.sessionTitles[session.ID]; ok {
			session.Title = custom.Title
		}
	}
}

// sessionWalk visits sessions, each with the ID of the workstream listing it.
type sessionWalk = iter.Seq2[string, *domain.Session]

// sessionsOf walks every session a project holds: each workstream's, then the
// ones hidden on each untracked worktree, whose workstream ID is "". It yields
// the slices' own sessions, so a caller may edit them in place. Every walk that
// decides whether a session is listed goes through here: a hidden session is
// listed as much as one a workstream shows.
func sessionsOf(workstreams []domain.Workstream, untracked []domain.UntrackedWorktree) sessionWalk {
	return func(yield func(string, *domain.Session) bool) {
		for i := range workstreams {
			for j := range workstreams[i].Sessions {
				if !yield(workstreams[i].ID, &workstreams[i].Sessions[j]) {
					return
				}
			}
		}
		for i := range untracked {
			for j := range untracked[i].Sessions {
				if !yield("", &untracked[i].Sessions[j]) {
					return
				}
			}
		}
	}
}

// projectSessions is sessionsOf a project's workstreams and suggestions.
func projectSessions(project *domain.Project) sessionWalk {
	return sessionsOf(project.Workstreams, project.UntrackedWorktrees)
}

// Option configures a Store at creation time.
type Option func(*Store)

// WithGitDetector installs the git-backing detector used when projects are
// added and refreshed. Without it, GitBacked is left as authored.
func WithGitDetector(detector git.Detector) Option {
	return func(s *Store) { s.detector = detector }
}

// Snapshot returns the current full state as a "state" event carrying the
// id of the last change that contributed to it.
func (s *Store) Snapshot() Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Event{ID: s.lastID, Type: "state", Data: s.marshalLocked()}
}

func (s *Store) marshalLocked() []byte {
	data, err := json.Marshal(struct {
		Projects []domain.Project `json:"projects"`
	}{s.projects})
	if err != nil {
		// Static types marshal unconditionally; this cannot happen.
		panic(err)
	}
	return data
}

// Subscribe registers for change events. The returned cancel function must
// be called when the subscriber goes away; the channel is closed either by
// cancel or by the store when the subscriber falls too far behind.
func (s *Store) Subscribe() (<-chan Event, func()) {
	eventCh := make(chan Event, subscriberBuffer)
	s.mu.Lock()
	s.subs[eventCh] = struct{}{}
	s.mu.Unlock()
	cancel := func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if _, ok := s.subs[eventCh]; ok {
			delete(s.subs, eventCh)
			close(eventCh)
		}
	}
	return eventCh, cancel
}

// broadcastLocked pushes the new state to subscribers without persisting, for
// ephemeral changes clients need to see but that are not written to disk.
func (s *Store) broadcastLocked() {
	s.lastID++
	event := Event{ID: s.lastID, Type: "state", Data: s.marshalLocked()}
	for subscriber := range s.subs {
		select {
		case subscriber <- event:
		default:
			// Too slow: drop it; the client reconnects for a snapshot.
			delete(s.subs, subscriber)
			close(subscriber)
		}
	}
}

// Projects returns a snapshot copy of the current projects, down to their
// sessions. Callers read it without the lock, so it must share no array a
// mutation writes into (Recompute alone rewrites every workstream).
func (s *Store) Projects() []domain.Project {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.Project, len(s.projects))
	for i, project := range s.projects {
		out[i] = cloneProject(project)
	}
	return out
}

// Project returns a copy of one project, like Projects.
func (s *Store) Project(projectID string) (domain.Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, project := range s.projects {
		if project.ID == projectID {
			return cloneProject(project), nil
		}
	}
	return domain.Project{}, fmt.Errorf("project %q not found", projectID)
}

// cloneProject gives a project its own workstream and suggestion arrays, down
// to their sessions.
func cloneProject(project domain.Project) domain.Project {
	project.Workstreams = slices.Clone(project.Workstreams)
	for i := range project.Workstreams {
		project.Workstreams[i] = cloneWorkstream(project.Workstreams[i])
	}
	project.UntrackedWorktrees = slices.Clone(project.UntrackedWorktrees)
	for i := range project.UntrackedWorktrees {
		project.UntrackedWorktrees[i].Sessions = slices.Clone(project.UntrackedWorktrees[i].Sessions)
	}
	return project
}

// Workstream returns a copy of one workstream, down to its sessions.
func (s *Store) Workstream(projectID, workstreamID string) (domain.Workstream, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, workstream, err := s.findLocked(projectID, workstreamID)
	if err != nil {
		return domain.Workstream{}, err
	}
	return cloneWorkstream(*workstream), nil
}

// cloneWorkstream gives a workstream its own session array, which the store's
// setters write into in place.
func cloneWorkstream(workstream domain.Workstream) domain.Workstream {
	workstream.Sessions = slices.Clone(workstream.Sessions)
	return workstream
}

// ReplaceProjection swaps a project's workstreams and untracked worktree
// suggestions with the given sets, recomputes derived state, and persists and
// broadcasts only when they actually changed (a reconcile wave would otherwise
// overflow a slow SSE subscriber). read is the project's workstreams as the
// sets were derived from them: what changed in the store since then is kept
// (keepChangesSinceRead). The store takes ownership of both sets: callers must
// not reuse them. The sessions of a suggestion count as listed: their titles
// and launch configs are kept, and they are stamped when they end.
func (s *Store) ReplaceProjection(projectID string, read, workstreams []domain.Workstream,
	untracked []domain.UntrackedWorktree) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.replaceProjectionLocked(projectID, read, workstreams, untracked)
}

// replaceProjectionLocked is ReplaceProjection with s.mu held.
func (s *Store) replaceProjectionLocked(projectID string, read, workstreams []domain.Workstream,
	untracked []domain.UntrackedWorktree) error {
	for i := range s.projects {
		if s.projects[i].ID != projectID {
			continue
		}
		if workstreams == nil {
			workstreams = []domain.Workstream{}
		}
		workstreams = keepChangesSinceRead(read, s.projects[i].Workstreams, workstreams, untracked)
		// Anything the daemon started tracking while this projection was being
		// computed is missing from it, and must survive the swap.
		workstreams = slices.DeleteFunc(workstreams, func(workstream domain.Workstream) bool {
			return s.unprojectedRemovals[workstream.ID] == projectID
		})
		workstreams = retainInFlightLocked(s.projects[i].Workstreams, workstreams, untracked, s.unprojectedPromotions)
		untracked = handOverTakenSuggestions(workstreams, untracked)
		s.forgetUnprojectedLocked(projectID)
		previousSessions := projectSessions(&s.projects[i])
		incomingSessions := sessionsOf(workstreams, untracked)
		// The providers just reported these sessions under their own titles; a
		// session the user renamed keeps the name they gave it. Applied before the
		// snapshot is written so the projection and the file agree.
		s.applyTitlesLocked(incomingSessions)
		keepPlaceholderLinks(previousSessions, incomingSessions)
		s.stampEndedAtLocked(previousSessions, incomingSessions)
		listed := sessionIDsIn(incomingSessions)
		s.forgetUnobservedLocked(previousSessions, listed)
		pruned := s.pruneTitlesLocked(projectID, listed)
		pruned = s.pruneLaunchConfigsLocked(projectID, listed) || pruned
		// Held for the change check below. Recompute normalizes derived fields, so
		// the comparison has to be made between two recomputed sets; it mutates the
		// new slices only, leaving these to compare against.
		previous := s.projects[i].Workstreams
		previousUntracked := s.projects[i].UntrackedWorktrees
		s.projects[i].Workstreams = workstreams
		s.projects[i].UntrackedWorktrees = untracked
		s.projects[i].Recompute()
		if pruned || !reflect.DeepEqual(previous, s.projects[i].Workstreams) ||
			!sameSuggestions(previousUntracked, s.projects[i].UntrackedWorktrees) {
			// Placeholders (workstream and session) are kept out of the snapshot by
			// snapshotLocked, so the projection carrying them through every cycle
			// cannot persist the very records a restart has to forget. An unchanged
			// set is not encoded at all: at a sub-second poll that is the hot path.
			s.persistLocked()
			s.broadcastLocked()
		}
		return nil
	}
	return fmt.Errorf("project %q not found", projectID)
}

// sameSuggestions reports whether two sets of suggestions look the same to a
// client: the sessions hidden on them are not shown, only how many are live.
func sameSuggestions(previous, current []domain.UntrackedWorktree) bool {
	return slices.EqualFunc(previous, current, func(was, now domain.UntrackedWorktree) bool {
		return was.Name == now.Name && was.Branch == now.Branch && was.Path == now.Path &&
			was.SessionCount == now.SessionCount
	})
}

// stampEndedAtLocked sets Session.EndedAt on the incoming sessions from what the
// previous projection knew of them. A session that was live and is now terminal
// ended just now; one that stays terminal keeps its recorded time; a live one has
// none. A session not in the previous projection, or restored from disk and not
// yet observed, was first seen in this state, so no end is claimed for it beyond
// the persisted one.
//
// A restored session stays unobserved until a projection brings it changed: the
// reconciler carries a session its provider did not report over as the stored
// copy, which is still the restored idle placeholder and says nothing about it.
func (s *Store) stampEndedAtLocked(previous, incoming sessionWalk) {
	before := map[string]domain.Session{}
	for _, session := range previous {
		before[session.ID] = *session
	}
	now := s.now()
	for _, session := range incoming {
		prev, known := before[session.ID]
		first := s.unobserved[session.ID]
		if first && known && sameReport(prev, *session) {
			continue
		}
		delete(s.unobserved, session.ID)
		var endedAt *time.Time
		switch {
		case !session.State.Terminal():
		case !known:
			// First seen already ended: a time the provider reports stands.
			endedAt = session.EndedAt
		case first || prev.EndedAt != nil:
			// After a restart the persisted time (nil if none) stands: the first
			// look cannot tell when the session ended, and now() would be a lie.
			endedAt = prev.EndedAt
		case !prev.State.Terminal():
			stamp := now
			endedAt = &stamp
		}
		session.EndedAt = endedAt
	}
}

// forgetUnobservedLocked drops the restored sessions of previous that are no
// longer listed from the unobserved set: one that comes back is first seen anew.
func (s *Store) forgetUnobservedLocked(previous sessionWalk, listed map[string]bool) {
	for _, session := range previous {
		if !listed[session.ID] {
			delete(s.unobserved, session.ID)
		}
	}
}

// sameReport reports whether incoming is the stored session carried over as is,
// ignoring Restartable and Ended, which the projection and Recompute derive anew.
func sameReport(stored, incoming domain.Session) bool {
	stored.Restartable, incoming.Restartable = false, false
	stored.Ended, incoming.Ended = false, false
	return reflect.DeepEqual(stored, incoming)
}

// retainInFlightLocked re-attaches the daemon's own creation bookkeeping —
// placeholder workstreams and placeholder sessions — that the live state holds
// but the incoming projection does not.
//
// A projection is computed from a snapshot taken *before* the providers were
// asked, and discovery is slow enough (a subprocess per provider) to leave a
// window of seconds. A create that starts inside that window is invisible to the
// projection, so replacing the state wholesale would drop the placeholder the
// user is watching — for the rest of a multi-minute build, with no way back:
// nothing re-reports a placeholder, because no provider knows one exists.
//
// Only placeholders are carried, and that is what makes the rule safe rather
// than a general "never lose anything" merge. They are the rows a projection can
// never legitimately contain, so their absence is always staleness and never a
// decision; everything else a provider reports on is the projection's to settle.
// A placeholder that is genuinely finished is not here to carry either, because
// whoever owns it (Service.finishCreate, Service.finishCreateSession) ends its
// life by promoting or removing it in the live state first.
//
// The one exception is a session or workstream promoted from a placeholder, or a
// workstream added (AddWorkstream), since the last swap (unprojected): the
// projection may have been computed before it existed, so its absence says
// nothing yet. It is carried through this one swap — and with a session its
// launch config, which pruning would otherwise delete for good — and the next
// projection, computed after it was promoted or added, settles it like any other.
//
// The live state is the authority on placeholders the other way too: a starting
// placeholder the projection carries from its snapshot but the live state no
// longer holds was promoted or removed meanwhile, and is dropped rather than
// brought back as a row that never finishes starting.
func retainInFlightLocked(live, next []domain.Workstream, nextUntracked []domain.UntrackedWorktree,
	unprojected map[string]string) []domain.Workstream {
	next = settlePlaceholders(live, next)
	starting := map[string]bool{}
	for _, was := range live {
		for _, sess := range was.Sessions {
			if isStartingPlaceholder(sess) {
				starting[sess.ID] = true
			}
		}
	}
	byID := make(map[string]int, len(next))
	for i := range next {
		next[i].Sessions = slices.DeleteFunc(next[i].Sessions, func(sess domain.Session) bool {
			return isStartingPlaceholder(sess) && !starting[sess.ID]
		})
		byID[next[i].ID] = i
	}
	// A session is retained only where the projection lists it nowhere: one it
	// placed elsewhere, or hid on a suggestion, would otherwise be listed twice.
	listed := sessionIDsIn(sessionsOf(next, nextUntracked))
	for _, was := range live {
		i, ok := byID[was.ID]
		_, promoted := unprojected[was.ID]
		if !ok && (strings.HasPrefix(was.ID, PlaceholderIDPrefix) || promoted) {
			byID[was.ID] = len(next)
			next = append(next, was)
			maps.Copy(listed, sessionIDsIn(sessionsOf([]domain.Workstream{was}, nil)))
			continue
		}
		if !ok || strings.HasPrefix(was.ID, PlaceholderIDPrefix) {
			continue
		}
		for _, sess := range was.Sessions {
			_, promoted := unprojected[sess.ID]
			if !(isStartingPlaceholder(sess) || promoted) || listed[sess.ID] {
				continue
			}
			listed[sess.ID] = true
			// Appended rather than restored to its old index: a placeholder is added
			// at the end of the list and nothing reorders one, so the end is where it
			// was.
			next[i].Sessions = append(next[i].Sessions, sess)
		}
	}
	return next
}

// keepChangesSinceRead carries onto a projection what changed in the live state
// since the projection read it. A pass asks the providers between its read and
// this write, so whatever changed meanwhile (a rename, a dismissed status, a
// workstream added by Work here, a removal) is newer than the pass's copy:
//
//   - a workstream or session changed since the read is taken from the live
//     state whole, so fields that belong together come from one writer; the
//     next pass brings it up to date;
//   - one removed since the read stays removed, and one added since stays;
//   - a name the pass gave that another workstream now has is not taken.
//
// Placeholders are left to retainInFlightLocked, and a nil read (no read to
// compare with) keeps the projection as it is.
func keepChangesSinceRead(read, live, next []domain.Workstream,
	untracked []domain.UntrackedWorktree) []domain.Workstream {
	if read == nil {
		return next
	}
	readWorkstreams, liveWorkstreams := workstreamsByID(read), workstreamsByID(live)
	readSessions, liveSessions := sessionsByID(read), sessionsByID(live)
	listed := sessionIDsIn(sessionsOf(next, untracked))
	kept := make([]domain.Workstream, 0, len(next))
	for _, workstream := range next {
		was, wasRead := readWorkstreams[workstream.ID]
		if strings.HasPrefix(workstream.ID, PlaceholderIDPrefix) || !wasRead {
			kept = append(kept, workstream)
			continue
		}
		now, isLive := liveWorkstreams[workstream.ID]
		if !isLive {
			continue
		}
		if !sameApartFromSessions(was, now) {
			sessions := workstream.Sessions
			workstream = now
			workstream.Sessions = sessions
		}
		workstream.Sessions = keepSessionChanges(workstream.Sessions, now.Sessions, readSessions, liveSessions, listed)
		kept = append(kept, workstream)
	}
	projected := workstreamsByID(next)
	for _, now := range live {
		_, wasRead := readWorkstreams[now.ID]
		_, isProjected := projected[now.ID]
		if wasRead || isProjected || strings.HasPrefix(now.ID, PlaceholderIDPrefix) {
			continue
		}
		kept = append(kept, now)
	}
	for i := range kept {
		now, isLive := liveWorkstreams[kept[i].ID]
		if isLive && kept[i].Name != now.Name {
			if _, taken := NameTaken(kept, kept[i].Name, kept[i].ID); taken {
				kept[i].Name, kept[i].CreatorData = now.Name, now.CreatorData
			}
		}
	}
	return kept
}

// keepSessionChanges is keepChangesSinceRead for the sessions of one
// workstream: projected are the projection's, current the live state's. A
// session added since the read is kept only where the projection lists it
// nowhere (listed).
func keepSessionChanges(projected, current []domain.Session, readSessions, liveSessions map[string]domain.Session, listed map[string]bool) []domain.Session {
	sessions := make([]domain.Session, 0, len(projected))
	for _, sess := range projected {
		was, wasRead := readSessions[sess.ID]
		now, isLive := liveSessions[sess.ID]
		switch {
		case isStartingPlaceholder(sess) || !wasRead:
		case !isLive:
			continue
		case !reflect.DeepEqual(was, now):
			sess = now
		}
		sessions = append(sessions, sess)
	}
	for _, now := range current {
		if _, wasRead := readSessions[now.ID]; wasRead || listed[now.ID] || isStartingPlaceholder(now) {
			continue
		}
		sessions = append(sessions, now)
	}
	return sessions
}

// sameApartFromSessions reports whether two copies of a workstream agree on
// everything but their sessions.
func sameApartFromSessions(a, b domain.Workstream) bool {
	a.Sessions, b.Sessions = nil, nil
	return reflect.DeepEqual(a, b)
}

// workstreamsByID indexes workstreams by ID.
func workstreamsByID(workstreams []domain.Workstream) map[string]domain.Workstream {
	byID := make(map[string]domain.Workstream, len(workstreams))
	for _, workstream := range workstreams {
		byID[workstream.ID] = workstream
	}
	return byID
}

// sessionsByID indexes the sessions workstreams list by ID.
func sessionsByID(workstreams []domain.Workstream) map[string]domain.Session {
	byID := map[string]domain.Session{}
	for _, workstream := range workstreams {
		for _, sess := range workstream.Sessions {
			byID[sess.ID] = sess
		}
	}
	return byID
}

// handOverTakenSuggestions drops each suggestion whose folder a workstream
// works in, and gives that workstream the sessions hidden there. Why: a
// projection computed before a workstream was added or promoted in a folder
// may still suggest it, and the view must not show both.
func handOverTakenSuggestions(workstreams []domain.Workstream,
	untracked []domain.UntrackedWorktree) []domain.UntrackedWorktree {
	return slices.DeleteFunc(untracked, func(suggestion domain.UntrackedWorktree) bool {
		i := slices.IndexFunc(workstreams, func(workstream domain.Workstream) bool {
			return domain.SameFolder(workstream.WorkingDir, suggestion.Path)
		})
		if i < 0 {
			return false
		}
		for _, session := range suggestion.Sessions {
			if !hasSession(workstreams[i].Sessions, session.ID) {
				workstreams[i].Sessions = append(workstreams[i].Sessions, session)
			}
		}
		return true
	})
}

// forgetUnprojectedLocked drops a project's unprojected promotions and
// removals, once a projection has been swapped in over them.
func (s *Store) forgetUnprojectedLocked(projectID string) {
	for id, promotedIn := range s.unprojectedPromotions {
		if promotedIn == projectID {
			delete(s.unprojectedPromotions, id)
		}
	}
	maps.DeleteFunc(s.unprojectedRemovals, func(_, removedFrom string) bool { return removedFrom == projectID })
}

// settlePlaceholders replaces each creation placeholder a projection carries from
// its snapshot with what the live state holds for it now: the placeholder itself,
// the real workstream it was promoted to (which the projection has never seen and
// would otherwise drop), or nothing once it has been removed. The live state is
// the only authority on placeholders; the snapshot's copy is always stale.
func settlePlaceholders(live, next []domain.Workstream) []domain.Workstream {
	current := map[string]domain.Workstream{}
	for _, workstream := range live {
		if strings.HasPrefix(workstream.ID, PlaceholderIDPrefix) {
			current[workstream.ID] = workstream
		} else if workstream.PlaceholderID != "" {
			current[workstream.PlaceholderID] = workstream
		}
	}
	nextIDs := map[string]bool{}
	for _, workstream := range next {
		nextIDs[workstream.ID] = true
	}
	settled := make([]domain.Workstream, 0, len(next))
	for _, workstream := range next {
		if !strings.HasPrefix(workstream.ID, PlaceholderIDPrefix) {
			settled = append(settled, workstream)
			continue
		}
		now, ok := current[workstream.ID]
		if ok && (now.ID == workstream.ID || !nextIDs[now.ID]) {
			settled = append(settled, now)
		}
	}
	return settled
}

// keepPlaceholderLinks carries each live session's PlaceholderID onto the
// projection's entry for it, which its provider reports without one.
func keepPlaceholderLinks(live, next sessionWalk) {
	links := map[string]string{}
	for _, session := range live {
		if session.PlaceholderID != "" {
			links[session.ID] = session.PlaceholderID
		}
	}
	for _, session := range next {
		if link := links[session.ID]; link != "" {
			session.PlaceholderID = link
		}
	}
}

// hasSession reports whether a session list already contains an ID.
func hasSession(sessions []domain.Session, id string) bool {
	for _, sess := range sessions {
		if sess.ID == id {
			return true
		}
	}
	return false
}

// sessionIDsIn is the set of the IDs of sessions.
func sessionIDsIn(sessions sessionWalk) map[string]bool {
	ids := map[string]bool{}
	for _, session := range sessions {
		ids[session.ID] = true
	}
	return ids
}

// pruneTitlesLocked forgets the custom titles of this project's sessions that
// are not listed in the projection about to replace it — the sessions whose providers
// have stopped reporting them, which is the only way a session disappears
// without anyone asking the store to remove it (a torn-down Enclave container,
// a tmux session that exited).
//
// It is safe against a provider that merely failed to answer this cycle: the
// reconciler retains such a provider's sessions in the projection rather than
// dropping them, so they are still here to match. A session that really is gone
// is gone from the daemon entirely, and its title would otherwise sit in the
// state file forever waiting for an ID that will never come back. The caller
// persists.
func (s *Store) pruneTitlesLocked(projectID string, listed map[string]bool) (pruned bool) {
	for sessionID, custom := range s.sessionTitles {
		if custom.ProjectID != projectID || listed[sessionID] {
			continue
		}
		delete(s.sessionTitles, sessionID)
		pruned = true
	}
	return pruned
}

// forgetTitleLocked drops one session's custom title, for a session being removed
// outright. The name goes with the session it named: the ID it is keyed on will
// not be handed out again, so keeping it would only be a record nothing can ever
// match. The caller persists.
func (s *Store) forgetTitleLocked(sessionID string) {
	delete(s.sessionTitles, sessionID)
}

// RenameSession gives a session a display title of the user's choosing, and
// broadcasts the new state.
//
// Session titles are otherwise the owning provider's: it reports one with every
// session it discovers (Enclave names the agent, Local reads the agent's own
// terminal title), and the reconciler writes those over the store on each cycle.
// A rename is therefore recorded beside the projection rather than in it, and
// re-applied to every later cycle — which is also what makes it survive the
// session's provider being restarted or the daemon going down.
//
// The title is stored as given (the caller trims it); an empty one is rejected,
// because a session with no name in the list is worse than one named after its
// agent — ResetSessionTitle is how a caller asks for that name back.
func (s *Store) RenameSession(projectID, workstreamID, sessionID, title string) error {
	if title == "" {
		return errors.New("a session title must not be empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, workstream, err := s.findLocked(projectID, workstreamID)
	if err != nil {
		return err
	}
	// Only a rename that *changes* what this row shows can introduce a duplicate.
	// Re-typing the title a session already displays is a no-op to the reader, and
	// refusing it would make the by-design collision unrenameable: two Claude
	// sessions both show "claude session", so pinning that on one of them would
	// otherwise be refused by the other.
	if !strings.EqualFold(strings.TrimSpace(currentSessionTitleLocked(workstream, sessionID)), strings.TrimSpace(title)) {
		if clash, taken := sessionTitleTakenLocked(workstream, title, sessionID); taken {
			return fmt.Errorf("%w: %q", ErrDuplicateSessionTitle, clash.Title)
		}
	}
	if err := s.setSessionTitleLocked(projectID, workstreamID, sessionID, title); err != nil {
		return err
	}
	s.sessionTitles[sessionID] = persist.CustomTitle{ProjectID: projectID, Title: title}
	// Memory is not rolled back if the write fails, matching AddSession: the name
	// is live either way and the cost of the failure is a rename lost at the next
	// restart. ResetSessionTitle *does* roll back, because its failure is the other
	// direction: a name still on disk would come back from the dead.
	// Broadcast whether or not the write lands: the rename is live either way.
	s.broadcastLocked()
	if err := s.saveLocked(); err != nil {
		return fmt.Errorf("persisting custom session title: %w", err)
	}
	return nil
}

// ResetSessionTitle drops a session's custom title so the owning provider's own
// title shows again, and broadcasts the new state.
//
// The provider is not asked for the title here: it re-reports one on the next
// reconcile cycle, and until then the stale custom title would be a worse answer
// than none. What the row shows in between is the title the provider last
// reported, which the store still has for every session it did not rename.
//
// Resetting a session that was never renamed succeeds: two clients clearing the
// same name must both be right, and the outcome the caller asked for holds
// either way.
func (s *Store) ResetSessionTitle(projectID, workstreamID, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	custom, renamed := s.sessionTitles[sessionID]
	if !renamed {
		// Still resolve the session, so a wrong ID is reported rather than
		// silently succeeding.
		_, _, err := s.findSessionLocked(projectID, workstreamID, sessionID)
		return err
	}
	delete(s.sessionTitles, sessionID)
	if err := s.saveLocked(); err != nil {
		// Put it back: a title that is still on disk but not in memory would come
		// back on the next restart.
		s.sessionTitles[sessionID] = custom
		return fmt.Errorf("persisting custom session title delete: %w", err)
	}
	s.broadcastLocked()
	return nil
}

// setSessionTitleLocked writes a title onto the stored session.
func (s *Store) setSessionTitleLocked(projectID, workstreamID, sessionID, title string) error {
	_, workstream, err := s.findSessionLocked(projectID, workstreamID, sessionID)
	if err != nil {
		return err
	}
	for i := range workstream.Sessions {
		if workstream.Sessions[i].ID == sessionID {
			workstream.Sessions[i].Title = title
		}
	}
	return nil
}

// findSessionLocked resolves a session within a workstream within a project,
// reporting which of the three was not found.
func (s *Store) findSessionLocked(projectID, workstreamID, sessionID string) (*domain.Project, *domain.Workstream, error) {
	project, workstream, err := s.findLocked(projectID, workstreamID)
	if err != nil {
		return nil, nil, err
	}
	for _, sess := range workstream.Sessions {
		if sess.ID == sessionID {
			return project, workstream, nil
		}
	}
	return nil, nil, fmt.Errorf("session %q not found in workstream %q", sessionID, workstreamID)
}

// NameUser returns the name of the workstream in the project, other than
// exceptIDs, that already answers to name, or "" — the rule AddWorkstream and
// UpdateWorkstream enforce.
func (s *Store) NameUser(projectID, name string, exceptIDs ...string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.projects {
		if s.projects[i].ID == projectID {
			existing, _ := NameTaken(s.projects[i].Workstreams, name, exceptIDs...)
			return existing.Name
		}
	}
	return ""
}

// WorkingDirUser names the workstream in the project already working in dir, or
// returns "" — the rule AddWorkstream and PromoteWorkstream enforce, and what
// the creation form and an in-place create are checked against.
func (s *Store) WorkingDirUser(projectID, dir string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.projects {
		if s.projects[i].ID == projectID {
			existing, _ := workingDirTakenLocked(&s.projects[i], dir)
			return existing.Name
		}
	}
	return ""
}

// workingDirTakenLocked reports the workstream in project already working in
// dir, ignoring exceptIDs. An empty dir — a placeholder's — never collides. The
// project folder is taken by any workstream shown in it (InProjectFolder), so a
// pending or failed in-place create, which has no folder yet, holds it too.
func workingDirTakenLocked(project *domain.Project, dir string, exceptIDs ...string) (domain.Workstream, bool) {
	projectFolder := domain.SameFolder(dir, project.Path)
	for _, existing := range project.Workstreams {
		if slices.Contains(exceptIDs, existing.ID) {
			continue
		}
		if domain.SameFolder(existing.WorkingDir, dir) || projectFolder && existing.InProjectFolder {
			return existing, true
		}
	}
	return domain.Workstream{}, false
}

// AddWorkstream inserts a single workstream into a project's workstream list,
// recomputes derived state, persists and broadcasts. Its usual caller adds a
// transient placeholder (a creation-in-progress workstream), which the snapshot
// leaves out, so one never survives a daemon restart; a real workstream added
// this way is written like any other.
//
// It rejects a workstream whose non-empty WorkingDir duplicates one already in
// the project (ErrDuplicateWorkingDir): the working directory is the join key
// session discovery matches on, so it must be unique. Placeholders (empty
// WorkingDir) never collide. The check-and-insert is atomic under the lock, so
// two concurrent creates targeting the same directory cannot both win.
//
// It also rejects a duplicate *name* (ErrDuplicateName, per project, trimmed and
// case-insensitive). Because this is the same door a placeholder comes through,
// asking for a name that is already taken is refused **before** any directory is
// materialized — the caller gets the error from Create itself rather than
// watching a workstream fail minutes later.
//
// The store takes ownership of the workstream's sessions: callers must not reuse
// the slice.
func (s *Store) AddWorkstream(projectID string, workstream domain.Workstream) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.projects {
		if s.projects[i].ID != projectID {
			continue
		}
		if err := uniqueLocked(&s.projects[i], workstream, workstream.ID); err != nil {
			return err
		}
		s.projects[i].Workstreams = append(s.projects[i].Workstreams, workstream)
		s.unprojectedPromotions[workstream.ID] = projectID
		s.projects[i].Recompute()
		s.persistLocked()
		s.broadcastLocked()
		return nil
	}
	return fmt.Errorf("project %q not found", projectID)
}

// uniqueLocked runs the two per-project uniqueness rules a workstream entering
// the store has to satisfy — its working directory (the join key) and its name
// (what a human picks it out by) — ignoring exceptIDs, the records this one is
// itself replacing.
func uniqueLocked(project *domain.Project, workstream domain.Workstream, exceptIDs ...string) error {
	if existing, taken := workingDirTakenLocked(project, workstream.WorkingDir, exceptIDs...); taken {
		return fmt.Errorf("%w: %q already works in %s", ErrDuplicateWorkingDir, existing.Name, workstream.WorkingDir)
	}
	if clash, taken := NameTaken(project.Workstreams, workstream.Name, exceptIDs...); taken {
		// Quote the *existing* workstream's name rather than the requested one:
		// they may differ only in case or padding, and seeing which name is
		// already there is what tells the user that.
		return fmt.Errorf("%w: %q", ErrDuplicateName, clash.Name)
	}
	return nil
}

// PromoteWorkstream swaps a creation placeholder for the real workstream it
// stood for, in one mutation: the placeholder is dropped and the real workstream
// takes its position in the list.
//
// One mutation for the same reasons PromoteEphemeralSession is, plus one of its
// own. Add-then-remove broadcasts an intermediate state holding both, so a
// client would briefly see the workstream twice and the row would jump; and the
// name rule counts a placeholder while its create is running, so adding the real
// workstream separately would be refused by the very placeholder that reserved
// its name. Excluding the placeholder from its own checks is what makes that
// reservation coherent — it was held *for this*.
//
// Every other workstream is still checked, so a name or directory that was taken
// while the provider worked is refused here, with the placeholder left exactly as
// it was for the caller to turn into the record of the failure. The store takes
// ownership of the workstream's sessions: callers must not reuse the slice.
func (s *Store) PromoteWorkstream(projectID, placeholderID string, workstream domain.Workstream) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.projects {
		if s.projects[i].ID != projectID {
			continue
		}
		if err := uniqueLocked(&s.projects[i], workstream, workstream.ID, placeholderID); err != nil {
			return err
		}
		workstream.PlaceholderID = placeholderID
		s.unprojectedPromotions[workstream.ID] = projectID
		next := make([]domain.Workstream, 0, len(s.projects[i].Workstreams)+1)
		replaced := false
		for _, existing := range s.projects[i].Workstreams {
			if existing.ID == placeholderID {
				next = append(next, workstream)
				replaced = true
				continue
			}
			next = append(next, existing)
		}
		if !replaced {
			// The placeholder is gone — removed by the user while the provider was
			// still working. What it stood for exists now regardless, and a workstream
			// the daemon cannot see is one nobody can ever remove; it lands, and the
			// user can delete it for real.
			next = append(next, workstream)
		}
		s.projects[i].Workstreams = next
		s.projects[i].Recompute()
		s.persistLocked()
		s.broadcastLocked()
		return nil
	}
	return fmt.Errorf("project %q not found", projectID)
}

// RemoveWorkstream removes a single workstream from a project's workstream
// list by ID, recomputes derived state, and broadcasts+persists.
func (s *Store) RemoveWorkstream(projectID, workstreamID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.projects {
		if s.projects[i].ID != projectID {
			continue
		}
		for j := range s.projects[i].Workstreams {
			if s.projects[i].Workstreams[j].ID == workstreamID {
				for _, sess := range s.projects[i].Workstreams[j].Sessions {
					s.forgetTitleLocked(sess.ID)
					s.forgetLaunchConfigLocked(sess.ID)
				}
				s.projects[i].Workstreams = append(s.projects[i].Workstreams[:j], s.projects[i].Workstreams[j+1:]...)
				s.unprojectedRemovals[workstreamID] = projectID
				s.projects[i].Recompute()
				s.persistLocked()
				s.broadcastLocked()
				return nil
			}
		}
		return fmt.Errorf("workstream %q not found in project %q", workstreamID, projectID)
	}
	return fmt.Errorf("project %q not found", projectID)
}

// UpdateWorkstream replaces a workstream in a project's workstream list
// (matched by ID) with the given one, recomputes derived state, persists and
// broadcasts.
//
// This is the door a **rename** comes through, so it enforces the same name rule
// as AddWorkstream (ErrDuplicateName) — otherwise the rule would be one PATCH
// away from being bypassed. The workstream being updated is excluded from the
// comparison, so re-saving one under its own name is never a conflict.
//
// Only an update that *changes* the name is checked, the same reasoning as
// RenameSession one level down: a duplicate can only be introduced by a name
// that moves, and re-saving the name a workstream already wears is a no-op to
// the reader. It also keeps the rule from blocking the daemon's own writes — a
// failed create rewrites its placeholder in place, and that update must land, or
// the create that failed *because* the name was taken leaves a "creating…"
// spinner nothing will ever resolve. The store takes ownership of the
// workstream's sessions: callers must not reuse the slice.
func (s *Store) UpdateWorkstream(projectID string, workstream domain.Workstream) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.projects {
		if s.projects[i].ID != projectID {
			continue
		}
		for j := range s.projects[i].Workstreams {
			if s.projects[i].Workstreams[j].ID == workstream.ID {
				renamed := !strings.EqualFold(
					strings.TrimSpace(s.projects[i].Workstreams[j].Name),
					strings.TrimSpace(workstream.Name),
				)
				if renamed {
					if clash, taken := NameTaken(s.projects[i].Workstreams, workstream.Name, workstream.ID); taken {
						// Quote the *existing* workstream's name rather than the requested one:
						// they may differ only in case or padding, and seeing which name is
						// already there is what tells the user that.
						return fmt.Errorf("%w: %q", ErrDuplicateName, clash.Name)
					}
				}
				s.projects[i].Workstreams[j] = workstream
				s.projects[i].Recompute()
				s.persistLocked()
				s.broadcastLocked()
				return nil
			}
		}
		return fmt.Errorf("workstream %q not found in project %q", workstream.ID, projectID)
	}
	return fmt.Errorf("project %q not found", projectID)
}

// AddSession appends a session to a workstream and broadcasts the new state.
func (s *Store) AddSession(projectID, workstreamID string, session domain.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	project, workstream, err := s.findLocked(projectID, workstreamID)
	if err != nil {
		return err
	}
	workstream.Sessions = append(workstream.Sessions, session)
	project.Recompute()
	// Broadcast whether or not the write lands: the session is live either way.
	s.broadcastLocked()
	if err := s.saveLocked(); err != nil {
		return fmt.Errorf("persisting session insert: %w", err)
	}
	return nil
}

// AddEphemeralSession appends a session that is deliberately **not** written to
// disk, and broadcasts the new state.
//
// This exists for the starting-session placeholder the daemon holds while a
// provider creates a session. Persisting it would be a bug, not an omission: a
// daemon killed mid-build would restore a session no provider can ever refresh
// (the creation it stood for is gone), leaving an immortal row that no reconcile
// can remove. Not persisting means a restart simply forgets it, which is the
// correct outcome — the build died with the daemon.
func (s *Store) AddEphemeralSession(projectID, workstreamID string, session domain.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	project, workstream, err := s.findLocked(projectID, workstreamID)
	if err != nil {
		return err
	}
	workstream.Sessions = append(workstream.Sessions, session)
	project.Recompute()
	s.broadcastLocked()
	return nil
}

// RemoveEphemeralSession drops a session without touching persistence, for a
// session that was never written (see AddEphemeralSession). Removing an absent
// session is not an error: the caller is unwinding and only cares that it is
// gone.
func (s *Store) RemoveEphemeralSession(projectID, workstreamID, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	project, workstream, err := s.findLocked(projectID, workstreamID)
	if err != nil {
		return err
	}
	remaining := slices.DeleteFunc(workstream.Sessions, func(sess domain.Session) bool { return sess.ID == sessionID })
	found := len(remaining) < len(workstream.Sessions)
	workstream.Sessions = remaining
	if found {
		project.Recompute()
		s.broadcastLocked()
	}
	return nil
}

// FailEphemeralSession turns an ephemeral placeholder into the record of a
// creation that failed: it takes domain.SessionError with message as its Error,
// and stays where it is in the list. Its preview (the provider's startup output)
// is left alone — it is usually what explains the failure.
//
// The row survives from here on because the reconciler retains starting-prefixed
// sessions unconditionally and nothing else writes to it. It is still never
// persisted, so a restart forgets the failed attempt, which is right: the attempt
// died with the daemon.
//
// A missing session is an error rather than a no-op, because the caller has a
// failure it must put *somewhere* — provider.Service falls back to the
// workstream's status message when this reports the placeholder gone.
func (s *Store) FailEphemeralSession(projectID, workstreamID, sessionID, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	project, workstream, err := s.findLocked(projectID, workstreamID)
	if err != nil {
		return err
	}
	found := false
	for i := range workstream.Sessions {
		if workstream.Sessions[i].ID == sessionID {
			workstream.Sessions[i].State = domain.SessionError
			workstream.Sessions[i].Error = message
			found = true
		}
	}
	if !found {
		return fmt.Errorf("session %q not found in workstream %q", sessionID, workstreamID)
	}
	project.Recompute()
	s.broadcastLocked()
	return nil
}

// PromoteEphemeralSession swaps an ephemeral placeholder for the real session it
// stood for, in one mutation: the placeholder is dropped, the real session takes
// its position in the list, and only the real one is persisted.
//
// Doing it under a single lock is what keeps the swap invisible. Remove-then-add
// would broadcast an intermediate state with neither session present, so clients
// would see the row vanish and reappear, and anything counting sessions between
// the two mutations (the concurrency cap) would read a count that was never
// true. Keeping the position means the row does not jump either.
//
// A projection may already list the real session, which its provider records
// before the create returns; that entry is then refreshed in place.
//
// What the session was started with (launch) is recorded in the same mutation, so
// there is no moment the session is listed without it. A launch with no provider
// records nothing.
func (s *Store) PromoteEphemeralSession(projectID, workstreamID, placeholderID string, session domain.Session, launch domain.LaunchConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	project, workstream, err := s.findLocked(projectID, workstreamID)
	if err != nil {
		return err
	}
	if launch.ProviderID != "" {
		s.recordLaunchConfigLocked(projectID, session.ID, launch)
	}
	session.PlaceholderID = placeholderID
	s.unprojectedPromotions[session.ID] = projectID
	listed := hasSession(workstream.Sessions, session.ID)
	placed := false
	for i, sess := range workstream.Sessions {
		if sess.ID == session.ID || sess.ID == placeholderID && !listed {
			workstream.Sessions[i] = session
			placed = true
		}
	}
	workstream.Sessions = slices.DeleteFunc(workstream.Sessions, func(sess domain.Session) bool { return sess.ID == placeholderID })
	if !placed {
		// The placeholder is gone (a concurrent removal, or the workstream was
		// re-projected). The real session still belongs here.
		workstream.Sessions = append(workstream.Sessions, session)
	}
	project.Recompute()
	// Broadcast whether or not the write lands: the session is live either way.
	s.broadcastLocked()
	if err := s.saveLocked(); err != nil {
		return fmt.Errorf("persisting session insert: %w", err)
	}
	return nil
}

// SetSessionPreview updates one session's terminal-output tail and broadcasts
// the new state. Ephemeral by definition — domain.Session.Preview is recomputed
// on every capture and never persisted.
func (s *Store) SetSessionPreview(projectID, workstreamID, sessionID, preview string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, workstream, err := s.findLocked(projectID, workstreamID)
	if err != nil {
		return err
	}
	found := false
	for i := range workstream.Sessions {
		if workstream.Sessions[i].ID == sessionID {
			workstream.Sessions[i].Preview = preview
			found = true
		}
	}
	if !found {
		return fmt.Errorf("session %q not found in workstream %q", sessionID, workstreamID)
	}
	s.broadcastLocked()
	return nil
}

// SetSessionState updates one session's state and broadcasts the new state.
func (s *Store) SetSessionState(projectID, workstreamID, sessionID string, state domain.SessionState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	project, workstream, err := s.findLocked(projectID, workstreamID)
	if err != nil {
		return err
	}
	for i := range workstream.Sessions {
		if workstream.Sessions[i].ID == sessionID {
			workstream.Sessions[i].State = state
			project.Recompute()
			s.broadcastLocked()
			return nil
		}
	}
	return fmt.Errorf("session %q not found in workstream %q", sessionID, workstreamID)
}

// SetRuntimeState updates a workstream's runtime state and broadcasts the
// new state. This is an ephemeral change (not persisted): runtime states are
// provider-reported and re-established on reconnect.
func (s *Store) SetRuntimeState(projectID, workstreamID string, state domain.RuntimeState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	project, workstream, err := s.findLocked(projectID, workstreamID)
	if err != nil {
		return err
	}
	workstream.RuntimeState = state
	project.Recompute()
	s.broadcastLocked()
	return nil
}

// SetStatusMessage updates a workstream's human-readable status message and
// broadcasts the new state. Like SetRuntimeState this is ephemeral (not
// persisted): status messages describe transient progress.
func (s *Store) SetStatusMessage(projectID, workstreamID, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, workstream, err := s.findLocked(projectID, workstreamID)
	if err != nil {
		return err
	}
	workstream.StatusMessage = message
	s.broadcastLocked()
	return nil
}

// RemoveSession deletes a session and broadcasts the new state.
func (s *Store) RemoveSession(projectID, workstreamID, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	project, workstream, err := s.findLocked(projectID, workstreamID)
	if err != nil {
		return err
	}
	for i := range workstream.Sessions {
		if workstream.Sessions[i].ID == sessionID {
			workstream.Sessions = append(workstream.Sessions[:i], workstream.Sessions[i+1:]...)
			project.Recompute()
			s.forgetTitleLocked(sessionID)
			s.forgetLaunchConfigLocked(sessionID)
			s.persistLocked()
			s.broadcastLocked()
			return nil
		}
	}
	return fmt.Errorf("session %q not found in workstream %q", sessionID, workstreamID)
}

// AddProject appends a new project and broadcasts the new state. It rejects
// duplicates by path so the same directory cannot be added twice. The passed
// project is enriched in place with the derived fields (GitBacked, normalized
// collections and state) so callers can echo the stored value. The store takes
// ownership of the project's slices: callers must not modify them afterwards.
func (s *Store) AddProject(project *domain.Project) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.projects {
		if existing.Path == project.Path {
			return fmt.Errorf("a project already exists for path %q", project.Path)
		}
	}
	if project.Workstreams == nil {
		project.Workstreams = []domain.Workstream{}
	}
	if s.detector != nil {
		project.GitBacked = s.detector.Detect(project.Path)
	}
	project.Recompute()
	s.projects = append(s.projects, *project)
	s.persistLocked()
	s.broadcastLocked()
	return nil
}

// RenameProject updates a project's display name and broadcasts the new state.
func (s *Store) RenameProject(projectID, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.projects {
		if s.projects[i].ID == projectID {
			s.projects[i].Name = name
			s.persistLocked()
			s.broadcastLocked()
			return nil
		}
	}
	return fmt.Errorf("project %q not found", projectID)
}

// SessionHome is a project, and a workstream in it, that lists a session.
type SessionHome struct {
	ProjectID string
	// WorkstreamID is "" for a session hidden on an untracked worktree.
	WorkstreamID string
}

// Hidden reports whether the project only hides the session on an untracked
// worktree: no workstream there lists it.
func (home SessionHome) Hidden() bool { return home.WorkstreamID == "" }

// RemoveProject deletes a project and broadcasts the new state. The
// underlying directory is not touched. It returns where each of the project's
// sessions another project still lists moved to (rehomeSessionRecordsLocked).
func (s *Store) RemoveProject(projectID string) (map[string]SessionHome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.projects {
		if s.projects[i].ID == projectID {
			removed := s.projects[i]
			s.projects = append(s.projects[:i], s.projects[i+1:]...)
			rehomed := s.rehomeSessionRecordsLocked(removed)
			s.forgetUnprojectedLocked(projectID)
			s.persistLocked()
			s.broadcastLocked()
			return rehomed, nil
		}
	}
	return nil, fmt.Errorf("project %q not found", projectID)
}

// rehomeSessionRecordsLocked settles the sessions of a removed project — those
// it lists, and those it holds a custom title or launch config of — and returns
// where each that another project still lists moved to: the last project
// listing it. Such a session keeps its records, moved to that project: a
// restart started in the removed project recorded its launch there, and may be
// left to finish for the other. The rest go with the project. The caller
// persists.
func (s *Store) rehomeSessionRecordsLocked(removed domain.Project) map[string]SessionHome {
	listedBy := map[string]SessionHome{}
	for i := range s.projects {
		for workstreamID, listed := range projectSessions(&s.projects[i]) {
			listedBy[listed.ID] = SessionHome{ProjectID: s.projects[i].ID, WorkstreamID: workstreamID}
		}
	}
	rehomed := map[string]SessionHome{}
	rehome := func(sessionID string) (SessionHome, bool) {
		home, listed := listedBy[sessionID]
		if listed {
			rehomed[sessionID] = home
		}
		return home, listed
	}
	for _, listed := range projectSessions(&removed) {
		rehome(listed.ID)
	}
	for sessionID, custom := range s.sessionTitles {
		if custom.ProjectID != removed.ID {
			continue
		}
		home, listed := rehome(sessionID)
		if !listed {
			delete(s.sessionTitles, sessionID)
			continue
		}
		s.sessionTitles[sessionID] = persist.CustomTitle{ProjectID: home.ProjectID, Title: custom.Title}
	}
	for sessionID, launch := range s.launchConfigs {
		if launch.ProjectID != removed.ID {
			continue
		}
		home, listed := rehome(sessionID)
		if !listed {
			delete(s.launchConfigs, sessionID)
			continue
		}
		launch.ProjectID = home.ProjectID
		s.launchConfigs[sessionID] = launch
	}
	return rehomed
}

// RefreshGitBacked re-detects whether a project is git-backed and broadcasts
// the new state, for a project added before `git init` ran in its directory.
// Without a detector installed the detection is a no-op.
func (s *Store) RefreshGitBacked(projectID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.projects {
		if s.projects[i].ID == projectID {
			if s.detector != nil {
				s.projects[i].GitBacked = s.detector.Detect(s.projects[i].Path)
			}
			s.persistLocked()
			s.broadcastLocked()
			return nil
		}
	}
	return fmt.Errorf("project %q not found", projectID)
}

func (s *Store) findLocked(projectID, workstreamID string) (*domain.Project, *domain.Workstream, error) {
	for i := range s.projects {
		project := &s.projects[i]
		if project.ID != projectID {
			continue
		}
		for j := range project.Workstreams {
			workstream := &project.Workstreams[j]
			if workstream.ID == workstreamID {
				return project, workstream, nil
			}
		}
		return nil, nil, fmt.Errorf("workstream %q not found in project %q", workstreamID, projectID)
	}
	return nil, nil, fmt.Errorf("project %q not found", projectID)
}
