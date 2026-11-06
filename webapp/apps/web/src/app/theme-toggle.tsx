import { resolveTheme, toggledTheme, useTheme } from './use-theme';
import styles from './theme-toggle.module.css';

/**
 * Top-bar one-click theme switch. Theme is a fast, frequently-toggled
 * per-device preference, so it gets a toolbar button in addition to the full
 * three-way choice (system/light/dark) in Settings › Appearance. It writes the
 * same browser-local `hs-theme` the settings control does; this button just
 * flips between light and dark based on what is currently showing.
 */
export function ThemeToggle() {
  const { theme, setTheme } = useTheme();
  const current = resolveTheme(theme);
  const next = toggledTheme(theme);
  const label = `Switch to ${next} theme`;
  return (
    <button
      type="button"
      className={styles.button}
      onClick={() => setTheme(next)}
      aria-label={label}
      title={label}
    >
      <span role="img" aria-hidden="true">
        {current === 'dark' ? '☀️' : '🌙'}
      </span>
    </button>
  );
}

export default ThemeToggle;
