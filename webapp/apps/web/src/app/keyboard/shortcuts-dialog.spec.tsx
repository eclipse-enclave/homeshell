import { fireEvent, render, screen, within } from '@testing-library/react';
import { vi } from 'vitest';

import { ShortcutsDialog } from './shortcuts-dialog';

function renderDialog(onClose = vi.fn()) {
  render(<ShortcutsDialog onClose={onClose} />);
  return onClose;
}

const row = (table: string, label: string) =>
  within(screen.getByRole('table', { name: table }))
    .getByRole('rowheader', { name: label })
    .closest('tr') as HTMLElement;

describe('ShortcutsDialog', () => {
  it('lists each bound command once, with all of its keys', () => {
    renderDialog();
    expect(
      screen.getByRole('dialog', { name: 'Keyboard shortcuts' }),
    ).toBeTruthy();
    expect(row('Anywhere', 'Show all commands').textContent).toBe(
      'Show all commandsCtrl+Shift+PF1',
    );
  });

  // Not by the palette's label, which names the workstream it would act on
  // and is absent while there is none.
  it('lists a command by its fixed label, whether it is on offer or not', () => {
    renderDialog();
    expect(
      row('Anywhere', 'New session in the selected workstream').textContent,
    ).toBe('New session in the selected workstreamCtrl+Shift+`');
  });

  it('leaves out commands that have no keys', () => {
    renderDialog();
    expect(screen.queryByRole('rowheader', { name: 'Toggle logs' })).toBeNull();
  });

  it.each([
    ['Close a dialog, palette or menu', 'Esc'],
    ['In Go to, list the commands instead', '>'],
  ])('lists "%s", which runs no command, as %s', (label, keys) => {
    renderDialog();
    expect(row('Anywhere', label).textContent).toBe(label + keys);
  });

  it.each([
    ['Move between workstreams and sessions', '↑↓'],
    ['Go to the first or last item', 'HomeEnd'],
    ['Expand, or go to the first session', '→'],
    ['Collapse, or go to the workstream', '←'],
    ['Open the focused workstream or session', 'Enter'],
    ['Rename the focused workstream or session', 'F2'],
    ['Delete the focused workstream or session', 'Delete'],
    ['Open the actions menu', 'Shift+F10'],
  ])('lists "%s" as %s', (label, keys) => {
    renderDialog();
    expect(row('In the workstream list', label).textContent).toBe(label + keys);
  });

  it.each([
    ['Open the focused folder', 'Enter→'],
    ['Go to the parent folder', 'BackspaceAlt+↑'],
    ['Select the folder shown', 'Ctrl+Enter'],
  ])('lists "%s" in the folder picker as %s', (label, keys) => {
    renderDialog();
    expect(row('In the folder picker', label).textContent).toBe(label + keys);
  });

  it('opens on its Close button', () => {
    renderDialog();
    expect(document.activeElement).toBe(
      screen.getByRole('button', { name: 'Close' }),
    );
  });

  it('closes from its Close button', () => {
    const onClose = renderDialog();
    fireEvent.click(screen.getByRole('button', { name: 'Close' }));
    expect(onClose).toHaveBeenCalled();
  });

  it('gives focus to the list tab stop when what opened it has gone', () => {
    const { rerender } = render(<button type="button">Menu item</button>);
    screen.getByRole('button', { name: 'Menu item' }).focus();
    const item = <div data-nav-id="ws-1" tabIndex={0} aria-label="Card" />;
    rerender(
      <>
        {item}
        <ShortcutsDialog onClose={vi.fn()} />
      </>,
    );
    rerender(item);
    expect(document.activeElement).toBe(screen.getByLabelText('Card'));
  });
});
