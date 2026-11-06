import {
  createContext,
  createElement,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from 'react';

export type Theme = 'system' | 'light' | 'dark';

const STORAGE_KEY = 'hs-theme';

/** Resolves a theme choice to the concrete palette, consulting the OS for `system`. */
export function resolveTheme(theme: Theme): 'light' | 'dark' {
  if (theme === 'light' || theme === 'dark') return theme;
  return typeof window.matchMedia === 'function' &&
    window.matchMedia('(prefers-color-scheme: light)').matches
    ? 'light'
    : 'dark';
}

/** The palette a one-step toggle switches to from what `theme` shows now. */
export function toggledTheme(theme: Theme): 'light' | 'dark' {
  return resolveTheme(theme) === 'dark' ? 'light' : 'dark';
}

function apply(theme: Theme) {
  const root = document.documentElement;
  if (resolveTheme(theme) === 'light') {
    root.setAttribute('data-theme', 'light');
  } else {
    root.removeAttribute('data-theme');
  }
  // The theme is a per-browser UI preference: localStorage is its store, read
  // by the pre-paint script in index.html so the first paint is themed.
  localStorage.setItem(STORAGE_KEY, theme);
}

/** Reads the stored theme, defaulting to `system`. */
function loadTheme(): Theme {
  const raw = localStorage.getItem(STORAGE_KEY);
  return raw === 'light' || raw === 'dark' || raw === 'system' ? raw : 'system';
}

/**
 * Applies the chosen theme to the document and keeps the `hs-theme` store in
 * sync. When the choice is `system`, it also follows OS preference changes.
 */
export function useApplyTheme(theme: Theme) {
  useEffect(() => {
    apply(theme);
    if (theme !== 'system' || typeof window.matchMedia !== 'function') return;
    const media = window.matchMedia('(prefers-color-scheme: light)');
    const onChange = () => apply('system');
    media.addEventListener('change', onChange);
    return () => media.removeEventListener('change', onChange);
  }, [theme]);
}

interface ThemeContextValue {
  theme: Theme;
  setTheme: (theme: Theme) => void;
}

const ThemeContext = createContext<ThemeContextValue | null>(null);

/**
 * Provides the browser-local theme preference and applies it to the document.
 * Theme is deliberately not part of the daemon-persisted settings: it is a
 * per-device UI preference, so it lives in localStorage.
 */
export function ThemeProvider({ children }: { children: ReactNode }) {
  const [theme, setTheme] = useState<Theme>(loadTheme);
  useApplyTheme(theme);

  // Follow theme changes made in other windows of this browser (e.g. changing
  // the theme in the main window while a log window is open). localStorage
  // writes fire `storage` in every other same-origin tab.
  useEffect(() => {
    const onStorage = (e: StorageEvent) => {
      if (
        e.key === STORAGE_KEY &&
        (e.newValue === 'light' || e.newValue === 'dark' || e.newValue === 'system')
      ) {
        setTheme(e.newValue);
      }
    };
    window.addEventListener('storage', onStorage);
    return () => window.removeEventListener('storage', onStorage);
  }, []);

  const value = useMemo(() => ({ theme, setTheme }), [theme]);
  return createElement(ThemeContext.Provider, { value }, children);
}

export function useTheme(): ThemeContextValue {
  const ctx = useContext(ThemeContext);
  if (!ctx) {
    throw new Error('useTheme must be used within a ThemeProvider');
  }
  return ctx;
}
