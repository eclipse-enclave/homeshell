import styles from './logs-button.module.css';

export interface LogsButtonProps {
  onClick: () => void;
}

/** Top-bar icon button that toggles the dockable log pane. */
export function LogsButton({ onClick }: LogsButtonProps) {
  const label = 'Logs';
  return (
    <button
      type="button"
      className={styles.button}
      onClick={onClick}
      aria-label={label}
      title={label}
    >
      <span role="img" aria-label={label}>
        🗒️
      </span>
    </button>
  );
}

export default LogsButton;
