import { MoonIcon, SunIcon } from '@homeshell/components';
import { resolveTheme, toggledTheme, useTheme } from './use-theme';
import { TopBarButton } from './top-bar-button';

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
  const Icon = current === 'dark' ? SunIcon : MoonIcon;
  return (
    <TopBarButton
      label={`Switch to ${next} theme`}
      icon={<Icon size={16} />}
      onClick={() => setTheme(next)}
    />
  );
}
