import type { Project } from '@homeshell/model';
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react';
import type { ReactNode } from 'react';
import { vi } from 'vitest';

import {
  eventSourceForUrl,
  FakeEventSource,
} from '../testing/fake-event-source';
import App from './app';
import { CommandsProvider } from './keyboard/commands';
import { SettingsProvider } from './settings/use-settings';
import { ThemeProvider } from './use-theme';

const workstream = {
  id: 'w1',
  name: 'feature/x',
  provider: 'Eclipse Enclave',
  strategyId: 'worktree',
  strategy: 'worktree',
  state: 'waiting',
  runtimeState: 'running',
  effectiveState: 'waiting',
  // The second mode is deliberately one the UI has never heard of.
  openModes: [
    { id: 'open-with:terminal', label: 'Terminal', default: true },
    { id: 'some-mode-the-ui-does-not-know', label: 'Named By The Daemon' },
  ],
  actions: [
    {
      id: 'note',
      label: 'Notes',
      description:
        "Open this workstream's notes in your desktop's default application",
    },
  ],
  sessions: [
    { id: 's1', title: 'Implement', agent: 'Claude Code', state: 'running' },
    { id: 's2', title: 'Verify', agent: 'Claude Code', state: 'waiting' },
  ],
} satisfies Project['workstreams'][number];

const projectsFixture: Project[] = [
  {
    id: 'p1',
    name: 'alpha',
    path: '/tmp/alpha',
    gitBacked: true,
    state: 'waiting',
    runtimeState: 'running',
    effectiveState: 'waiting',
    untrackedWorktrees: [],
    workstreams: [workstream],
  },
  {
    id: 'p2',
    name: 'beta',
    path: '/tmp/beta',
    gitBacked: false,
    state: 'idle',
    runtimeState: 'idle',
    effectiveState: 'idle',
    untrackedWorktrees: [],
    workstreams: [],
  },
];

/** The providers main.tsx wraps the app in. */
function AppProviders({ children }: { children: ReactNode }) {
  return (
    <ThemeProvider>
      <SettingsProvider>
        <CommandsProvider>{children}</CommandsProvider>
      </SettingsProvider>
    </ThemeProvider>
  );
}

/** Runs a command the way a user would: F1, its name, then Enter. */
function runCommand(name: string) {
  fireEvent.keyDown(window, { key: 'F1' });
  const box = within(
    screen.getByRole('dialog', { name: 'Command palette' }),
  ).getByRole('combobox');
  fireEvent.change(box, { target: { value: `>${name}` } });
  fireEvent.keyDown(box, { key: 'Enter' });
}

