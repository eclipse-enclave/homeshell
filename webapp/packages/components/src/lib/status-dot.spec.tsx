import { render, screen } from '@testing-library/react';

import { StatusDot } from './status-dot';

describe('StatusDot', () => {
  it('names its state, and shows the words only when asked', () => {
    const { rerender } = render(<StatusDot state="waiting" />);
    expect(screen.getByRole('img', { name: 'Waiting for input' })).toBeTruthy();
    expect(screen.queryByText('Waiting for input')).toBeNull();

    rerender(<StatusDot state="ended" showLabel />);
    expect(screen.getByRole('img', { name: 'Ended' })).toBeTruthy();
    expect(screen.getByText('Ended')).toBeTruthy();
  });

  it('tells states apart by shape, not colour alone', () => {
    const states = ['running', 'waiting', 'error', 'ended', 'unknown'] as const;
    const shapes = states.map((state) => {
      const { unmount } = render(<StatusDot state={state} />);
      const shape = screen.getByRole('img').getAttribute('data-shape');
      unmount();
      return shape;
    });
    expect(new Set(shapes).size).toBe(states.length);
  });
});
