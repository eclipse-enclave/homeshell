import { act, fireEvent, render, screen, within } from '@testing-library/react';
import { createRef } from 'react';

import { SessionRow, type SessionRowHandle } from './session-row';
import { WorkstreamCard, type WorkstreamCardHandle } from './workstream-card';

/**
 * The actions menu a focused card or row opens on Shift+F10 or the menu key,
 * listing what its own buttons and keys offer.
 */

const running = {
  id: 's1',
  title: 'Implement',
  agent: 'Claude Code',
  state: 'running' as const,
};

function renderRow(disabledReason?: string) {
  const handlers = {
    onOpen: vi.fn(),
    onStop: vi.fn(),
    onRemove: vi.fn(),
    onRename: vi.fn(),
  };
  const handle = createRef<SessionRowHandle>();
  render(
    <ul>
      <SessionRow
        ref={handle}
        session={running}
        tabIndex={0}
        disabledReason={disabledReason}
        {...handlers}
      />
    </ul>,
  );
  const row = screen.getByRole('listitem');
  act(() => row.focus());
  return { row, handle, ...handlers };
}

/** Each item's label, without the key shown after it. */
const labels = (menu: HTMLElement) =>
  within(menu)
    .getAllByRole('menuitem')
    .map((item) => item.firstChild?.textContent);

