import { act, render } from '@testing-library/react';
import { createElement } from 'react';
import { renderHook } from '@testing-library/react';
import { vi } from 'vitest';

import {
  resolveTheme,
  ThemeProvider,
  toggledTheme,
  useApplyTheme,
} from './use-theme';

describe('toggledTheme', () => {
  it('switches to the palette not showing', () => {
    expect(toggledTheme('light')).toBe('dark');
    expect(toggledTheme('dark')).toBe('light');
    // Without matchMedia, system shows dark.
    expect(toggledTheme('system')).toBe('light');
  });
});

describe('resolveTheme', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('returns explicit choices unchanged', () => {
    expect(resolveTheme('light')).toBe('light');
    expect(resolveTheme('dark')).toBe('dark');
  });

  it('resolves system from the OS preference', () => {
    vi.stubGlobal(
      'matchMedia',
      (query: string) =>
        ({
          matches: query === '(prefers-color-scheme: light)',
          media: query,
          addEventListener: () => undefined,
          removeEventListener: () => undefined,
        }) as unknown as MediaQueryList,
    );
    expect(resolveTheme('system')).toBe('light');
  });

  it('falls back to dark when the OS preference is unavailable', () => {
    expect(resolveTheme('system')).toBe('dark');
  });
});

describe('useApplyTheme', () => {
  beforeEach(() => {
    localStorage.clear();
    document.documentElement.removeAttribute('data-theme');
  });
  afterEach(() => vi.unstubAllGlobals());

  it('applies light and mirrors the choice to hs-theme', () => {
    renderHook(() => useApplyTheme('light'));
    expect(document.documentElement.getAttribute('data-theme')).toBe('light');
    expect(localStorage.getItem('hs-theme')).toBe('light');
  });

  it('applies dark by removing the attribute', () => {
    document.documentElement.setAttribute('data-theme', 'light');
    renderHook(() => useApplyTheme('dark'));
    expect(document.documentElement.getAttribute('data-theme')).toBeNull();
    expect(localStorage.getItem('hs-theme')).toBe('dark');
  });

  it('resolves system via matchMedia and stores the raw choice', () => {
    vi.stubGlobal(
      'matchMedia',
      (query: string) =>
        ({
          matches: query === '(prefers-color-scheme: light)',
          media: query,
          addEventListener: () => undefined,
          removeEventListener: () => undefined,
        }) as unknown as MediaQueryList,
    );
    renderHook(() => useApplyTheme('system'));
    expect(document.documentElement.getAttribute('data-theme')).toBe('light');
    expect(localStorage.getItem('hs-theme')).toBe('system');
  });
});

describe('ThemeProvider', () => {
  beforeEach(() => {
    localStorage.clear();
    document.documentElement.removeAttribute('data-theme');
  });

  it('follows a theme change made in another window (storage event)', () => {
    render(createElement(ThemeProvider, null, createElement('div')));
    // A theme change in another tab writes hs-theme and fires `storage` here.
    act(() => {
      window.dispatchEvent(
        new StorageEvent('storage', { key: 'hs-theme', newValue: 'light' }),
      );
    });
    expect(document.documentElement.getAttribute('data-theme')).toBe('light');
  });
});