describe('App', () => {
  beforeEach(() => {
    localStorage.clear();
    FakeEventSource.reset();
    vi.stubGlobal('EventSource', FakeEventSource);
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue({
        ok: true,
        json: () =>
          Promise.resolve({
            current: 'test',
            latest: 'test',
            updateAvailable: false,
          }),
      }),
    );
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  const projectNav = () => screen.getByRole('navigation', { name: 'Projects' });

  // Renders the app, delivers a snapshot, and waits for /api/version to settle.
  const renderApp = async () => {
    render(
      <AppProviders>
        <App />
      </AppProviders>,
    );
    act(() => {
      eventSourceForUrl('/api/events').emit('state', {
        projects: projectsFixture,
      });
    });
    await screen.findByText('daemon test');
  };

  it('shows a connecting state until the snapshot arrives', () => {
    render(
      <AppProviders>
        <App />
      </AppProviders>,
    );
    expect(screen.getByText('connecting…')).toBeTruthy();
    expect(screen.getByText('Waiting for the daemon…')).toBeTruthy();
    expect(screen.getByText('Connecting to the daemon…')).toBeTruthy();
  });

  it('lists the projects from the daemon snapshot', async () => {
    await renderApp();
    for (const project of projectsFixture) {
      expect(within(projectNav()).getByText(project.name)).toBeTruthy();
    }
  });

  it('selects the first project by default and shows its workstreams', async () => {
    await renderApp();
    expect(
      screen.getByRole('heading', { level: 1, name: 'alpha' }),
    ).toBeTruthy();
    expect(screen.getByRole('heading', { name: 'feature/x' })).toBeTruthy();
  });

  it('switches the workstream panel when another project is selected', async () => {
    await renderApp();
    fireEvent.click(within(projectNav()).getByText('beta'));
    expect(
      screen.getByRole('heading', { level: 1, name: 'beta' }),
    ).toBeTruthy();
  });

  it('applies live state events from the stream', async () => {
    await renderApp();
    const updated: Project[] = [
      {
        ...projectsFixture[0],
        state: 'done',
        effectiveState: 'done',
        workstreams: [
          {
            ...workstream,
            state: 'done',
            effectiveState: 'done',
            sessions: [
              {
                id: 's1',
                title: 'Implement',
                agent: 'Claude Code',
                state: 'done',
                ended: true,
              },
              {
                id: 's3',
                title: 'Fresh session',
                agent: 'Demo',
                state: 'done',
                ended: true,
              },
            ],
          },
        ],
      },
      projectsFixture[1],
    ];
    act(() => {
      eventSourceForUrl('/api/events').emit('state', {
        projects: updated,
      });
    });
    expect(screen.getByText('Fresh session')).toBeTruthy();
    // The card's session counts say what its sessions are doing now.
    expect(screen.getByRole('img', { name: 'Sessions: 2 ended' })).toBeTruthy();
  });

  it('shows a toast when the daemon pushes a notification', async () => {
    await renderApp();
    act(() => {
      eventSourceForUrl('/api/events').emit('notification', {
        id: 'n1',
        kind: 'waiting',
        title: 'Implement is waiting for input',
        body: 'alpha · feature/x',
        projectId: 'p1',
        projectName: 'alpha',
        workstreamId: 'w1',
        sessionId: 's1',
        sessionTitle: 'Implement',
        agent: 'Claude Code',
      });
    });
    const toast = await screen.findByRole('alert');
    expect(
      within(toast).getByText('Implement is waiting for input'),
    ).toBeTruthy();
    expect(within(toast).getByText('alpha · feature/x')).toBeTruthy();
  });

  it('reports the daemon as offline when the stream errors', async () => {
    await renderApp();
    act(() => {
      // A transient drop: the browser is still retrying, so this reports offline
      // without the reconnect machinery getting involved.
      eventSourceForUrl('/api/events').fail(FakeEventSource.CONNECTING);
    });
    expect(screen.getByText('daemon offline')).toBeTruthy();
    // Left open for the browser's own retry; only the fatal path closes it.
    expect(eventSourceForUrl('/api/events').closed).toBe(false);
  });

  it('opens and closes the add-project form', async () => {
    await renderApp();
    const addButton = screen.getByRole('button', { name: 'Add project' });
    expect((addButton as HTMLButtonElement).disabled).toBe(false);
    fireEvent.click(addButton);
    expect(screen.getByPlaceholderText('/path/to/project')).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(screen.queryByPlaceholderText('/path/to/project')).toBeNull();
  });

  describe('focus around the add-project form', () => {
    const addButton = () => screen.getByRole('button', { name: 'Add project' });
    const pathBox = () => screen.getByPlaceholderText('/path/to/project');

    it('keeps Add project enabled, and pressing it again refocuses the form', async () => {
      await renderApp();
      fireEvent.click(addButton());
      act(() => addButton().focus());
      fireEvent.click(addButton());
      expect((addButton() as HTMLButtonElement).disabled).toBe(false);
      expect(document.activeElement).toBe(pathBox());
    });

    it('gives focus back to Add project on Escape', async () => {
      await renderApp();
      act(() => addButton().focus());
      fireEvent.click(addButton());
      fireEvent.keyDown(pathBox(), { key: 'Escape' });
      await waitFor(() => expect(document.activeElement).toBe(addButton()));
    });

    it('selects the added project and focuses it in the sidebar', async () => {
      await renderApp();
      vi.mocked(fetch).mockResolvedValueOnce({
        ok: true,
        status: 201,
        json: async () => ({ id: 'p3' }),
      } as Response);
      fireEvent.click(addButton());
      fireEvent.change(pathBox(), { target: { value: '/tmp/gamma' } });
      fireEvent.submit(screen.getByRole('form', { name: 'Add project' }));
      await waitFor(() =>
        expect(screen.queryByRole('form', { name: 'Add project' })).toBeNull(),
      );

      // It is listed only once the daemon's state push says so.
      const gamma = { ...projectsFixture[1], id: 'p3', name: 'gamma' };
      act(() => {
        eventSourceForUrl('/api/events').emit('state', {
          projects: [...projectsFixture, gamma],
        });
      });

      const entry = within(projectNav()).getByRole('button', { name: /gamma/ });
      await waitFor(() => expect(document.activeElement).toBe(entry));
      expect(entry.getAttribute('aria-current')).toBe('true');
    });
  });

  it('opens and closes the new-workstream form', async () => {
    await renderApp();
    fireEvent.click(screen.getByRole('button', { name: 'New workstream' }));
    const cancel = await screen.findByRole('button', { name: 'Cancel' });
    fireEvent.click(cancel);
    expect(screen.queryByRole('button', { name: 'Cancel' })).toBeNull();
  });

  it('asks the daemon for the default open with a bare Open click', async () => {
    await renderApp();
    fireEvent.click(
      screen.getByRole('button', { name: 'Open workstream feature/x' }),
    );
    await waitFor(() => {
      expect(fetch).toHaveBeenCalledWith(
        '/api/projects/p1/workstreams/w1/open',
        { method: 'POST' },
      );
    });
  });

  it('asks the daemon to open the note the projection advertised', async () => {
    await renderApp();
    fireEvent.click(
      screen.getByRole('button', { name: 'Notes for workstream feature/x' }),
    );
    await waitFor(() => {
      expect(fetch).toHaveBeenCalledWith(
        '/api/projects/p1/workstreams/w1/note',
        { method: 'POST' },
      );
    });
  });

  it('asks the daemon to open a workstream in a chosen mode', async () => {
    await renderApp();
    fireEvent.click(
      screen.getByRole('button', { name: 'Open workstream feature/x with…' }),
    );
    fireEvent.click(screen.getByRole('menuitem', { name: 'Terminal' }));
    await waitFor(() => {
      expect(fetch).toHaveBeenCalledWith(
        '/api/projects/p1/workstreams/w1/open-with',
        {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ mode: 'open-with:terminal' }),
        },
      );
    });
  });

  it('renders one open choice per mode the daemon declared', async () => {
    await renderApp();
    fireEvent.click(
      screen.getByRole('button', { name: 'Open workstream feature/x with…' }),
    );
    // The daemon named it; the UI never maps a mode id to a name.
    expect(
      screen.getByRole('menuitem', { name: 'Named By The Daemon' }),
    ).toBeTruthy();
  });

  it('opens a browser tab when the daemon resolves a URL', async () => {
    await renderApp();
    // The daemon resolves the URL but does not proxy it, so the client opens it.
    const open = vi.fn();
    vi.stubGlobal('open', open);
    vi.mocked(fetch).mockResolvedValueOnce({
      ok: true,
      json: () =>
        Promise.resolve({
          mode: 'link:web-ide',
          data: { target: 'browser', url: 'http://127.0.0.1:3000' },
        }),
    } as Response);

    fireEvent.click(
      screen.getByRole('button', { name: 'Open workstream feature/x with…' }),
    );
    fireEvent.click(screen.getByRole('menuitem', { name: 'Terminal' }));

    // No `noopener` in the features: with it the spec has window.open return null
    // even on success, so the client could not tell an opened tab from a blocked
    // one and would ask the daemon to open the URL a second time.
    await waitFor(() => {
      expect(open).toHaveBeenCalledWith('http://127.0.0.1:3000', '_blank');
    });
  });

  it('reports a refused workstream open', async () => {
    await renderApp();
    // A workstream with nothing on disk comes back as a 409.
    vi.mocked(fetch).mockResolvedValueOnce({
      ok: false,
      json: () =>
        Promise.resolve({
          error: 'workstream has no working directory to open',
        }),
    } as Response);
    fireEvent.click(
      screen.getByRole('button', { name: 'Open workstream feature/x' }),
    );
    expect(
      await screen.findByText('Workstream has no working directory to open'),
    ).toBeTruthy();
  });

  describe('the project header', () => {
    const removeButton = () =>
      screen.getByRole('button', { name: 'Delete project alpha' });

    it('asks before removing a project, on Cancel, and Cancel backs out', async () => {
      await renderApp();
      act(() => removeButton().focus());
      fireEvent.click(removeButton());
      const dialog = screen.getByRole('dialog', { name: 'Delete "alpha"?' });
      expect(document.activeElement).toBe(
        within(dialog).getByRole('button', { name: 'Cancel' }),
      );

      fireEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }));
      expect(screen.queryByRole('dialog')).toBeNull();
      expect(document.activeElement).toBe(removeButton());
    });

    it('focuses the project selected in its place once one is removed', async () => {
      await renderApp();
      fireEvent.click(removeButton());
      const dialog = screen.getByRole('dialog', { name: 'Delete "alpha"?' });
      fireEvent.click(within(dialog).getByRole('button', { name: 'Delete' }));
      await waitFor(() =>
        expect(fetch).toHaveBeenCalledWith('/api/projects/p1', {
          method: 'DELETE',
        }),
      );

      act(() => {
        eventSourceForUrl('/api/events').emit('state', {
          projects: [projectsFixture[1]],
        });
      });

      const beta = within(projectNav()).getByRole('button', { name: /beta/ });
      await waitFor(() => expect(document.activeElement).toBe(beta));
      expect(beta.getAttribute('aria-current')).toBe('true');
    });

    it.each(['Enter', 'Escape'])(
      'gives focus back to the header when %s leaves the rename',
      async (key) => {
        await renderApp();
        // The name takes no focus, so focus falls back to the header's first
        // control.
        fireEvent.doubleClick(
          screen.getByRole('heading', { level: 1, name: 'alpha' }),
        );
        fireEvent.keyDown(
          screen.getByRole('textbox', { name: 'Project name' }),
          {
            key,
          },
        );

        await waitFor(() =>
          expect(document.activeElement).toBe(
            screen.getByRole('button', { name: 'New workstream' }),
          ),
        );
      },
    );
  });

  it('preserves the log buffer across closing and reopening the pane', async () => {
    await renderApp();

    runCommand('Toggle logs'); // open
    expect(screen.getByRole('region', { name: 'Logs' })).toBeTruthy();
    act(() => {
      eventSourceForUrl('/api/logs').emit('log', {
        seq: 1,
        time: 't',
        level: 'warn',
        msg: 'persisted-log',
      });
    });
    expect(screen.getByText('persisted-log')).toBeTruthy();

    runCommand('Toggle logs'); // close — pane hidden
    expect(screen.queryByText('persisted-log')).toBeNull();

    runCommand('Toggle logs'); // reopen — the buffer survived
    expect(screen.getByText('persisted-log')).toBeTruthy();
  });

  it('toggles the settings page from the toolbar button', async () => {
    await renderApp();
    const settingsButton = screen.getByRole('button', { name: 'Settings' });

    fireEvent.click(settingsButton);
    expect(
      await screen.findByRole('heading', { level: 1, name: 'Settings' }),
    ).toBeTruthy();

    fireEvent.click(settingsButton);
    await waitFor(() =>
      expect(
        screen.queryByRole('heading', { level: 1, name: 'Settings' }),
      ).toBeNull(),
    );
  });

  describe('from the keyboard', () => {
    // The route lives in the location, which outlasts the test.
    afterEach(() => {
      window.location.hash = '';
    });

    const palette = () => screen.getByRole('combobox');

    it.each([
      ['Ctrl+Shift+P', { key: 'P', ctrlKey: true, shiftKey: true }],
      ['F1', { key: 'F1' }],
    ])('opens the command palette on %s', async (_keys, init) => {
      await renderApp();
      fireEvent.keyDown(window, init);
      expect(
        screen.getByRole('dialog', { name: 'Command palette' }),
      ).toBeTruthy();
      expect((palette() as HTMLInputElement).value).toBe('>');
    });

    it.each([
      [
        'Ctrl+K Ctrl+S',
        [
          { key: 'k', ctrlKey: true },
          { key: 's', ctrlKey: true },
        ],
      ],
      ['?', [{ key: '?', shiftKey: true }]],
    ])('shows the shortcut reference on %s', async (_keys, strokes) => {
      await renderApp();
      for (const stroke of strokes) fireEvent.keyDown(window, stroke);
      expect(
        screen.getByRole('dialog', { name: 'Keyboard shortcuts' }),
      ).toBeTruthy();
    });

    it('does not take ? from someone typing it', async () => {
      await renderApp();
      fireEvent.click(screen.getByRole('button', { name: 'Add project' }));
      fireEvent.keyDown(screen.getByPlaceholderText('/path/to/project'), {
        key: '?',
        shiftKey: true,
      });
      expect(
        screen.queryByRole('dialog', { name: 'Keyboard shortcuts' }),
      ).toBeNull();
    });

    it('toggles the theme from the palette', async () => {
      await renderApp();
      runCommand('Toggle light/dark theme');
      expect(document.documentElement.getAttribute('data-theme')).toBe('light');
      runCommand('Toggle light/dark theme');
      expect(document.documentElement.hasAttribute('data-theme')).toBe(false);
    });

    it('opens the add-project form from the palette', async () => {
      await renderApp();
      runCommand('Add project');
      expect(screen.getByPlaceholderText('/path/to/project')).toBeTruthy();
    });

    // A chord's second key is the palette's, not the focused row's: Enter must
    // not also open the session.
    it('keeps a broken chord’s key from the focused row', async () => {
      await renderApp();
      const opens = () =>
        vi
          .mocked(fetch)
          .mock.calls.filter(([url]) => String(url).endsWith('/open')).length;
      const row = within(
        screen.getByRole('region', { name: 'Workstream feature/x' }),
      ).getAllByRole('listitem')[0];
      act(() => row.focus());

      fireEvent.keyDown(row, { key: 'k', ctrlKey: true });
      fireEvent.keyDown(row, { key: 'Enter' });
      expect(opens()).toBe(0);

      fireEvent.keyDown(row, { key: 'Enter' });
      expect(opens()).toBe(1);
    });

    it('opens settings on Ctrl+,', async () => {
      await renderApp();
      fireEvent.keyDown(window, { key: ',', ctrlKey: true });
      expect(
        await screen.findByRole('heading', { level: 1, name: 'Settings' }),
      ).toBeTruthy();
    });

    it('focuses the list again when settings close', async () => {
      await renderApp();
      fireEvent.keyDown(window, { key: ',', ctrlKey: true });
      const nav = await screen.findByRole('navigation', {
        name: 'Settings categories',
      });
      expect(nav.contains(document.activeElement)).toBe(true);

      fireEvent.click(screen.getByRole('button', { name: 'Close' }));
      await waitFor(() =>
        expect(document.activeElement).toBe(
          screen.getByRole('region', { name: 'Workstream feature/x' }),
        ),
      );
    });

    // Ctrl+, reaches the app from inside the palette's box, and must not open
    // settings underneath it.
    it('closes the palette to run a command by its key', async () => {
      await renderApp();
      fireEvent.keyDown(window, { key: 'F1' });
      fireEvent.keyDown(palette(), { key: ',', ctrlKey: true });
      expect(
        await screen.findByRole('heading', { level: 1, name: 'Settings' }),
      ).toBeTruthy();
      expect(screen.queryByRole('dialog')).toBeNull();
    });

    // The project view's commands, not only the app's, close it first.
    it('closes the palette to start a session by its key', async () => {
      await renderApp();
      fireEvent.keyDown(window, { key: 'F1' });
      fireEvent.keyDown(palette(), {
        key: '~',
        code: 'Backquote',
        ctrlKey: true,
        shiftKey: true,
      });
      // The fetch stub lists no agents, which the card's inline form says.
      expect(
        await screen.findByText(
          'No agent is available to start a session here.',
        ),
      ).toBeTruthy();
      expect(screen.queryByRole('dialog')).toBeNull();
    });

    // The palette's own box changes its mode; its keys must still switch it.
    it.each([
      ['F1', { key: 'F1' }, 'x', '>'],
      ['Ctrl+P', { key: 'p', ctrlKey: true }, '>x', ''],
    ])(
      'switches the palette back on %s after typing changed its mode',
      async (_keys, init, typed, expected) => {
        await renderApp();
        fireEvent.keyDown(window, init);
        fireEvent.change(palette(), { target: { value: typed } });
        fireEvent.keyDown(palette(), init);
        expect((palette() as HTMLInputElement).value).toBe(expected);
      },
    );

    it('switches to going somewhere when that is picked from the command palette', async () => {
      await renderApp();
      runCommand('Go to a project');
      expect(screen.getByRole('dialog', { name: 'Go to' })).toBeTruthy();
      expect((palette() as HTMLInputElement).value).toBe('');
    });

    // beta has no workstreams, so there is no card to land on.
    it('focuses the first New workstream of a project quick open went to', async () => {
      await renderApp();
      fireEvent.keyDown(window, { key: 'p', ctrlKey: true });
      fireEvent.change(palette(), { target: { value: 'beta' } });
      fireEvent.keyDown(palette(), { key: 'Enter' });

      const buttons = await screen.findAllByRole('button', {
        name: 'New workstream',
      });
      await waitFor(() =>
        expect(document.activeElement).toBe(buttons[buttons.length - 1]),
      );
    });

    it('jumps to a session on Ctrl+P and focuses its row', async () => {
      await renderApp();
      fireEvent.click(
        within(projectNav()).getByRole('button', { name: /beta/ }),
      );
      fireEvent.keyDown(window, { key: 'p', ctrlKey: true });
      expect(screen.getByRole('dialog', { name: 'Go to' })).toBeTruthy();
      fireEvent.change(palette(), { target: { value: 'verify' } });
      fireEvent.keyDown(palette(), { key: 'Enter' });

      await waitFor(() =>
        expect(document.activeElement?.textContent).toContain('Verify'),
      );
      expect(document.activeElement?.tagName).toBe('LI');
      expect(screen.queryByRole('dialog')).toBeNull();
    });
  });

  describe('Alt and a letter', () => {
    const alt = (target: Element | Window = document.body) =>
      fireEvent.keyDown(target, { key: 'Alt', code: 'AltLeft', altKey: true });
    const altLetter = (letter: string) =>
      fireEvent.keyDown(document.body, {
        key: letter,
        code: `Key${letter.toUpperCase()}`,
        altKey: true,
      });

    it('underlines the letters in reach while Alt is held', async () => {
      await renderApp();
      const newWorkstream = screen.getByRole('button', {
        name: 'New workstream',
      });
      alt();
      expect(newWorkstream.hasAttribute('data-mnemonic-shown')).toBe(true);
      expect(
        screen
          .getByRole('button', { name: 'Add project' })
          .hasAttribute('data-mnemonic-shown'),
      ).toBe(true);
      // A card is not in reach until it is selected.
      expect(
        screen
          .getByRole('button', { name: 'New session' })
          .hasAttribute('data-mnemonic-shown'),
      ).toBe(false);

      fireEvent.keyUp(document.body, { key: 'Alt', code: 'AltLeft' });
      expect(newWorkstream.hasAttribute('data-mnemonic-shown')).toBe(false);
    });

    it('names the letter in the tooltip', async () => {
      await renderApp();
      expect(
        screen.getByRole('button', { name: 'New workstream' }).title,
      ).toMatch(/\(Alt\+W\)$/);
    });

    it('opens the new-workstream form on Alt+W', async () => {
      await renderApp();
      altLetter('w');
      // No provider is stubbed, so the form opens on saying so.
      expect(
        await screen.findByText('No provider can create a workstream here.'),
      ).toBeTruthy();
    });

    it('opens the add-project form on Alt+A', async () => {
      await renderApp();
      altLetter('a');
      expect(
        await screen.findByRole('form', { name: 'Add project' }),
      ).toBeTruthy();
    });

    it('starts a session in the selected workstream on Alt+N', async () => {
      await renderApp();
      const card = screen.getByRole('region', { name: 'Workstream feature/x' });
      act(() => card.focus());
      expect(card.getAttribute('aria-current')).toBe('true');

      alt();
      expect(
        screen
          .getByRole('button', { name: 'New session' })
          .hasAttribute('data-mnemonic-shown'),
      ).toBe(true);

      altLetter('n');
      // The card's New session, which asks for the session types first.
      await waitFor(() =>
        expect(fetch).toHaveBeenCalledWith(
          '/api/projects/p1/workstreams/w1/session-schemas',
        ),
      );
      expect(screen.queryByRole('form', { name: 'New workstream' })).toBeNull();
    });

    it('leaves a letter typed into a text field alone', async () => {
      await renderApp();
      fireEvent.click(screen.getByRole('button', { name: 'Add project' }));
      const path = await screen.findByRole('textbox', { name: 'Path' });
      fireEvent.keyDown(path, { key: 'w', code: 'KeyW', altKey: true });
      await act(async () => undefined);
      expect(
        screen.queryByText('No provider can create a workstream here.'),
      ).toBeNull();
    });
  });
});

