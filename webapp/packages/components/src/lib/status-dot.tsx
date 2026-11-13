import type { DotState } from '@homeshell/model';
import { dotStateLabel } from '@homeshell/model';
import styles from './status-dot.module.css';

export interface StatusDotProps {
  state: DotState;
  /** Also render the human-readable state label next to the dot. */
  showLabel?: boolean;
  /** Never pulse, e.g. for counts that only summarise. */
  still?: boolean;
}

type Shape = 'filled' | 'ringed' | 'bang' | 'hollow' | 'dashed';

/**
 * Each state's look besides its colour, so state is never colour alone, and
 * whether it pulses (the states that are working or want attention).
 */
const LOOKS: Record<DotState, { shape: Shape; pulses?: true }> = {
  idle: { shape: 'filled' },
  running: { shape: 'filled', pulses: true },
  waiting: { shape: 'ringed', pulses: true },
  done: { shape: 'filled' },
  stopped: { shape: 'hollow' },
  ended: { shape: 'hollow' },
  stopping: { shape: 'filled', pulses: true },
  starting: { shape: 'filled', pulses: true },
  creating: { shape: 'filled', pulses: true },
  error: { shape: 'bang' },
  unknown: { shape: 'dashed' },
};

/** Colored indicator for a session, workstream, or project state. */
export function StatusDot({
  state,
  showLabel = false,
  still = false,
}: StatusDotProps) {
  const label = dotStateLabel(state);
  // A state this build does not know yet still gets a dot.
  const { shape, pulses } = LOOKS[state] ?? LOOKS.idle;
  const pulse = pulses && !still ? ` ${styles.pulse}` : '';
  return (
    <span className={styles.wrapper}>
      <span
        className={`${styles.dot} ${styles[state]}${pulse}`}
        role="img"
        aria-label={label}
        title={label}
        data-shape={shape}
      >
        {shape === 'bang' && '!'}
      </span>
      {showLabel && <span className={styles.label}>{label}</span>}
    </span>
  );
}

export default StatusDot;
