import type { Session } from '@homeshell/model';
import { act, fireEvent, render, screen } from '@testing-library/react';
import { SessionList, type SessionListProps } from './session-list';

const live = (id: string): Session => ({
  id,
  title: `Live ${id}`,
  agent: 'Claude Code',
  state: 'running',
});

/** Ended `minutes` ago, or at an unknown time. */
const ended = (id: string, minutes?: number): Session => ({
  id,
  title: `Ended ${id}`,
  agent: 'Claude Code',
  state: 'stopped',
  ended: true,
  endedAt:
    minutes === undefined
      ? undefined
      : new Date(Date.now() - minutes * 60_000).toISOString(),
});

const fiveEnded = [1, 2, 3, 4, 5].map((n) => ended(`e${n}`, n));

/** A list of plain rows, `tabStopId`'s holding the tab stop when set. */
function renderList(
  sessions: Session[],
  props: Partial<SessionListProps> = {},
) {
  const tabIndexFor = (id: string) =>
    props.tabStopId === undefined ? undefined : id === props.tabStopId ? 0 : -1;
  render(
    <SessionList
      sessions={sessions}
      tabIndexFor={tabIndexFor}
      renderRow={(session) => (
        <li
          key={session.id}
          data-nav-id={session.id}
          tabIndex={tabIndexFor(session.id)}
        >
          {session.title}
        </li>
      )}
      {...props}
    />,
  );
}

const shownItems = () =>
  screen.getAllByRole('listitem').map((item) => item.textContent);

const row = (title: string) => screen.getByText(title);

describe('SessionList', () => {
  it('lists live sessions first, then the 3 newest ended, the rest folded', () => {
    renderList([
      ended('e5', 50),
      live('l1'),
      ended('e1', 10),
      ended('old'),
      ended('e2', 20),
      live('l2'),
      ended('e3', 30),
    ]);
    expect(shownItems()).toEqual([
      'Live l1',
      'Live l2',
      'Ended · 5',
      'Ended e1',
      'Ended e2',
      'Ended e3',
      '2 more ended sessions',
    ]);

    fireEvent.click(
      screen.getByRole('button', { name: '2 more ended sessions' }),
    );
    // An ended session of unknown time comes last.
    expect(shownItems().slice(3)).toEqual([
      'Ended e1',
      'Ended e2',
      'Ended e3',
      'Ended e5',
      'Ended old',
      'Show fewer ended sessions',
    ]);
  });

  it('folds nothing away with 3 ended sessions or fewer', () => {
    renderList(fiveEnded.slice(0, 3));
    expect(shownItems()).toEqual([
      'Ended · 3',
      'Ended e1',
      'Ended e2',
      'Ended e3',
    ]);
  });

  it('keeps the selected ended session in view while the rest are folded', () => {
    renderList(fiveEnded, { tabStopId: 'e5', selectedId: 'e5' });
    expect(shownItems().slice(1)).toEqual([
      'Ended e1',
      'Ended e2',
      'Ended e3',
      'Ended e5',
      '1 more ended session',
    ]);
  });

  it('opens on Enter onto the first row it held, so the arrows go on from there', () => {
    renderList(fiveEnded, { tabStopId: 'e1' });
    const fold = screen.getByRole('button', { name: '2 more ended sessions' });
    act(() => fold.focus());
    fireEvent.keyDown(fold, { key: 'Enter' });
    expect(document.activeElement).toBe(row('Ended e4'));
  });

  it('leaves focus on the fold when it is clicked', () => {
    renderList(fiveEnded, { tabStopId: 'e1' });
    const fold = screen.getByRole('button', { name: '2 more ended sessions' });
    act(() => fold.focus());
    fireEvent.click(fold);
    expect(row('Ended e4')).toBeTruthy();
    expect(document.activeElement).toBe(fold);
  });

  // As the controls of a row are: from the row with the tab stop only.
  it.each([
    ['e3', null],
    ['e1', '-1'],
  ])('with the tab stop on %s, takes tabindex %s', (tabStopId, tabIndex) => {
    renderList(fiveEnded, { tabStopId });
    expect(
      screen
        .getByRole('button', { name: /more ended/ })
        .getAttribute('tabindex'),
    ).toBe(tabIndex);
  });
});
