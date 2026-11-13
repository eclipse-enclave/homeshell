import type { ReactNode } from 'react';
import styles from './top-bar-button.module.css';

export interface TopBarButtonProps {
  label: string;
  icon: ReactNode;
  onClick: () => void;
}

/** A square top-bar icon button, named and titled by `label`. */
export function TopBarButton({ label, icon, onClick }: TopBarButtonProps) {
  return (
    <button
      type="button"
      className={styles.button}
      onClick={onClick}
      aria-label={label}
      title={label}
    >
      {icon}
    </button>
  );
}
