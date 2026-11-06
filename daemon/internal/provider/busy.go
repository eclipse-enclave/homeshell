package provider

import (
	"errors"
	"fmt"
	"strings"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
)

// BusySession identifies one session that stands in the way of a removal. It is
// the wire shape the API returns alongside the refusal, so a client can name the
// sessions in a confirmation prompt instead of parsing the message.
type BusySession struct {
	ID    string              `json:"id"`
	Title string              `json:"title"`
	State domain.SessionState `json:"state"`
}

// BusyError reports a removal refused because sessions are still busy. It
// wraps ErrWorkstreamBusy (so errors.Is keeps working for callers that only care
// *that* it was refused) and carries the offending sessions for callers that want
// to say which — the same split as ValidationError/FieldErrors.
type BusyError struct {
	Sessions []BusySession
}

// Error names the busy sessions and the way out, so a direct API caller learns
// what to do from the message alone.
func (e *BusyError) Error() string {
	if len(e.Sessions) == 0 {
		return ErrWorkstreamBusy.Error()
	}
	named := make([]string, 0, len(e.Sessions))
	for _, session := range e.Sessions {
		title := session.Title
		if strings.TrimSpace(title) == "" {
			title = session.ID
		}
		named = append(named, fmt.Sprintf("%q (%s)", title, session.State))
	}
	subject := fmt.Sprintf("%d sessions are", len(e.Sessions))
	if len(e.Sessions) == 1 {
		subject = "a session is"
	}
	// "busy", not "running": a session waiting for input blocks the removal too,
	// and naming it "running (waiting)" contradicts itself.
	return fmt.Sprintf(
		"cannot remove the workstream while %s still busy: %s — remove with force to stop %s and delete anyway",
		subject, strings.Join(named, ", "), pluralThem(len(e.Sessions)),
	)
}

// Unwrap exposes the sentinel so errors.Is(err, ErrWorkstreamBusy) holds.
func (e *BusyError) Unwrap() error { return ErrWorkstreamBusy }

// BusySessions extracts the busy-session detail from an error if it is a
// BusyError, returning nil otherwise. Mirrors FieldErrors.
func BusySessions(err error) []BusySession {
	var be *BusyError
	if errors.As(err, &be) {
		return be.Sessions
	}
	return nil
}

// busySessions returns the workstream's sessions that stand in the way of a
// removal — running or waiting for input (domain.SessionState.Busy), in the
// order they are held. This is the single definition of "removing this
// workstream would interrupt live work".
func busySessions(ws domain.Workstream) []domain.Session {
	var busy []domain.Session
	for _, session := range ws.Sessions {
		if session.State.Busy() {
			busy = append(busy, session)
		}
	}
	return busy
}

// newBusyError converts store sessions into the refusal the API serves.
func newBusyError(sessions []domain.Session) *BusyError {
	busy := make([]BusySession, 0, len(sessions))
	for _, session := range sessions {
		busy = append(busy, BusySession{ID: session.ID, Title: session.Title, State: session.State})
	}
	return &BusyError{Sessions: busy}
}

func pluralThem(n int) string {
	if n == 1 {
		return "it"
	}
	return "them"
}
