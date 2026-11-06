import type { SessionState } from '@homeshell/model';
import { stateLabel } from '@homeshell/model';
import styles from './session-badges.module.css';
import dots from './status-dot.module.css';

/**
 * The order badges appear in: what wants the user first, then what is busy,
 * then what is at rest.
 */
const ORDER: readonly SessionState[] = [
  'waiting',
  'done',
  'error',
  'running',
  'starting',
  'idle',
  'stopped',
  'unknown',
];

export interface SessionBadgesProps {
  /** The state each session is shown in (its display state, `done` included). */
  states: readonly SessionState[];
}

/**
 * A workstream's sessions at a glance, beside its name: one badge per state
 * any session is in — its dot and how many — with the states named on hover.
 * It stands in for both a session count and the workstream's own aggregate
 * state, which it says more precisely: "2 running, 1 waiting" rather than
 * "Waiting". No sessions is a muted 0, so an empty workstream still reads as
 * one at a glance.
 */
export function SessionBadges({ states }: SessionBadgesProps) {
  const counts = new Map<SessionState, number>();
  for (const state of states) counts.set(state, (counts.get(state) ?? 0) + 1);
  const shown = ORDER.filter((state) => counts.has(state));
  const summary =
    shown.length === 0
      ? 'No sessions'
      : shown
          .map(
            (state) =>
              `${counts.get(state)} ${stateLabel(state).toLowerCase()}`,
          )
          .join(', ');
  return (
    <span
      className={styles.badges}
      role="img"
      aria-label={`Sessions: ${summary}`}
      title={summary}
      data-testid="workstream-sessions"
    >
      {shown.length === 0 ? (
        <span className={`${styles.badge} ${styles.empty}`}>0</span>
      ) : (
        shown.map((state) => (
          <span key={state} className={styles.badge} data-state={state}>
            <span className={`${dots.dot} ${dots[state]}`} />
            {counts.get(state)}
          </span>
        ))
      )}
    </span>
  );
}

export default SessionBadges;
