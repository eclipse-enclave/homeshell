import { render, screen } from '@testing-library/react';

import { SessionBadges } from './session-badges';

describe('SessionBadges', () => {
  it('gives each state one badge with its count, what wants the user first', () => {
    render(
      <SessionBadges states={['idle', 'running', 'waiting', 'running']} />,
    );
    const badges = screen.getByRole('img', {
      name: 'Sessions: 1 waiting for input, 2 running, 1 idle',
    });
    expect(
      Array.from(badges.children).map((badge) => [
        badge.getAttribute('data-state'),
        badge.textContent,
      ]),
    ).toEqual([
      ['waiting', '1'],
      ['running', '2'],
      ['idle', '1'],
    ]);
  });

  // The counts alone on screen; the states are named on hover.
  it('names the states on hover', () => {
    render(<SessionBadges states={['running', 'waiting', 'running']} />);
    expect(screen.getByRole('img').title).toBe(
      '1 waiting for input, 2 running',
    );
  });

  it('says 0 with no sessions', () => {
    render(<SessionBadges states={[]} />);
    const badges = screen.getByRole('img', { name: 'Sessions: No sessions' });
    expect(badges.textContent).toBe('0');
    expect(badges.title).toBe('No sessions');
  });
});
