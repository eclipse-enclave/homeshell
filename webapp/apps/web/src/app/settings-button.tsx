import styles from './settings-button.module.css';

export interface SettingsButtonProps {
  onClick: () => void;
}

/** Top-bar icon button that opens the settings page. */
export function SettingsButton({ onClick }: SettingsButtonProps) {
  const label = 'Settings';
  return (
    <button
      type="button"
      className={styles.button}
      onClick={onClick}
      aria-label={label}
      title={label}
    >
      <span role="img" aria-label={label}>
        ⚙️
      </span>
    </button>
  );
}

export default SettingsButton;
