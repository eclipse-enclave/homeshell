import { render, screen } from '@testing-library/react';

import { SessionCounts } from './session-counts';

describe('SessionCounts', () => {
  it('shows a number per kind in order, the words only in the name', () => {
    render(
      <SessionCounts
        counts={[
          { kind: 'waiting', count: 1 },
          { kind: 'running', count: 2 },
          { kind: 'ended', count: 5 },
        ]}
      />,
    );
    const counts = screen.getByRole('img', {
      name: 'Sessions: 1 waiting for input, 2 running, 5 ended',
    });
    // The numbers in order, and no words beside them.
    expect(counts.textContent).toBe('125');
  });

  it('renders nothing without sessions', () => {
    const { container } = render(<SessionCounts counts={[]} />);
    expect(container.firstChild).toBeNull();
  });
});
