import type { SessionCount } from '@homeshell/model';
import { countsSummary } from '@homeshell/model';
import styles from './session-counts.module.css';
import { StatusDot } from './status-dot';

export interface SessionCountsProps {
  counts: readonly SessionCount[];
}

/**
 * A workstream's sessions at a glance: a dot and a number per state that
 * occurs, no words. The words are on hover and in the accessible name.
 */
export function SessionCounts({ counts }: SessionCountsProps) {
  if (counts.length === 0) return null;
  const summary = `Sessions: ${countsSummary(counts)}`;
  return (
    <span
      className={styles.counts}
      role="img"
      aria-label={summary}
      title={summary}
      data-testid="workstream-sessions"
    >
      {counts.map(({ kind, count }) => (
        <span key={kind} className={styles.count} data-kind={kind}>
          <StatusDot state={kind} still />
          {count}
        </span>
      ))}
    </span>
  );
}

export default SessionCounts;
