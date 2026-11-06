import { useEffect, useState } from 'react';
import type { Project, Session, SessionState } from '@homeshell/model';
import { displaySessionState, isSessionActive } from '@homeshell/model';

/**
 * Tracks, per session, whether the user has looked at it since it went idle —
 * client-local state the daemon does not hold. `done` is purely a UI overlay:
 * a session the daemon reports as `idle` that the user has not seen since it
 * finished reads as `done` ("go look"). Opening a session marks it seen; fresh
 * activity (running/waiting) resets it so the next idle is unseen again.
 *
 * The state derives from the SSE snapshots the daemon pushes; nothing here is a
 * domain fact (that all lives in the daemon) — it is presentation only.
 */
export interface SessionAttention {
  displayState: (session: Session) => SessionState;
  markSeen: (sessionId: string) => void;
}

export function useSessionAttention(project: Project | null): SessionAttention {
  const [seen, setSeen] = useState<Record<string, boolean>>({});

  useEffect(() => {
    if (!project) return;
    setSeen((prev) => {
      const next: Record<string, boolean> = {};
      for (const workstream of project.workstreams) {
        for (const session of workstream.sessions) {
          if (isSessionActive(session.state)) {
            // Fresh activity: the eventual idle should read as unseen ("done").
            next[session.id] = false;
          } else if (session.id in prev) {
            next[session.id] = prev[session.id];
          } else {
            // First observed non-active: nothing finished to look at yet.
            next[session.id] = true;
          }
        }
      }
      return next;
    });
  }, [project]);

  return {
    displayState: (session) =>
      displaySessionState(session.state, seen[session.id] ?? true),
    markSeen: (sessionId) => setSeen((prev) => ({ ...prev, [sessionId]: true })),
  };
}
