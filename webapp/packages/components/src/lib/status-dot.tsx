import type { EffectiveState } from '@homeshell/model';
import { effectiveStateLabel } from '@homeshell/model';
import styles from './status-dot.module.css';

export interface StatusDotProps {
  state: EffectiveState;
  /** Also render the human-readable state label next to the dot. */
  showLabel?: boolean;
}

/** Colored indicator for a session, workstream, or project state. */
export function StatusDot({ state, showLabel = false }: StatusDotProps) {
  const label = effectiveStateLabel(state);
  return (
    <span className={styles.wrapper}>
      <span
        className={`${styles.dot} ${styles[state]}`}
        role="img"
        aria-label={label}
        title={label}
      />
      {showLabel && <span className={styles.label}>{label}</span>}
    </span>
  );
}

export default StatusDot;
