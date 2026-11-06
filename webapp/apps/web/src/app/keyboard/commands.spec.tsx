import { fireEvent, render, screen } from '@testing-library/react';
import { vi } from 'vitest';

import { Contributes, registryProbe } from '../../testing/contributes';
import { CommandsProvider, createCommandRegistry } from './commands';

describe('createCommandRegistry', () => {
  it('lists and runs what is added until it is taken away', () => {
    const registry = createCommandRegistry();
    const run = vi.fn();
    const remove = registry.add(() => [
      { id: 'quickOpen', label: 'Go to', run },
    ]);

    expect(registry.list().map((command) => command.id)).toEqual(['quickOpen']);
    registry.run('quickOpen');
    expect(run).toHaveBeenCalledTimes(1);

    remove();
    registry.run('quickOpen');
    expect(registry.list()).toEqual([]);
    expect(run).toHaveBeenCalledTimes(1);
  });

  it('tells its listeners before a keybinding runs a command', () => {
    const registry = createCommandRegistry();
    const calls: string[] = [];
    registry.add(() => [
      { id: 'quickOpen', label: 'Go to', run: () => calls.push('run') },
    ]);
    const stop = registry.onKeybinding(() => calls.push('listener'));

    registry.runKeybinding('unknown');
    registry.run('quickOpen');
    registry.runKeybinding('quickOpen');
    stop();
    registry.runKeybinding('quickOpen');
    expect(calls).toEqual(['run', 'listener', 'run', 'run']);
  });
});

describe('CommandsProvider', () => {
  it('runs a mounted command from its keybinding', () => {
    const run = vi.fn();
    render(
      <CommandsProvider>
        <Contributes commands={[{ id: 'quickOpen', label: 'Go to', run }]} />
      </CommandsProvider>,
    );
    const event = fireEvent.keyDown(window, { key: 'p', ctrlKey: true });
    expect(run).toHaveBeenCalledTimes(1);
    // The browser's own Ctrl+P (print) must not open as well.
    expect(event).toBe(false);
  });

  it('forgets a command once its component unmounts', () => {
    const run = vi.fn();
    const { rerender } = render(
      <CommandsProvider>
        <Contributes commands={[{ id: 'quickOpen', label: 'Go to', run }]} />
      </CommandsProvider>,
    );
    rerender(<CommandsProvider>{null}</CommandsProvider>);
    fireEvent.keyDown(window, { key: 'p', ctrlKey: true });
    expect(run).not.toHaveBeenCalled();
  });

  it('asks a function for its commands each time they are listed', () => {
    let label = 'First';
    const { Probe, registry } = registryProbe();
    render(
      <CommandsProvider>
        <Contributes commands={() => [{ id: 'lazy', label, run: vi.fn() }]} />
        <Probe />
      </CommandsProvider>,
    );
    label = 'Second';
    expect(
      registry()
        .list()
        .map((command) => command.label),
    ).toEqual(['Second']);
  });

  it('runs the commands of the latest render', () => {
    const first = vi.fn();
    const second = vi.fn();
    const { rerender } = render(
      <CommandsProvider>
        <Contributes
          commands={[{ id: 'quickOpen', label: 'Go to', run: first }]}
        />
      </CommandsProvider>,
    );
    rerender(
      <CommandsProvider>
        <Contributes
          commands={[{ id: 'quickOpen', label: 'Go to', run: second }]}
        />
      </CommandsProvider>,
    );
    fireEvent.keyDown(window, { key: 'p', ctrlKey: true });
    expect(first).not.toHaveBeenCalled();
    expect(second).toHaveBeenCalledTimes(1);
  });

  it('keeps a key it ran from the focused element', () => {
    const onKeyDown = vi.fn();
    render(
      <CommandsProvider>
        <Contributes
          commands={[{ id: 'quickOpen', label: 'Go to', run: vi.fn() }]}
        />
        <button type="button" onKeyDown={onKeyDown}>
          Focused
        </button>
      </CommandsProvider>,
    );
    fireEvent.keyDown(screen.getByRole('button', { name: 'Focused' }), {
      key: 'p',
      ctrlKey: true,
    });
    expect(onKeyDown).not.toHaveBeenCalled();
  });

  it.each([
    ['ignores shortcuts inside another modal dialog', {}, 0],
    [
      'still takes them inside a modal that passes them',
      { 'data-pass-shortcuts': '' },
      1,
    ],
  ])('%s', (_case, marker, calls) => {
    const run = vi.fn();
    render(
      <CommandsProvider>
        <Contributes commands={[{ id: 'quickOpen', label: 'Go to', run }]} />
        <div role="dialog" aria-modal="true" aria-label="Modal" {...marker}>
          <button type="button">Inside</button>
        </div>
      </CommandsProvider>,
    );
    fireEvent.keyDown(screen.getByRole('button', { name: 'Inside' }), {
      key: 'p',
      ctrlKey: true,
    });
    expect(run).toHaveBeenCalledTimes(calls);
  });

  describe('with a chord begun', () => {
    function renderChord() {
      const run = vi.fn();
      render(
        <CommandsProvider>
          <Contributes
            commands={[{ id: 'showShortcuts', label: 'Shortcuts', run }]}
          />
          <button type="button">Elsewhere</button>
          <div role="dialog" aria-modal="true" aria-label="Modal">
            <button type="button">Inside</button>
          </div>
        </CommandsProvider>,
      );
      fireEvent.keyDown(window, { key: 'k', ctrlKey: true });
      return run;
    }

    it('runs it on the second stroke', () => {
      const run = renderChord();
      fireEvent.keyDown(window, { key: 's', ctrlKey: true });
      expect(run).toHaveBeenCalledTimes(1);
    });

    it.each([
      ['the window loses focus', () => fireEvent.blur(window)],
      [
        'focus moves',
        () => screen.getByRole('button', { name: 'Elsewhere' }).focus(),
      ],
      [
        'a key goes to another modal dialog',
        () =>
          fireEvent.keyDown(screen.getByRole('button', { name: 'Inside' }), {
            key: 'x',
          }),
      ],
    ])('drops it when %s', (_case, interrupt) => {
      const run = renderChord();
      interrupt();
      fireEvent.keyDown(window, { key: 's', ctrlKey: true });
      expect(run).not.toHaveBeenCalled();
    });
  });

  it('leaves unbound keys to the page', () => {
    render(<CommandsProvider>{null}</CommandsProvider>);
    expect(fireEvent.keyDown(window, { key: 'q', ctrlKey: true })).toBe(true);
  });
});