describe('the actions menu', () => {
  it.each([
    [
      'Shift+F10',
      (row: HTMLElement) =>
        fireEvent.keyDown(row, { key: 'F10', shiftKey: true }),
    ],
    // Which the menu key raises too, after its keyup on Windows.
    ['a contextmenu event', (row: HTMLElement) => fireEvent.contextMenu(row)],
  ])('opens on %s with what the row offers', (_keys, open) => {
    const { row } = renderRow();
    open(row);

    const menu = screen.getByRole('menu', {
      name: 'Actions for session Implement',
    });
    expect(labels(menu)).toEqual([
      'Open',
      'Stop',
      'Rename',
      'Show details',
      'Delete',
    ]);
    expect(document.activeElement?.textContent).toBe('Open');
    const separator = within(menu).getByRole('separator');
    expect(
      separator.nextElementSibling?.querySelector('[role="menuitem"]')
        ?.firstChild?.textContent,
    ).toBe('Delete');
  });

  it('shows the keys of Rename and Delete beside them, as VS Code does', () => {
    const { row } = renderRow();
    fireEvent.contextMenu(row);

    const rename = screen.getByRole('menuitem', { name: 'Rename' });
    expect(rename.textContent).toBe('RenameF2');
    expect(rename.getAttribute('aria-keyshortcuts')).toBe('F2');
    expect(
      screen.getByRole('menuitem', { name: 'Delete' }).textContent,
    ).toMatch(/^Delete.+/);
    expect(
      screen.getByRole('menuitem', { name: 'Open' }).textContent,
    ).toBe('Open');
  });

  it("shows F2 beside a card's Rename", () => {
    render(
      <WorkstreamCard
        projectPath="/p"
        workstream={{
          id: 'w1',
          name: 'feature/x',
          provider: 'fake',
          strategyId: 'in-place',
          strategy: 'in place',
          state: 'running',
          runtimeState: 'running',
          effectiveState: 'running',
          sessions: [],
        }}
        tabStopId="w1"
        onRename={vi.fn()}
      />,
    );
    fireEvent.contextMenu(
      screen.getByRole('region', { name: 'Workstream feature/x' }),
    );
    expect(
      screen.getByRole('menuitem', { name: 'Rename' }).textContent,
    ).toBe('RenameF2');
  });

  it('opens on a right-click inside the row, but not in its text boxes', () => {
    const { row } = renderRow();
    const title = within(row).getByTestId('session-title');
    expect(fireEvent.contextMenu(title)).toBe(false);
    expect(screen.getByRole('menu')).toBeTruthy();
    fireEvent.keyDown(document.activeElement as HTMLElement, {
      key: 'Escape',
    });

    fireEvent.keyDown(row, { key: 'F2' });
    fireEvent.contextMenu(
      screen.getByRole('textbox', { name: 'Session name' }),
    );
    expect(screen.queryByRole('menu')).toBeNull();
  });

  it('leaves a right-click in a dialog the row holds alone', () => {
    const { row } = renderRow();
    fireEvent.keyDown(row, { key: 'Delete' });
    const dialog = screen.getByRole('dialog');
    fireEvent.contextMenu(within(dialog).getByText(/delete the session/));
    expect(screen.queryByRole('menu')).toBeNull();
  });

  it('runs the chosen action, with focus back on the row', () => {
    const { row, onStop } = renderRow();
    fireEvent.keyDown(row, { key: 'F10', shiftKey: true });
    fireEvent.click(screen.getByRole('menuitem', { name: 'Stop' }));

    expect(onStop).toHaveBeenCalled();
    expect(screen.queryByRole('menu')).toBeNull();
    expect(document.activeElement).toBe(row);
  });

  it('gives focus back to the row on Escape', () => {
    const { row } = renderRow();
    fireEvent.keyDown(row, { key: 'F10', shiftKey: true });
    fireEvent.keyDown(document.activeElement as HTMLElement, {
      key: 'Escape',
    });

    expect(screen.queryByRole('menu')).toBeNull();
    expect(document.activeElement).toBe(row);
  });

  it("keeps the row's own keys out of the menu", () => {
    const { row, onRename } = renderRow();
    fireEvent.keyDown(row, { key: 'F10', shiftKey: true });
    fireEvent.keyDown(document.activeElement as HTMLElement, { key: 'F2' });

    expect(screen.getByRole('menu')).toBeTruthy();
    expect(screen.queryByRole('textbox', { name: 'Session name' })).toBeNull();
    expect(onRename).not.toHaveBeenCalled();
  });

  // Disabled buttons are shown; the menu leaves out what they cannot do.
  it('lists only what is on offer while the daemon is unreachable', () => {
    const { row } = renderRow('Offline');
    fireEvent.keyDown(row, { key: 'F10', shiftKey: true });
    expect(labels(screen.getByRole('menu'))).toEqual(['Show details']);
  });

  it('hands the same actions out through the row handle', () => {
    const { handle } = renderRow();
    expect(handle.current?.actions().map((action) => action.label)).toEqual([
      'Open',
      'Stop',
      'Rename',
      'Show details',
      'Delete',
    ]);
  });

  it('lists what a card offers, and its rows through the card', () => {
    const handle = createRef<WorkstreamCardHandle>();
    render(
      <WorkstreamCard
        projectPath="/p"
        ref={handle}
        workstream={{
          id: 'w1',
          name: 'feature/x',
          provider: 'fake',
          strategyId: 'in-place',
          strategy: 'in place',
          state: 'running',
          runtimeState: 'running',
          effectiveState: 'running',
          openModes: [{ id: 'terminal', label: 'Terminal', default: true }],
          actions: [{ id: 'note', label: 'Note' }],
          sessions: [running],
        }}
        tabStopId="w1"
        onOpen={vi.fn()}
        onOpenWith={vi.fn()}
        onRename={vi.fn()}
        onDelete={vi.fn()}
        onAction={vi.fn()}
        onStopSession={vi.fn()}
      />,
    );
    const card = screen.getByRole('region', { name: 'Workstream feature/x' });
    act(() => card.focus());
    fireEvent.contextMenu(card);

    expect(
      labels(
        screen.getByRole('menu', { name: 'Actions for workstream feature/x' }),
      ),
    ).toEqual(['Open', 'Open with Terminal', 'Rename', 'Note', 'Delete']);
    expect(
      handle.current
        ?.actions('s1')
        .filter((action) => !action.disabled)
        .map((action) => action.label),
    ).toEqual(['Stop', 'Show details']);
    expect(handle.current?.actions('elsewhere')).toEqual([]);
  });
});
