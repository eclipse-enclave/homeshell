import { fireEvent, render, screen } from '@testing-library/react';

import { ThemeProvider } from './use-theme';
import { ThemeToggle } from './theme-toggle';

describe('ThemeToggle', () => {
  beforeEach(() => {
    localStorage.clear();
    document.documentElement.removeAttribute('data-theme');
  });

  it('flips the document theme and persists it to hs-theme', () => {
    render(
      <ThemeProvider>
        <ThemeToggle />
      </ThemeProvider>
    );

    // Default resolves to dark in jsdom (no matchMedia), so the first click
    // switches to light and writes the browser-local preference.
    fireEvent.click(
      screen.getByRole('button', { name: 'Switch to light theme' }),
    );
    expect(document.documentElement.getAttribute('data-theme')).toBe('light');
    expect(localStorage.getItem('hs-theme')).toBe('light');

    // Clicking again flips back to dark.
    fireEvent.click(
      screen.getByRole('button', { name: 'Switch to dark theme' }),
    );
    expect(document.documentElement.getAttribute('data-theme')).toBeNull();
    expect(localStorage.getItem('hs-theme')).toBe('dark');
  });
});
