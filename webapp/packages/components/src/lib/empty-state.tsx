import type { ReactNode } from 'react';
import styles from './empty-state.module.css';

export interface EmptyStateProps {
  title: string;
  hint?: string;
  /** Optional call-to-action, e.g. a (not yet functional) create button. */
  action?: ReactNode;
}

/** Friendly placeholder for lists that have nothing to show yet. */
export function EmptyState({ title, hint, action }: EmptyStateProps) {
  return (
    <div className={styles.wrapper}>
      <p className={styles.title}>{title}</p>
      {hint && <p className={styles.hint}>{hint}</p>}
      {action}
    </div>
  );
}

export default EmptyState;
