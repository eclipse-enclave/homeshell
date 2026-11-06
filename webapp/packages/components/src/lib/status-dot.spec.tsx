import { EFFECTIVE_STATES, effectiveStateLabel, SESSION_STATES } from '@homeshell/model';
import { render, screen } from '@testing-library/react';

import { StatusDot } from './status-dot';

describe('StatusDot', () => {
  it.each(SESSION_STATES)('exposes an accessible label for session state "%s"', (state) => {
    render(<StatusDot state={state} />);
    expect(screen.getByRole('img', { name: effectiveStateLabel(state) })).toBeTruthy();
  });

  it.each(EFFECTIVE_STATES)('exposes an accessible label for effective state "%s"', (state) => {
    render(<StatusDot state={state} />);
    expect(screen.getByRole('img', { name: effectiveStateLabel(state) })).toBeTruthy();
  });

  it('renders the label text only when requested', () => {
    const { rerender } = render(<StatusDot state="waiting" />);
    expect(screen.queryByText('Waiting for input')).toBeNull();

    rerender(<StatusDot state="waiting" showLabel />);
    expect(screen.getByText('Waiting for input')).toBeTruthy();
  });

  it('renders runtime state labels when requested', () => {
    render(<StatusDot state="stopped" showLabel />);
    expect(screen.getByText('Stopped')).toBeTruthy();
  });

  it('renders error state label', () => {
    render(<StatusDot state="error" showLabel />);
    expect(screen.getByText('Error')).toBeTruthy();
  });
});
