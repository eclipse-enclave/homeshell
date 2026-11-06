import { fireEvent, render, screen } from '@testing-library/react';
import { vi } from 'vitest';

import { Contributes } from '../../testing/contributes';
import { CommandsProvider } from './commands';
import { PickOne } from './pick-one';

const items = [
  { id: 'w1', label: 'feature/login' },
  { id: 'w2', label: 'bugfix/crash' },
];

function renderPick() {
  const onPick = vi.fn();
  const onClose = vi.fn();
  render(
    <PickOne
      label="New session in"
      placeholder="Choose"
      items={items}
      onPick={onPick}
      onClose={onClose}
    />,
  );
  return { onPick, onClose, box: screen.getByRole('combobox') };
}

describe('PickOne', () => {
  it('lists every item until something is typed', () => {
    renderPick();
    expect(screen.getAllByRole('option').map((o) => o.textContent)).toEqual([
      'feature/login',
      'bugfix/crash',
    ]);
  });

  it('picks the best match for what is typed, and closes', () => {
    const { onPick, onClose, box } = renderPick();
    fireEvent.change(box, { target: { value: 'crash' } });
    fireEvent.keyDown(box, { key: 'Enter' });
    expect(onPick).toHaveBeenCalledWith(items[1]);
    expect(onClose).toHaveBeenCalled();
  });

  it('picks nothing on Escape', () => {
    const { onPick, onClose, box } = renderPick();
    fireEvent.keyDown(box, { key: 'Escape' });
    expect(onClose).toHaveBeenCalled();
    expect(onPick).not.toHaveBeenCalled();
  });

  // It is modal: a shortcut would open the palette over it.
  it("keeps the app's shortcuts from running inside it", () => {
    const run = vi.fn();
    render(
      <CommandsProvider>
        <Contributes commands={[{ id: 'showCommands', label: 'Show', run }]} />
        <PickOne
          label="New session in"
          placeholder="Choose"
          items={items}
          onPick={vi.fn()}
          onClose={vi.fn()}
        />
      </CommandsProvider>,
    );
    fireEvent.keyDown(screen.getByRole('combobox'), { key: 'F1' });
    expect(run).not.toHaveBeenCalled();
  });
});