/**
 * Losing the daemon: the actions that would call it go disabled everywhere at
 * once, driven by the one connection state. The projection already on screen is
 * deliberately left in place — it is the last thing known to be true, and it is
 * what the user is looking at while the daemon comes back.
 */
describe('App while the daemon is unreachable', () => {
  beforeEach(() => {
    localStorage.clear();
    FakeEventSource.reset();
    vi.stubGlobal('EventSource', FakeEventSource);
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue({
        ok: true,
        json: () =>
          Promise.resolve({
            current: 'test',
            latest: 'test',
            updateAvailable: false,
          }),
      }),
    );
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  /** Renders, delivers a snapshot, then drops the stream. */
  const renderOffline = async () => {
    render(
      <AppProviders>
        <App />
      </AppProviders>,
    );
    act(() => {
      eventSourceForUrl('/api/events').emit('state', {
        projects: projectsFixture,
      });
    });
    await screen.findByText('daemon test');
    act(() => {
      eventSourceForUrl('/api/events').fail(FakeEventSource.CONNECTING);
    });
    await screen.findByText('daemon offline');
  };

  const button = (name: string | RegExp) =>
    screen.getByRole('button', { name }) as HTMLButtonElement;

  it('offers no commands that need the daemon', async () => {
    await renderOffline();
    fireEvent.keyDown(window, { key: 'F1' });
    const labels = screen
      .getAllByRole('option')
      .map((option) => option.textContent);
    expect(labels.some((label) => label?.startsWith('Add project'))).toBe(
      false,
    );
    expect(labels.some((label) => label?.startsWith('New workstream'))).toBe(
      false,
    );
    expect(labels.some((label) => label?.startsWith('Toggle logs'))).toBe(true);
  });

  it('disables the project actions', async () => {
    await renderOffline();
    expect(button('Add project').disabled).toBe(true);
    expect(
      screen.getByRole('heading', { level: 1, name: 'alpha' }).title,
    ).toMatch(/\n\n.+/);
    expect(
      screen
        .getByRole('heading', { level: 1, name: 'alpha' })
        .hasAttribute('data-mnemonic'),
    ).toBe(false);
    expect(button('Delete project alpha').disabled).toBe(true);
    expect(button('New workstream').disabled).toBe(true);
  });

  it('disables the workstream and session actions', async () => {
    await renderOffline();
    expect(button('New session').disabled).toBe(true);
    expect(button(/^Delete workstream/).disabled).toBe(true);
    // Both halves of the Open split button: the default open and the modes menu.
    expect(button(/^Open workstream [^ ]+$/).disabled).toBe(true);
    expect(button(/^Open workstream .+ with…$/).disabled).toBe(true);
    // A session's menu keeps only what needs no daemon.
    const row = screen.getByText('Implement').closest('li') as HTMLElement;
    fireEvent.click(within(row).getByRole('button', { name: 'More actions' }));
    expect(
      screen.getAllByRole('menuitem').map((item) => item.textContent),
    ).toEqual(['Show details']);
  });

  it('keeps the state it already had on screen', async () => {
    await renderOffline();
    expect(screen.getByRole('heading', { name: 'feature/x' })).toBeTruthy();
    expect(screen.getByText('Implement')).toBeTruthy();
  });

  // Choosing a project and opening the settings are local.
  it('leaves the local controls usable', async () => {
    await renderOffline();
    expect(button('Settings').disabled).toBe(false);
    fireEvent.click(
      within(screen.getByRole('navigation', { name: 'Projects' })).getByText(
        'beta',
      ),
    );
    expect(
      screen.getByRole('heading', { level: 1, name: 'beta' }),
    ).toBeTruthy();
  });

  it('re-enables everything when the daemon comes back', async () => {
    await renderOffline();
    act(() => {
      eventSourceForUrl('/api/events').emit('state', {
        projects: projectsFixture,
      });
    });
    await screen.findByText('daemon test');
    expect(button('Add project').disabled).toBe(false);
    expect(button('New workstream').disabled).toBe(false);
    expect(button('New session').disabled).toBe(false);
  });
});
