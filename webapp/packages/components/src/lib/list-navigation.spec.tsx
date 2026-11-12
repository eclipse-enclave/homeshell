import type { Workstream } from '@homeshell/model';
import { act, fireEvent, render, screen } from '@testing-library/react';
import { useRef } from 'react';

import {
  controlTabIndexFor,
  describeListKeys,
  handleListKeyDown,
  listItemKeyShortcuts,
  tabStopItem,
  useListTabStop,
} from './list-navigation';

/** Two cards, the first with two rows: the shape ProjectView renders. */
function renderList() {
  render(
    <div onKeyDown={handleListKeyDown}>
      <section aria-label="one" data-nav-id="one" tabIndex={-1}>
        <button type="button">Open</button>
        <ul>
          <li data-nav-id="one-a" tabIndex={-1}>
            a
          </li>
          <li data-nav-id="one-b" tabIndex={-1}>
            b
          </li>
        </ul>
      </section>
      <section aria-label="two" data-nav-id="two" tabIndex={-1} />
    </div>,
  );
}

const item = (id: string) =>
  document.querySelector<HTMLElement>(`[data-nav-id="${id}"]`) as HTMLElement;

describe('handleListKeyDown', () => {
  it.each([
    ['one', 'ArrowDown', 'one-a'],
    ['one-b', 'ArrowDown', 'two'],
    ['two', 'ArrowUp', 'one-b'],
    ['one-a', 'ArrowUp', 'one'],
    ['one-b', 'Home', 'one'],
    ['one', 'End', 'two'],
    ['one-b', 'ArrowLeft', 'one'],
    ['one', 'ArrowRight', 'one-a'],
  ])('moves from %s on %s to %s', (from, key, to) => {
    renderList();
    item(from).focus();
    fireEvent.keyDown(item(from), { key });
    expect(document.activeElement).toBe(item(to));
  });

  it.each([
    ['one', 'ArrowUp'],
    ['two', 'ArrowDown'],
    ['one', 'ArrowLeft'],
    ['two', 'ArrowRight'],
  ])('stays on %s at the edge on %s', (from, key) => {
    renderList();
    item(from).focus();
    expect(fireEvent.keyDown(item(from), { key })).toBe(true);
    expect(document.activeElement).toBe(item(from));
  });

  it('leaves keys pressed on a control inside an item alone', () => {
    renderList();
    const button = screen.getByRole('button', { name: 'Open' });
    button.focus();
    fireEvent.keyDown(button, { key: 'ArrowDown' });
    expect(document.activeElement).toBe(button);
  });

  it('leaves modified keys alone', () => {
    renderList();
    item('one').focus();
    fireEvent.keyDown(item('one'), { key: 'ArrowDown', shiftKey: true });
    expect(document.activeElement).toBe(item('one'));
  });

  it('leaves a key the item already handled alone', () => {
    renderList();
    const row = item('one-a');
    row.addEventListener('keydown', (event) => event.preventDefault());
    row.focus();
    fireEvent.keyDown(row, { key: 'ArrowLeft' });
    expect(document.activeElement).toBe(row);
  });
});

describe('tabStopItem', () => {
  it('finds the item Tab lands on', () => {
    renderList();
    item('one-b').tabIndex = 0;
    expect(tabStopItem(document)).toBe(item('one-b'));
  });
});

describe('useListTabStop', () => {
  const workstream = (id: string) =>
    ({ id, name: id, sessions: [] }) as unknown as Workstream;

  /** `inside`: the list stays on the page, New in it, once it is empty. */
  function List({
    workstreams,
    resetKey = 'p1',
    inside = false,
  }: {
    workstreams: Workstream[];
    resetKey?: string;
    inside?: boolean;
  }) {
    const empty = useRef<HTMLButtonElement>(null);
    const list = useListTabStop(workstreams, {
      resetKey,
      emptyFocus: () => empty.current,
    });
    const newButton = (
      <button ref={empty} type="button">
        New
      </button>
    );
    return inside || workstreams.length > 0 ? (
      <div {...list.listProps}>
        {inside && newButton}
        {workstreams.map(({ id }) => (
          <section
            key={id}
            aria-label={id}
            data-nav-id={id}
            tabIndex={id === list.tabStopId ? 0 : -1}
            aria-current={id === list.selectedId || undefined}
          />
        ))}
      </div>
    ) : (
      newButton
    );
  }

  const two = [workstream('one'), workstream('two')];
  const current = () =>
    Array.from(document.querySelectorAll('[aria-current="true"]')).map(
      (element) => element.getAttribute('aria-label'),
    );

  it('puts the tab stop on the first item, and selects nothing yet', () => {
    render(<List workstreams={two} />);
    expect(tabStopItem(document)).toBe(item('one'));
    expect(current()).toEqual([]);
  });

  it('selects the item focus went to, until it starts over', () => {
    const { rerender } = render(<List workstreams={two} />);
    act(() => item('two').focus());
    expect(tabStopItem(document)).toBe(item('two'));
    expect(current()).toEqual(['two']);

    rerender(<List workstreams={two} resetKey="p2" />);
    expect(current()).toEqual([]);
  });

  it('focuses what is left when the focused item goes', () => {
    const { rerender } = render(<List workstreams={two} />);
    act(() => item('two').focus());
    rerender(<List workstreams={[two[0]]} />);
    expect(document.activeElement).toBe(item('one'));

    rerender(<List workstreams={[]} />);
    expect(document.activeElement).toBe(
      screen.getByRole('button', { name: 'New' }),
    );
  });

  it('focuses New when the last item goes from a list that stays', () => {
    const { rerender } = render(<List workstreams={[two[0]]} inside />);
    act(() => item('one').focus());
    rerender(<List workstreams={[]} inside />);
    expect(document.activeElement).toBe(
      screen.getByRole('button', { name: 'New' }),
    );
  });
});

describe('the list keys', () => {
  it('names the keys a focused item answers, on each platform', () => {
    expect(listItemKeyShortcuts('card', false)).toBe(
      'Enter F2 Delete Shift+F10',
    );
    expect(listItemKeyShortcuts('session', true)).toBe(
      'ArrowRight ArrowLeft Enter F2 Delete Meta+Backspace Shift+F10',
    );
  });

  it('describes every key of the list', () => {
    expect(describeListKeys(false)).toContain(
      'Delete: Delete the focused workstream or session.',
    );
    expect(describeListKeys(true)).toContain(
      'Delete or Meta+Backspace: Delete the focused workstream or session.',
    );
  });

  it.each([
    [0, undefined],
    [undefined, undefined],
    [-1, -1],
  ])('gives the controls of an item at tabIndex %s tabIndex %s', (item, controls) => {
    expect(controlTabIndexFor(item)).toBe(controls);
  });
});
