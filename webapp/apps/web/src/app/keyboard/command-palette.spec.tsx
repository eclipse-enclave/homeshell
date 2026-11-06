import type { Project } from '@homeshell/model';
import { fireEvent, render, screen } from '@testing-library/react';
import { vi } from 'vitest';

import { Contributes } from '../../testing/contributes';
import { CommandPalette } from './command-palette';
import { type Command, CommandsProvider } from './commands';

const projects: Project[] = [
  {
    id: 'p1',
    name: 'alpha',
    path: '/tmp/alpha',
    gitBacked: true,
    state: 'idle',
    runtimeState: 'running',
    effectiveState: 'idle',
    workstreams: [
      {
        id: 'w1',
        name: 'feature/x',
        provider: 'Fake',
        strategyId: 'in-place',
        strategy: 'in place',
        state: 'idle',
        runtimeState: 'running',
        effectiveState: 'idle',
        sessions: [
          { id: 's1', title: 'Implement', agent: 'Claude Code', state: 'idle' },
        ],
      },
    ],
  },
];

function renderPalette(
  initialQuery: string,
  commands: Command[] = [],
  shown: Project[] = projects,
) {
  const onGoTo = vi.fn();
  const onClose = vi.fn();
  // The palette reads the registry when it mounts, so the commands go in first.
  const { rerender } = render(
    <CommandsProvider>
      <Contributes commands={commands} />
    </CommandsProvider>,
  );
  rerender(
    <CommandsProvider>
      <Contributes commands={commands} />
      <CommandPalette
        initialQuery={initialQuery}
        projects={shown}
        onGoTo={onGoTo}
        onClose={onClose}
      />
    </CommandsProvider>,
  );
  return { onGoTo, onClose };
}

const input = () => screen.getByRole('combobox');
const options = () =>
  screen.getAllByRole('option').map((option) => option.textContent);

describe('CommandPalette', () => {
  const run = vi.fn();
  const commands: Command[] = [
    { id: 'toggleLogs', label: 'Toggle logs', run: vi.fn() },
    { id: 'openSettings', label: 'Open settings', run },
  ];

  it('lists the commands alphabetically, with their shortcuts, after a >', () => {
    renderPalette('>', commands);
    expect(
      screen.getByRole('dialog', { name: 'Command palette' }),
    ).toBeTruthy();
    expect(options()).toEqual(['Open settingsCtrl+,', 'Toggle logs']);
  });

  it('filters the commands fuzzily', () => {
    renderPalette('>', commands);
    fireEvent.change(input(), { target: { value: '>tl' } });
    expect(options()).toEqual(['Toggle logs']);
  });

  it('closes, then runs the command picked', () => {
    const { onClose } = renderPalette('>', commands);
    fireEvent.keyDown(input(), { key: 'Enter' });
    expect(onClose).toHaveBeenCalled();
    expect(run).toHaveBeenCalled();
  });

  // Its own keys switch it between going somewhere and commands.
  it("lets the app's shortcuts through", () => {
    const quickOpen = vi.fn();
    renderPalette('>', [{ id: 'quickOpen', label: 'Go to', run: quickOpen }]);
    fireEvent.keyDown(input(), { key: 'p', ctrlKey: true });
    expect(quickOpen).toHaveBeenCalled();
  });

  it('lists projects, workstreams and sessions without the >', () => {
    renderPalette('', commands);
    expect(screen.getByRole('dialog', { name: 'Go to' })).toBeTruthy();
    expect(options()).toEqual([
      'alpha/tmp/alpha',
      'feature/xWorkstream · alpha',
      'ImplementSession · alpha › feature/x',
    ]);
  });

  it('switches back to places when the > is deleted', () => {
    renderPalette('>', commands);
    fireEvent.change(input(), { target: { value: 'impl' } });
    expect(options()).toEqual(['ImplementSession · alpha › feature/x']);
  });

  it('matches a place by where it lives, too', () => {
    renderPalette('');
    fireEvent.change(input(), { target: { value: 'alpha impl' } });
    expect(options()).toEqual(['ImplementSession · alpha › feature/x']);
  });

  const place = (
    id: string,
    name: string,
    path: string,
    workstreams: Project['workstreams'] = [],
  ): Project => ({ ...projects[0], id, name, path, workstreams });

  it.each([
    [
      'a place named like the query over places that only live there',
      [
        ...projects,
        place('p2', 'beta', '/tmp/beta', [
          {
            ...projects[0].workstreams[0],
            id: 'w2',
            name: 'alpha-fix',
            sessions: [],
          },
        ]),
      ],
      'alpha',
      [
        'alpha/tmp/alpha',
        'alpha-fixWorkstream · beta',
        'feature/xWorkstream · alpha',
        'ImplementSession · alpha › feature/x',
      ],
    ],
    [
      'even a scattered name match over a location-only one',
      [place('p1', 'zeta', '/srv/ab'), place('p2', 'a-lpha-b', '/tmp/x')],
      'ab',
      ['a-lpha-b/tmp/x', 'zeta/srv/ab'],
    ],
  ])('ranks %s', (_case, shown, query, expected) => {
    renderPalette('', [], shown);
    fireEvent.change(input(), { target: { value: query } });
    expect(options()).toEqual(expected);
  });

  it('goes to the place picked', () => {
    const { onGoTo, onClose } = renderPalette('');
    fireEvent.click(screen.getByRole('option', { name: /^Implement/ }));
    expect(onClose).toHaveBeenCalled();
    expect(onGoTo).toHaveBeenCalledWith('p1', 's1');
  });
});
