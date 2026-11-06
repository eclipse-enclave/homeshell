import type { EffectiveState, Workstream } from '@homeshell/model';
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react';
import { createRef } from 'react';

import { useMnemonics } from './mnemonic';
import { WorkstreamCard, type WorkstreamCardHandle } from './workstream-card';

/** A rename handler the daemon accepts (null = no error). */
const renameOk = () => vi.fn(async () => null);

/** The card's name, which is what renames it. */
const cardName = () =>
  screen.getByRole('heading', { name: 'feature/self-update' });

/** Starts a rename as the mouse does: a double-click on the name. */
const renameByName = () => fireEvent.doubleClick(cardName());

/** Gives the page Alt's letters, as the app does. */
function Mnemonics() {
  useMnemonics();
  return null;
}

const workstream = (
  state: Workstream['state'],
  sessions: Workstream['sessions'],
  effectiveState?: EffectiveState,
  runtimeState?: Workstream['runtimeState'],
  overrides?: Partial<Workstream>,
): Workstream => ({
  id: 'ws-1',
  name: 'feature/self-update',
  provider: 'enclave',
  providerLabel: 'Eclipse Enclave',
  strategyId: 'worktree',
  strategy: 'worktree',
  branch: 'feature/self-update',
  state,
  runtimeState: runtimeState ?? 'running',
  effectiveState: effectiveState ?? state,
  sessions,
  ...overrides,
});

describe('WorkstreamCard', () => {
  it('renders name, provider, strategy and branch', () => {
    render(<WorkstreamCard workstream={workstream('idle', [])} />);
    expect(
      screen.getByRole('heading', { name: 'feature/self-update' }),
    ).toBeTruthy();
    expect(screen.getByText('Eclipse Enclave')).toBeTruthy();
    expect(screen.getByText('worktree')).toBeTruthy();
  });

  it('titles its name in full on hover only while it is cut off', () => {
    render(<WorkstreamCard workstream={workstream('idle', [])} />);
    const name = screen.getByRole('heading', { name: 'feature/self-update' });
    fireEvent.pointerEnter(name);
    expect(name.hasAttribute('title')).toBe(false);
    Object.defineProperty(name, 'scrollWidth', { value: 500 });
    fireEvent.pointerEnter(name);
    expect(name.getAttribute('title')).toBe('feature/self-update');
  });

  it('shows no provider pill when the daemon gives no provider label', () => {
    render(
      <WorkstreamCard
        workstream={workstream('idle', [], undefined, undefined, {
          provider: 'core',
          providerLabel: undefined,
        })}
      />,
    );
    expect(screen.queryByText('core')).toBeNull();
    expect(screen.getByText('worktree')).toBeTruthy();
  });

  it('renders one row per session', () => {
    render(
      <WorkstreamCard
        workstream={workstream('waiting', [
          {
            id: 's1',
            title: 'Implement',
            agent: 'Claude Code',
            state: 'running',
          },
          { id: 's2', title: 'Verify', agent: 'Claude Code', state: 'waiting' },
        ])}
      />,
    );
    expect(screen.getAllByRole('listitem')).toHaveLength(2);
  });

  // One badge per state, its count beside the dot, so the card says how
  // many sessions there are and what they are doing in one place.
  it('shows a badge per session state, with its count', () => {
    render(
      <WorkstreamCard
        workstream={workstream('waiting', [
          {
            id: 's1',
            title: 'Implement',
            agent: 'Claude Code',
            state: 'running',
          },
          { id: 's2', title: 'Verify', agent: 'Claude Code', state: 'waiting' },
          { id: 's3', title: 'Review', agent: 'Claude Code', state: 'running' },
        ])}
      />,
    );
    const badges = screen.getByRole('img', {
      name: 'Sessions: 1 waiting for input, 2 running',
    });
    expect(badges.textContent).toBe('12');
  });

  // The badges sit with the name rather than in a footer of their own, which
  // cost the card a whole line to say one number.
  it('places the badges in the header, beside the name', () => {
    render(
      <WorkstreamCard
        workstream={workstream('running', [
          { id: 's1', title: 'Work', agent: 'Claude Code', state: 'running' },
        ])}
      />,
    );
    const header = screen
      .getByRole('heading', { name: 'feature/self-update' })
      .closest('header') as HTMLElement;
    expect(within(header).getByTestId('workstream-sessions')).toBeTruthy();
    expect(screen.queryByText(/\d+ sessions?$/)).toBeNull();
  });

  it('counts sessions in their display state, as their rows show them', () => {
    render(
      <WorkstreamCard
        workstream={workstream('idle', [
          { id: 's1', title: 'Work', agent: 'Claude Code', state: 'idle' },
        ])}
        displayStateFor={() => 'done'}
      />,
    );
    expect(screen.getByRole('img', { name: 'Sessions: 1 done' })).toBeTruthy();
  });

  // The runtime's own state is the news while it is not up: the sessions
  // inside a workstream that is starting or failed say nothing about it.
  it.each([
    ['starting', 'Starting'],
    ['stopped', 'Stopped'],
    ['error', 'Error'],
  ] as const)('shows the workstream state while it is %s', (runtime, label) => {
    render(
      <WorkstreamCard
        workstream={workstream(
          'running',
          [{ id: 's1', title: 'Work', agent: 'Claude Code', state: 'running' }],
          runtime,
          runtime,
        )}
      />,
    );
    expect(
      within(screen.getByTestId('workstream-state')).getByText(label),
    ).toBeTruthy();
    expect(screen.queryByTestId('workstream-sessions')).toBeNull();
  });

  it('leaves the state to the sessions while the workstream is up', () => {
    render(
      <WorkstreamCard
        workstream={workstream('waiting', [
          { id: 's1', title: 'Verify', agent: 'Claude Code', state: 'waiting' },
        ])}
      />,
    );
    expect(screen.queryByTestId('workstream-state')).toBeNull();
  });

  // A zero badge is the whole of the empty state: an empty workstream reads
  // as one at a glance, with no "No sessions yet." line saying it again.
  it('says a workstream is empty once, through a zero badge', () => {
    render(<WorkstreamCard workstream={workstream('idle', [])} />);
    expect(screen.queryByText('No sessions yet.')).toBeNull();
    expect(
      screen.getByRole('img', { name: 'Sessions: No sessions' }).textContent,
    ).toBe('0');
  });

  // The default name of a workstream is its branch's, and then the pill says
  // it a second time.
  it('shows the branch only when it differs from the name', () => {
    const { rerender } = render(
      <WorkstreamCard workstream={workstream('idle', [])} />,
    );
    expect(
      screen.queryByText('feature/self-update', { selector: 'span' }),
    ).toBeNull();

    rerender(
      <WorkstreamCard
        workstream={workstream('idle', [], undefined, undefined, {
          name: 'self-update',
        })}
      />,
    );
    expect(screen.getByText('feature/self-update')).toBeTruthy();
  });

  it('disables "New session" when session creation is not wired', () => {
    render(<WorkstreamCard workstream={workstream('idle', [])} />);
    const button = screen.getByRole('button', { name: 'New session' });
    expect((button as HTMLButtonElement).disabled).toBe(true);
  });

  it.each([
    [new Error('Could not reach the daemon.'), 'Could not reach the daemon.'],
    [undefined, 'Could not load session types.'],
  ])('says why the session types could not be loaded', async (cause, said) => {
    render(
      <WorkstreamCard
        workstream={workstream('idle', [])}
        loadSessionOptions={vi.fn().mockRejectedValue(cause)}
        onCreateSession={vi.fn()}
      />,
    );
    fireEvent.click(screen.getByRole('button', { name: 'New session' }));
    expect((await screen.findByRole('alert')).textContent).toBe(said);
  });

  it('opens the session form (loading its options) and creates a session', async () => {
    const onCreateSession = vi.fn().mockResolvedValue(null);
    const loadSessionOptions = vi.fn().mockResolvedValue([
      {
        providerId: 'local',
        label: 'Local',
        options: [
          { id: 'claude', label: 'Claude Code', schema: { properties: {} } },
        ],
      },
    ]);
    render(
      <WorkstreamCard
        workstream={workstream('idle', [])}
        loadSessionOptions={loadSessionOptions}
        onCreateSession={onCreateSession}
      />,
    );
    fireEvent.click(screen.getByRole('button', { name: 'New session' }));
    expect(loadSessionOptions).toHaveBeenCalled();
    // A single option renders no agent selector.
    const form = await screen.findByRole('form', { name: 'New session' });
    expect(within(form).queryByLabelText('Agent')).toBeNull();
    fireEvent.click(
      within(form).getByRole('button', { name: 'Start session' }),
    );
    expect(onCreateSession).toHaveBeenCalledWith({
      providerId: 'local',
      optionId: 'claude',
      config: {},
    });
  });

  it('leaves a right-click in the session form to the form', async () => {
    render(
      <WorkstreamCard
        workstream={workstream('idle', [])}
        loadSessionOptions={vi.fn().mockResolvedValue([
          {
            providerId: 'local',
            label: 'Local',
            options: [
              {
                id: 'claude',
                label: 'Claude Code',
                schema: {
                  properties: {
                    model: { type: 'string', title: 'Model', enum: ['a', 'b'] },
                  },
                },
              },
            ],
          },
        ])}
        onCreateSession={vi.fn()}
      />,
    );
    fireEvent.click(screen.getByRole('button', { name: 'New session' }));
    const form = await screen.findByRole('form', { name: 'New session' });
    fireEvent.contextMenu(within(form).getByText('Model'));
    expect(screen.queryByRole('menu')).toBeNull();
    // The card's own header still opens it.
    fireEvent.contextMenu(screen.getByRole('region', { name: /^Workstream / }));
    expect(screen.getByRole('menu')).toBeTruthy();
  });

  describe('focus around the session form', () => {
    const options = [
      {
        providerId: 'local',
        label: 'Local',
        options: [
          { id: 'claude', label: 'Claude Code', schema: { properties: {} } },
        ],
      },
    ];

    async function openForm() {
      render(
        <WorkstreamCard
          workstream={workstream('idle', [])}
          loadSessionOptions={vi.fn().mockResolvedValue(options)}
          onCreateSession={vi.fn().mockResolvedValue(null)}
        />,
      );
      const opener = screen.getByRole('button', { name: 'New session' });
      opener.focus();
      fireEvent.click(opener);
      const form = await screen.findByRole('form', { name: 'New session' });
      const start = within(form).getByRole('button', { name: 'Start session' });
      await waitFor(() => expect(document.activeElement).toBe(start));
      return { opener, form, start };
    }

    // Disabling it would drop the focus of whoever pressed it.
    it('keeps New session enabled, and pressing it again refocuses the form', async () => {
      const { opener, start } = await openForm();
      expect((opener as HTMLButtonElement).disabled).toBe(false);
      opener.focus();
      fireEvent.click(opener);
      expect(document.activeElement).toBe(start);
      expect(screen.getAllByRole('form', { name: 'New session' })).toHaveLength(
        1,
      );
    });

    it('opens and refocuses the form through its handle', async () => {
      const handle = createRef<WorkstreamCardHandle>();
      render(
        <WorkstreamCard
          ref={handle}
          workstream={workstream('idle', [])}
          loadSessionOptions={vi.fn().mockResolvedValue(options)}
          onCreateSession={vi.fn().mockResolvedValue(null)}
        />,
      );
      act(() => {
        handle.current?.newSession();
      });
      const start = await screen.findByRole('button', {
        name: 'Start session',
      });
      await waitFor(() => expect(document.activeElement).toBe(start));

      act(() => screen.getByRole('button', { name: 'New session' }).focus());
      act(() => {
        handle.current?.newSession();
      });
      expect(document.activeElement).toBe(start);
    });

    // It is the card's New session, offered or not the same way.
    it('opens nothing through its handle while New session is not offered', () => {
      const handle = createRef<WorkstreamCardHandle>();
      const loadSessionOptions = vi.fn().mockResolvedValue(options);
      render(
        <WorkstreamCard
          ref={handle}
          workstream={workstream('idle', [])}
          loadSessionOptions={loadSessionOptions}
          onCreateSession={vi.fn()}
          disabledReason="Offline"
        />,
      );
      act(() => {
        handle.current?.newSession();
      });
      expect(loadSessionOptions).not.toHaveBeenCalled();
      expect(screen.queryByRole('form', { name: 'New session' })).toBeNull();
    });

    // A session started in it would have nowhere to land once the real
    // workstream replaces it.
    it('offers no New session on a creating placeholder', () => {
      const handle = createRef<WorkstreamCardHandle>();
      render(
        <WorkstreamCard
          ref={handle}
          workstream={workstream('idle', [], 'creating', 'creating', {
            id: 'creating-1',
          })}
          loadSessionOptions={vi.fn().mockResolvedValue(options)}
          onCreateSession={vi.fn()}
        />,
      );
      expect(
        (
          screen.getByRole('button', {
            name: 'New session',
          }) as HTMLButtonElement
        ).disabled,
      ).toBe(true);
      expect(
        handle.current
          ?.actions('creating-1')
          .find((action) => action.id === 'newSession')?.disabled,
      ).toBe(true);
    });

    it.each([
      [
        'Cancel',
        (form: HTMLElement) =>
          fireEvent.click(within(form).getByRole('button', { name: 'Cancel' })),
      ],
      [
        'Escape',
        (form: HTMLElement) => fireEvent.keyDown(form, { key: 'Escape' }),
      ],
    ])('gives focus back to New session on %s', async (_how, cancel) => {
      const { opener, form } = await openForm();
      cancel(form);
      await waitFor(() => expect(document.activeElement).toBe(opener));
      expect(screen.queryByRole('form', { name: 'New session' })).toBeNull();
    });
  });

  it('wires session Stop/Start/Delete to their handlers by session id', () => {
    const onStopSession = vi.fn();
    const onRemoveSession = vi.fn();
    render(
      <WorkstreamCard
        workstream={workstream('running', [
          { id: 's1', title: 'Work', agent: 'Claude Code', state: 'running' },
        ])}
        onStopSession={onStopSession}
        onRemoveSession={onRemoveSession}
      />,
    );
    fireEvent.click(screen.getByRole('button', { name: 'Stop' }));
    expect(onStopSession).toHaveBeenCalledWith('s1');
    // Delete opens a confirmation dialog; clicking Delete inside confirms.
    fireEvent.click(screen.getByRole('button', { name: /Delete session/ }));
    fireEvent.click(
      within(screen.getByRole('dialog')).getByRole('button', {
        name: 'Delete',
      }),
    );
    expect(onRemoveSession).toHaveBeenCalledWith('s1');
  });

  it('wires a session rename to its handler by session id', () => {
    const onRenameSession = vi.fn();
    render(
      <WorkstreamCard
        workstream={workstream('running', [
          { id: 's1', title: 'Work', agent: 'Claude Code', state: 'running' },
        ])}
        onRenameSession={onRenameSession}
      />,
    );
    fireEvent.doubleClick(screen.getByTestId('session-title'));
    const input = screen.getByLabelText('Session name');
    fireEvent.change(input, { target: { value: 'Port the CLI' } });
    fireEvent.keyDown(input, { key: 'Enter' });
    expect(onRenameSession).toHaveBeenCalledWith('s1', 'Port the CLI');
  });

  it('offers no session rename without a handler', () => {
    render(
      <WorkstreamCard
        workstream={workstream('running', [
          { id: 's1', title: 'Work', agent: 'Claude Code', state: 'running' },
        ])}
      />,
    );
    fireEvent.doubleClick(screen.getByTestId('session-title'));
    expect(screen.queryByLabelText('Session name')).toBeNull();
  });

  it('gates Stop/Start per session on its own provider, not the workstream', () => {
    // The workstream's provider is "core", which has no session start/stop.
    const onStopSession = vi.fn();
    render(
      <WorkstreamCard
        workstream={workstream(
          'running',
          [
            {
              id: 'a',
              title: 'Local',
              agent: 'Claude Code',
              state: 'running',
              provider: 'local',
            },
            {
              id: 'b',
              title: 'Enclave',
              agent: 'claude',
              state: 'running',
              provider: 'enclave',
            },
          ],
          undefined,
          undefined,
          { provider: 'core' },
        )}
        onStopSession={onStopSession}
        canSessionStartStop={(session) => session.provider === 'local'}
      />,
    );
    // Exactly one Stop button: the local session, not the enclave one.
    const stops = screen.getAllByRole('button', { name: 'Stop' });
    expect(stops).toHaveLength(1);
    fireEvent.click(stops[0]);
    expect(onStopSession).toHaveBeenCalledWith('a');
  });

  it('gates the send-text input per session on canPromptSession', () => {
    // The workstream's own provider has no prompting.
    render(
      <WorkstreamCard
        workstream={workstream(
          'running',
          [
            {
              id: 'a',
              title: 'Fake',
              agent: 'Claude Code',
              state: 'running',
              provider: 'fake',
            },
            {
              id: 'b',
              title: 'Bare',
              agent: 'claude',
              state: 'running',
              provider: 'core',
            },
          ],
          undefined,
          undefined,
          { provider: 'Eclipse Enclave' },
        )}
        onPromptSession={vi.fn()}
        canPromptSession={(session) => session.provider === 'fake'}
      />,
    );
    // Expand both rows; only the fake-owned session shows the input.
    for (const toggle of screen.getAllByRole('button', {
      name: /toggle session details/i,
    })) {
      fireEvent.click(toggle);
    }
    expect(screen.getAllByLabelText(/send text to session/i)).toHaveLength(1);
  });

  it('wires each session Open button to onOpenSession with the session id', () => {
    const onOpenSession = vi.fn();
    render(
      <WorkstreamCard
        workstream={workstream('running', [
          {
            id: 's1',
            title: 'Implement',
            agent: 'Claude Code',
            state: 'running',
          },
        ])}
        onOpenSession={onOpenSession}
      />,
    );
    fireEvent.click(screen.getByRole('button', { name: 'Open' }));
    expect(onOpenSession).toHaveBeenCalledWith('s1');
  });

  it('shows status message when present', () => {
    render(
      <WorkstreamCard
        workstream={workstream('idle', [], 'creating', 'creating', {
          statusMessage: 'Setting up workstream\u2026',
        })}
      />,
    );
    expect(screen.getByText('Setting up workstream\u2026')).toBeTruthy();
    expect(
      within(screen.getByTestId('workstream-state')).getByText('Creating'),
    ).toBeTruthy();
  });

  // A placeholder stands for a workstream the daemon has not made yet, so there
  // is nothing to count: a zero badge beside "Setting up workstream\u2026" would
  // be stating a fact about something that does not exist.
  it('shows no session badges on a creating placeholder', () => {
    render(
      <WorkstreamCard
        workstream={workstream('idle', [], 'creating', 'creating', {
          statusMessage: 'Setting up workstream\u2026',
        })}
      />,
    );
    expect(screen.getByText('Setting up workstream\u2026')).toBeTruthy();
    expect(screen.queryByTestId('workstream-sessions')).toBeNull();
  });

  // A failed create stands for no workstream: it offers only its removal, and
  // no pill for a strategy it never got.
  it('offers only Delete on a failed create', () => {
    const handle = createRef<WorkstreamCardHandle>();
    render(
      <WorkstreamCard
        ref={handle}
        workstream={workstream('idle', [], 'error', 'error', {
          id: 'creating-1',
          name: 'New workstream',
          provider: 'core',
          providerLabel: undefined,
          strategyId: '',
          strategy: '',
          branch: undefined,
          createFailed: true,
          statusMessage: 'could not create the worktree: invalid reference: x',
          actions: [{ id: 'note', label: 'Note' }],
          openModes: [{ id: 'terminal', label: 'Terminal' }],
        })}
        onOpen={vi.fn()}
        onOpenWith={vi.fn()}
        onAction={vi.fn()}
        onRename={renameOk()}
        onDelete={vi.fn()}
        loadSessionOptions={vi.fn().mockResolvedValue([])}
        onCreateSession={vi.fn()}
      />,
    );
    expect(
      screen.getAllByRole('button').map((button) => button.textContent),
    ).toEqual(['Delete']);
    expect(
      handle.current?.actions('creating-1').map((action) => action.id),
    ).toEqual(['delete']);
    // No pills: the heading's sibling holds them, and it is empty.
    const heading = screen.getByRole('heading', { name: 'New workstream' });
    expect(heading.nextElementSibling?.childElementCount).toBe(0);
    expect(screen.queryByTestId('workstream-sessions')).toBeNull();
  });

  it('wires onPromptSession per session', () => {
    const onPromptSession = vi.fn();
    render(
      <WorkstreamCard
        workstream={workstream('running', [
          {
            id: 's1',
            title: 'Work',
            agent: 'Claude Code',
            state: 'running',
            preview: 'hello',
          },
        ])}
        onPromptSession={onPromptSession}
      />,
    );
    fireEvent.click(
      screen.getByRole('button', { name: /toggle session details/i }),
    );
    const input = screen.getByLabelText(/send text to session/i);
    fireEvent.change(input, { target: { value: 'go' } });
    fireEvent.click(screen.getByRole('button', { name: /^send$/i }));
    expect(onPromptSession).toHaveBeenCalledWith('s1', 'go');
  });

  it('shows a delete action only when onDelete is provided', () => {
    const { rerender } = render(
      <WorkstreamCard workstream={workstream('idle', [])} />,
    );
    expect(
      screen.queryByRole('button', { name: /Delete workstream/ }),
    ).toBeNull();

    const onDelete = vi.fn();
    rerender(
      <WorkstreamCard
        workstream={workstream('idle', [])}
        onDelete={onDelete}
      />,
    );
    fireEvent.click(screen.getByRole('button', { name: /Delete workstream/ }));
    fireEvent.click(
      within(screen.getByRole('dialog')).getByRole('button', {
        name: 'Delete',
      }),
    );
    expect(onDelete).toHaveBeenCalledWith({
      keepFiles: false,
    });
  });

  it('shows an open action only when onOpen is provided, first, with Delete last', () => {
    const onOpen = vi.fn();
    const openable = workstream('idle', [], undefined, undefined, {
      openModes: [
        { id: 'open-with:terminal', label: 'Terminal', default: true },
      ],
    });
    const { rerender } = render(<WorkstreamCard workstream={openable} />);
    expect(
      screen.queryByRole('button', { name: /Open workstream/ }),
    ).toBeNull();

    rerender(
      <WorkstreamCard
        workstream={openable}
        onDelete={vi.fn()}
        onOpen={onOpen}
      />,
    );
    const actions = screen
      .getAllByRole('button')
      .map((button) => button.getAttribute('aria-label') ?? button.textContent);
    expect(actions.indexOf('Open workstream feature/self-update')).toBe(0);
    expect(actions.indexOf('Delete workstream feature/self-update')).toBe(
      actions.length - 1,
    );

    // Opening is not destructive, so it fires straight away with no dialog. The
    // button is the default open and names no mode.
    fireEvent.click(
      screen.getByRole('button', {
        name: 'Open workstream feature/self-update',
      }),
    );
    expect(onOpen).toHaveBeenCalledWith();
    expect(screen.queryByRole('dialog')).toBeNull();
  });

  it('offers one open choice per mode the daemon declared', () => {
    const onOpen = vi.fn();
    const onOpenWith = vi.fn();
    render(
      <WorkstreamCard
        workstream={workstream('idle', [], undefined, undefined, {
          openModes: [
            { id: 'open-with:terminal', label: 'Terminal', default: true },
            { id: 'open-with:e1', label: 'Visual Studio Code' },
          ],
        })}
        onOpen={onOpen}
        onOpenWith={onOpenWith}
      />,
    );

    // The caret beside the default Open button holds the menu of modes.
    fireEvent.click(
      screen.getByRole('button', {
        name: 'Open workstream feature/self-update with…',
      }),
    );
    // The labels are the daemon's; the card renders them as given.
    expect(screen.getByRole('menuitem', { name: 'Terminal' })).toBeTruthy();
    fireEvent.click(
      screen.getByRole('menuitem', { name: 'Visual Studio Code' }),
    );
    expect(onOpenWith).toHaveBeenCalledWith('open-with:e1');
    expect(onOpen).not.toHaveBeenCalled();
  });

  // The plain open would fail (the folder is not on the daemon's host), so the
  // control is a menu of what does work, with no default half.
  it('offers only the menu when no mode is the default open', () => {
    const onOpen = vi.fn();
    const onOpenWith = vi.fn();
    render(
      <WorkstreamCard
        workstream={workstream('idle', [], undefined, undefined, {
          openModes: [
            { id: 'session-shell:s1', label: 'Shell in Work' },
            { id: 'link:web-ide', label: 'Web IDE' },
          ],
        })}
        onOpen={onOpen}
        onOpenWith={onOpenWith}
      />,
    );

    expect(
      screen.queryByRole('button', {
        name: 'Open workstream feature/self-update with…',
      }),
    ).toBeNull();
    fireEvent.click(
      screen.getByRole('button', {
        name: 'Open workstream feature/self-update',
      }),
    );
    fireEvent.click(screen.getByRole('menuitem', { name: 'Shell in Work' }));
    expect(onOpenWith).toHaveBeenCalledWith('session-shell:s1');
    expect(onOpen).not.toHaveBeenCalled();
  });

  it('shows no open action when the daemon declares no modes', () => {
    render(
      <WorkstreamCard
        workstream={workstream('idle', [], undefined, undefined, {
          openModes: [],
        })}
        onOpen={vi.fn()}
      />,
    );
    expect(
      screen.queryByRole('button', { name: /Open workstream/ }),
    ).toBeNull();
  });

  // The card reads the daemon's action list and echoes the ID back; it has no
  // opinion about which actions exist, not even for the note.
  it('runs the action the daemon advertised, by its ID', () => {
    const onAction = vi.fn();
    render(
      <WorkstreamCard
        workstream={workstream('idle', [], undefined, undefined, {
          actions: [{ id: 'note', label: 'Note' }],
        })}
        onAction={onAction}
      />,
    );

    fireEvent.click(
      screen.getByRole('button', { name: /Note for workstream/ }),
    );
    expect(onAction).toHaveBeenCalledWith('note');
  });

  // The daemon authors the words, including what the action will actually do —
  // it is the side that knows, so the card never writes its own copy.
  it('labels an action as the daemon named it', () => {
    render(
      <WorkstreamCard
        workstream={workstream('idle', [], undefined, undefined, {
          actions: [
            {
              id: 'some-action-the-ui-does-not-know',
              label: 'Named By The Daemon',
              description: 'Explained by the daemon',
            },
          ],
        })}
        onAction={vi.fn()}
      />,
    );

    const button = screen.getByRole('button', {
      name: /Named By The Daemon for workstream/,
    });
    expect(button.textContent).toBe('Named By The Daemon');
    // Its letter is the first of its label no other button has, the page's
    // included: N is New and A is Add project.
    expect(button.getAttribute('title')).toBe(
      'Explained by the daemon (Alt+M)',
    );
  });

  // One row of controls, in the order every item shares: opening first, then
  // making something in it, then what the daemon advertises, and what deletes
  // last. The footer they used to sit in cost the card a line of its own.
  it('places the actions in the header, before Delete', () => {
    render(
      <WorkstreamCard
        workstream={workstream(
          'running',
          [{ id: 's1', title: 'Work', agent: 'Claude Code', state: 'running' }],
          undefined,
          undefined,
          {
            actions: [{ id: 'note', label: 'Notes' }],
            openModes: [{ id: 'terminal', label: 'Terminal', default: true }],
          },
        )}
        onOpen={vi.fn()}
        onOpenWith={vi.fn()}
        onAction={vi.fn()}
        onRename={renameOk()}
        onDelete={vi.fn()}
        loadSessionOptions={vi.fn().mockResolvedValue([])}
        onCreateSession={vi.fn()}
      />,
    );

    const header = screen
      .getByRole('heading', { name: 'feature/self-update' })
      .closest('header') as HTMLElement;
    const controls = within(header)
      .getAllByRole('button')
      .filter((button) => button.tabIndex !== -1)
      .map((button) => button.textContent);
    expect(controls).toEqual(['Open', '▾', 'New session', 'Notes', 'Delete']);
  });

  // The daemon advertises its actions by label, so their letters are picked
  // from it: the first the card's own (O, N, R, D) leave free.
  it('gives an advertised action a letter of its label the others leave', () => {
    render(
      <WorkstreamCard
        workstream={workstream('idle', [], undefined, undefined, {
          actions: [{ id: 'note', label: 'Notes' }],
        })}
        onAction={vi.fn()}
      />,
    );
    const notes = screen.getByRole('button', { name: /Notes for workstream/ });
    expect(notes.getAttribute('data-mnemonic')).toBe('t');
    expect(notes.getAttribute('aria-keyshortcuts')).toBe('Alt+T');
  });

  it('shows no advertised buttons when the daemon offers no actions', () => {
    render(
      <WorkstreamCard
        workstream={workstream('idle', [], undefined, undefined, {
          actions: [],
        })}
        onAction={vi.fn()}
      />,
    );
    expect(screen.queryByRole('button', { name: /for workstream/ })).toBeNull();
  });

  it('resets the delete confirmation when the workstream changes', () => {
    const onDelete = vi.fn();
    const { rerender } = render(
      <WorkstreamCard
        workstream={workstream('idle', [])}
        onDelete={onDelete}
      />,
    );
    fireEvent.click(screen.getByRole('button', { name: /Delete workstream/ }));
    expect(screen.getByRole('dialog')).toBeTruthy();
    // Switching to a different workstream (new id) closes the dialog.
    rerender(
      <WorkstreamCard
        workstream={{ ...workstream('idle', []), id: 'ws-2', name: 'other' }}
        onDelete={onDelete}
      />,
    );
    expect(screen.queryByRole('dialog')).toBeNull();
  });

  it('makes the name the way to rename only when onRename is provided', () => {
    const { rerender } = render(
      <WorkstreamCard workstream={workstream('idle', [])} />,
    );
    expect(cardName().hasAttribute('data-mnemonic')).toBe(false);
    renameByName();
    expect(screen.queryByLabelText('Workstream name')).toBeNull();
    rerender(
      <WorkstreamCard
        workstream={workstream('idle', [])}
        onRename={renameOk()}
      />,
    );
    expect(cardName().getAttribute('data-mnemonic')).toBe('r');
    expect(cardName().getAttribute('aria-keyshortcuts')).toBe('Alt+R');
    expect(cardName().getAttribute('title')).toBe(
      'feature/self-update\n\nDouble-click to rename',
    );
  });

  // F2 reaches a card only where it can take focus.
  it('names F2 in the tooltip only on a card that can take focus', () => {
    const ws = workstream('idle', []);
    render(
      <WorkstreamCard
        workstream={ws}
        onRename={renameOk()}
        tabStopId={ws.id}
      />,
    );
    expect(cardName().getAttribute('title')).toBe(
      'feature/self-update\n\nDouble-click to rename (or press F2)',
    );
  });

  // The name is the rename affordance; there is no control beside it.
  it('has no Rename button', () => {
    render(
      <WorkstreamCard
        workstream={workstream('idle', [])}
        onRename={renameOk()}
      />,
    );
    expect(screen.queryByRole('button', { name: /^Rename/ })).toBeNull();
  });

  it('hides Rename while the workstream is still being created', () => {
    // A `creating-` placeholder is replaced wholesale by the real workstream, so
    // a rename here would be dropped and the PATCH would 404.
    render(
      <WorkstreamCard
        workstream={workstream('idle', [], 'creating', 'starting', {
          id: 'creating-1',
        })}
        onRename={renameOk()}
      />,
    );
    expect(cardName().hasAttribute('data-mnemonic')).toBe(false);
    renameByName();
    expect(screen.queryByLabelText('Workstream name')).toBeNull();
  });

  it('renames on Enter with the edited value', async () => {
    const onRename = renameOk();
    render(
      <WorkstreamCard
        workstream={workstream('idle', [])}
        onRename={onRename}
      />,
    );
    renameByName();
    const input = screen.getByLabelText('Workstream name');
    fireEvent.change(input, { target: { value: 'renamed-ws' } });
    fireEvent.keyDown(input, { key: 'Enter' });
    expect(onRename).toHaveBeenCalledWith('renamed-ws');
    expect(onRename).toHaveBeenCalledTimes(1);
    await waitFor(() =>
      expect(screen.queryByLabelText('Workstream name')).toBeNull(),
    );
  });

  it('renames on blur with the edited value', async () => {
    const onRename = renameOk();
    render(
      <WorkstreamCard
        workstream={workstream('idle', [])}
        onRename={onRename}
      />,
    );
    renameByName();
    const input = screen.getByLabelText('Workstream name');
    fireEvent.change(input, { target: { value: 'blurred-name' } });
    fireEvent.blur(input);
    expect(onRename).toHaveBeenCalledWith('blurred-name');
    expect(onRename).toHaveBeenCalledTimes(1);
    await waitFor(() =>
      expect(screen.queryByLabelText('Workstream name')).toBeNull(),
    );
  });

  // The name that started it is not focusable, so focus goes back to the card
  // it renamed, as for F2.
  it('cancels on Escape without calling onRename, restoring focus', async () => {
    const onRename = renameOk();
    render(
      <WorkstreamCard
        workstream={workstream('idle', [])}
        onRename={onRename}
        tabStopId="ws-1"
      />,
    );
    renameByName();
    const input = screen.getByLabelText('Workstream name');
    fireEvent.change(input, { target: { value: 'discard-me' } });
    fireEvent.keyDown(input, { key: 'Escape' });
    expect(onRename).not.toHaveBeenCalled();
    expect(screen.queryByLabelText('Workstream name')).toBeNull();
    expect(
      screen.getByRole('heading', { name: 'feature/self-update' }),
    ).toBeTruthy();
    // The input unmounts, so without an explicit restore focus falls to <body>.
    await waitFor(() =>
      expect(document.activeElement).toBe(
        screen.getByRole('region', { name: /^Workstream / }),
      ),
    );
  });

  it('keeps the field, the draft and an error when the rename fails', async () => {
    const onRename = vi.fn(async () => 'Rename failed.');
    render(
      <WorkstreamCard
        workstream={workstream('idle', [])}
        onRename={onRename}
      />,
    );
    renameByName();
    const input = screen.getByLabelText('Workstream name');
    fireEvent.change(input, { target: { value: 'rejected-name' } });
    fireEvent.keyDown(input, { key: 'Enter' });

    expect((await screen.findByRole('alert')).textContent).toBe(
      'Rename failed.',
    );
    const stillOpen = screen.getByLabelText('Workstream name');
    expect((stillOpen as HTMLInputElement).value).toBe('rejected-name');
  });

  // The reason was about the name that was refused; once that is being edited
  // it no longer describes anything on screen.
  it("drops a refused rename's error as the name is edited", async () => {
    render(
      <WorkstreamCard
        workstream={workstream('idle', [])}
        onRename={vi.fn(async () => 'Rename failed.')}
      />,
    );
    renameByName();
    const input = screen.getByLabelText('Workstream name');
    fireEvent.change(input, { target: { value: 'rejected-name' } });
    fireEvent.keyDown(input, { key: 'Enter' });
    await screen.findByRole('alert');

    fireEvent.change(input, { target: { value: 'better-name' } });
    expect(screen.queryByRole('alert')).toBeNull();
  });

  it('renames on a double-click on the name', () => {
    render(
      <WorkstreamCard
        workstream={workstream('idle', [])}
        onRename={renameOk()}
      />,
    );
    const name = screen.getByRole('heading', { name: 'feature/self-update' });
    expect(name.getAttribute('title')).toMatch(/Double-click to rename/);
    fireEvent.doubleClick(name);
    expect(screen.getByLabelText('Workstream name')).toBeTruthy();
  });

  it('does not rename on a double-click while blocked', () => {
    render(
      <WorkstreamCard
        workstream={workstream('idle', [])}
        onRename={renameOk()}
        disabledReason="Daemon offline"
      />,
    );
    const name = screen.getByRole('heading', { name: 'feature/self-update' });
    expect(name.getAttribute('title')).toBe(
      'feature/self-update\n\nDaemon offline',
    );
    expect(name.hasAttribute('data-mnemonic')).toBe(false);
    fireEvent.doubleClick(name);
    expect(screen.queryByLabelText('Workstream name')).toBeNull();
  });

  it.each([
    ['renames', undefined, true],
    ['does not rename while blocked', 'Daemon offline', false],
  ])('on Alt+R with the card selected, %s', (_what, disabledReason, renames) => {
    render(
      <>
        <Mnemonics />
        <WorkstreamCard
          workstream={workstream('idle', [])}
          onRename={renameOk()}
          disabledReason={disabledReason}
          tabStopId="ws-1"
          selectedId="ws-1"
        />
      </>,
    );
    fireEvent.keyDown(window, { key: 'r', code: 'KeyR', altKey: true });
    expect(Boolean(screen.queryByLabelText('Workstream name'))).toBe(renames);
  });

  it("gives the delete dialog's buttons their letters", () => {
    render(
      <WorkstreamCard workstream={workstream('idle', [])} onDelete={vi.fn()} />,
    );
    fireEvent.click(screen.getByRole('button', { name: /^Delete workstream/ }));
    const dialog = screen.getByRole('dialog');
    const letters = within(dialog)
      .getAllByRole('button')
      .map((button) => [
        button.textContent,
        button.getAttribute('data-mnemonic'),
      ]);
    expect(letters).toEqual([
      ['Cancel', 'c'],
      ['Keep files', 'k'],
      ['Delete', 'd'],
    ]);
  });

  // Enter asked for focus back, but the box stayed open; the blur that later
  // commits it must not pull focus back from where the user went.
  it('takes no focus back for an Enter refused while offline', async () => {
    const onRename = renameOk();
    const shown = (disabledReason?: string) => (
      <>
        <WorkstreamCard
          workstream={workstream('idle', [])}
          onRename={onRename}
          disabledReason={disabledReason}
        />
        <button type="button">elsewhere</button>
      </>
    );
    const { rerender } = render(shown());
    renameByName();
    const input = screen.getByLabelText('Workstream name');
    act(() => input.focus());
    fireEvent.change(input, { target: { value: 'renamed-ws' } });
    rerender(shown('Offline'));
    fireEvent.keyDown(input, { key: 'Enter' });
    expect(onRename).not.toHaveBeenCalled();

    rerender(shown());
    const elsewhere = screen.getByRole('button', { name: 'elsewhere' });
    await act(async () => elsewhere.focus());

    await waitFor(() => expect(onRename).toHaveBeenCalledWith('renamed-ws'));
    expect(screen.queryByLabelText('Workstream name')).toBeNull();
    expect(document.activeElement).toBe(elsewhere);
  });

  it('does not rename to an empty or unchanged name', () => {
    const onRename = renameOk();
    render(
      <WorkstreamCard
        workstream={workstream('idle', [])}
        onRename={onRename}
      />,
    );
    // Empty.
    renameByName();
    let input = screen.getByLabelText('Workstream name');
    fireEvent.change(input, { target: { value: '   ' } });
    fireEvent.keyDown(input, { key: 'Enter' });
    expect(onRename).not.toHaveBeenCalled();
    // Unchanged.
    renameByName();
    input = screen.getByLabelText('Workstream name');
    fireEvent.keyDown(input, { key: 'Enter' });
    expect(onRename).not.toHaveBeenCalled();
  });
});

// A provider's failure lands on the status message and nothing overwrites it
// until the next create, so the card offers a way to say "read it, thanks".
describe('dismissing the status message', () => {
  const failed = (message: string) =>
    workstream('idle', [], undefined, undefined, { statusMessage: message });

  it('offers a dismiss button that reports the workstream it belongs to', () => {
    const onDismissStatus = vi.fn();
    render(
      <WorkstreamCard
        workstream={failed('start enclave container: exit status 1')}
        onDismissStatus={onDismissStatus}
      />,
    );

    fireEvent.click(
      screen.getByRole('button', { name: /Dismiss message for workstream/ }),
    );
    expect(onDismissStatus).toHaveBeenCalledTimes(1);
  });

  // Dismissal is the daemon's to perform (the message is daemon state), so the
  // card must not hide it locally on click — it waits for the state push.
  it('leaves the message on screen until the daemon clears it', () => {
    const { rerender } = render(
      <WorkstreamCard
        workstream={failed('a long provider error')}
        onDismissStatus={vi.fn()}
      />,
    );
    fireEvent.click(
      screen.getByRole('button', { name: /Dismiss message for workstream/ }),
    );
    expect(screen.getByText('a long provider error')).toBeTruthy();

    // …and it goes when the cleared state arrives.
    rerender(
      <WorkstreamCard workstream={failed('')} onDismissStatus={vi.fn()} />,
    );
    expect(screen.queryByText('a long provider error')).toBeNull();
  });

  // No handler means no affordance, the same rule the card's other actions follow.
  it('shows no dismiss button without a handler', () => {
    render(<WorkstreamCard workstream={failed('some message')} />);
    expect(screen.getByText('some message')).toBeTruthy();
    expect(
      screen.queryByRole('button', { name: /Dismiss message for workstream/ }),
    ).toBeNull();
  });

  // A failed creation is the other kind of message: the workstream itself is in
  // error and the message is the only account of why. Dismissing it would leave a
  // permanently red card explaining nothing, so the card does not offer it — the
  // way out of a failed creation is to remove it. The daemon refuses this too (409).
  it('shows no dismiss button on a workstream in error state', () => {
    render(
      <WorkstreamCard
        workstream={workstream('idle', [], 'error', 'error', {
          statusMessage: 'branch "main" is checked out in another worktree',
        })}
        onDismissStatus={vi.fn()}
      />,
    );

    expect(
      screen.getByText('branch "main" is checked out in another worktree'),
    ).toBeTruthy();
    expect(
      screen.queryByRole('button', { name: /Dismiss message for workstream/ }),
    ).toBeNull();
  });

  // Nothing to dismiss when there is no message.
  it('shows no dismiss button when there is no status message', () => {
    render(
      <WorkstreamCard
        workstream={workstream('idle', [])}
        onDismissStatus={vi.fn()}
      />,
    );
    expect(
      screen.queryByRole('button', { name: /Dismiss message for workstream/ }),
    ).toBeNull();
  });
});

// Offline handling: the card's own actions and the rows it renders all need the
// daemon, so one reason disables the lot while leaving them visible.
describe('WorkstreamCard when the daemon is unreachable', () => {
  const REASON = 'The daemon is offline';

  const renderBlocked = () =>
    render(
      <WorkstreamCard
        workstream={workstream(
          'running',
          [{ id: 's1', title: 'Work', agent: 'Claude Code', state: 'running' }],
          undefined,
          undefined,
          {
            statusMessage: 'something happened',
            // Without a mode the Open menu renders nothing at all, so there would
            // be no disabled trigger to assert on.
            openModes: [{ id: 'terminal', label: 'Terminal', default: true }],
            // The note is offered only where the daemon says the files are on
            // its host, so it has to advertise the action for one to render.
            actions: [{ id: 'note', label: 'Note' }],
          },
        )}
        onDelete={vi.fn()}
        onOpen={vi.fn()}
        onAction={vi.fn()}
        onDismissStatus={vi.fn()}
        onStopSession={vi.fn()}
        onRemoveSession={vi.fn()}
        onRenameSession={vi.fn()}
        loadSessionOptions={vi.fn().mockResolvedValue([])}
        onCreateSession={vi.fn()}
        disabledReason={REASON}
      />,
    );

  it('disables its own workstream actions', () => {
    renderBlocked();
    for (const name of [
      /^New session$/,
      /^Delete workstream/,
      // Both halves of the Open split button: the default open and the modes menu.
      /^Open workstream [^ ]+$/,
      /^Open workstream .+ with…$/,
      /^Note for workstream/,
      /^Dismiss message for workstream/,
    ]) {
      const button = screen.getByRole('button', { name }) as HTMLButtonElement;
      expect(button.disabled).toBe(true);
      expect(button.getAttribute('title')).toBe(REASON);
    }
  });

  it('passes the reason down to its session rows', () => {
    renderBlocked();
    expect(
      screen
        .getByRole('button', { name: 'Stop' })
        .getAttribute('aria-disabled'),
    ).toBe('true');
    expect(
      (
        screen.getByRole('button', {
          name: /^Delete session/,
        }) as HTMLButtonElement
      ).disabled,
    ).toBe(true);
    // Rename has no button; the row's gesture is what must be inert.
    fireEvent.doubleClick(screen.getByTestId('session-title'));
    expect(screen.queryByLabelText('Session name')).toBeNull();
  });

  it('leaves them all enabled with no reason set', () => {
    render(
      <WorkstreamCard
        workstream={workstream('running', [
          { id: 's1', title: 'Work', agent: 'Claude Code', state: 'running' },
        ])}
        onDelete={vi.fn()}
        onOpen={vi.fn()}
        onAction={vi.fn()}
        onStopSession={vi.fn()}
        loadSessionOptions={vi.fn().mockResolvedValue([])}
        onCreateSession={vi.fn()}
      />,
    );
    for (const name of [/^New session$/, /^Delete workstream/, /^Stop$/]) {
      expect(
        (screen.getByRole('button', { name }) as HTMLButtonElement).disabled,
      ).toBe(false);
    }
  });
});

describe('WorkstreamCard as a keyboard list item', () => {
  const sessions: Workstream['sessions'] = [
    { id: 's1', title: 'Implement', agent: 'Claude Code', state: 'running' },
    { id: 's2', title: 'Verify', agent: 'Claude Code', state: 'running' },
  ];
  const card = () =>
    screen.getByRole('region', { name: 'Workstream feature/self-update' });

  /** A card with no sessions, focused as the list's tab stop. */
  const renderNavigable = (
    props: Partial<Parameters<typeof WorkstreamCard>[0]> = {},
  ) => {
    render(
      <WorkstreamCard
        workstream={workstream('idle', [])}
        tabStopId="ws-1"
        {...props}
      />,
    );
    card().focus();
    return card();
  };

  it('gives the tab stop to the named item and -1 to the others', () => {
    render(
      <WorkstreamCard
        workstream={workstream('idle', sessions)}
        tabStopId="s2"
      />,
    );
    const rows = screen.getAllByRole('listitem');
    expect(card().getAttribute('tabindex')).toBe('-1');
    expect(rows.map((row) => row.getAttribute('tabindex'))).toEqual([
      '-1',
      '0',
    ]);
  });

  // Only the card holding the tab stop keeps its controls in the Tab order.
  it.each([
    ['s2', '-1'],
    ['ws-1', null],
  ])(
    'with the tab stop on %s, gives its controls tab index %s',
    (id, expected) => {
      render(
        <WorkstreamCard
          workstream={workstream('idle', sessions)}
          tabStopId={id}
          onRename={renameOk()}
          onDelete={vi.fn()}
          onOpen={vi.fn()}
        />,
      );
      const header = within(card()).getByRole('banner');
      for (const control of within(header).getAllByRole('button')) {
        expect(control.getAttribute('tabindex')).toBe(expected);
      }
    },
  );

  it('is not focusable without a list around it', () => {
    render(<WorkstreamCard workstream={workstream('idle', sessions)} />);
    expect(card().hasAttribute('tabindex')).toBe(false);
    expect(screen.getAllByRole('listitem')[0].hasAttribute('tabindex')).toBe(
      false,
    );
  });

  it('opens on Enter, once for a held key', () => {
    const onOpen = vi.fn();
    const focused = renderNavigable({
      onOpen,
      workstream: workstream('idle', [], undefined, undefined, {
        openModes: [{ id: 'terminal', label: 'Terminal', default: true }],
      }),
    });
    fireEvent.keyDown(focused, { key: 'Enter' });
    fireEvent.keyDown(focused, { key: 'Enter', repeat: true });
    expect(onOpen).toHaveBeenCalledTimes(1);
  });

  // Without a default mode the plain open would fail, so Enter has none to run.
  it('does not open on Enter without a default mode', () => {
    const onOpen = vi.fn();
    const focused = renderNavigable({
      onOpen,
      workstream: workstream('idle', [], undefined, undefined, {
        openModes: [{ id: 'terminal', label: 'Terminal' }],
      }),
    });
    fireEvent.keyDown(focused, { key: 'Enter' });
    expect(onOpen).not.toHaveBeenCalled();
  });

  it('asks before deleting on Delete', () => {
    const onDelete = vi.fn();
    fireEvent.keyDown(renderNavigable({ onDelete }), { key: 'Delete' });
    expect(screen.getByRole('dialog')).toBeTruthy();
    expect(onDelete).not.toHaveBeenCalled();
  });

  // Without confirmation, a held Delete would go on to the next item.
  it('deletes once for a held Delete', () => {
    const onDelete = vi.fn();
    const focused = renderNavigable({
      workstream: workstream('idle', [], undefined, undefined, {
        strategyId: 'in-place',
      }),
      onDelete,
      skipConfirmation: true,
    });
    fireEvent.keyDown(focused, { key: 'Delete' });
    fireEvent.keyDown(focused, { key: 'Delete', repeat: true });
    expect(onDelete).toHaveBeenCalledTimes(1);
  });

  // A click focuses the card too, but with no ring to show it is selected.
  it('ignores its keys when focus came from a click', () => {
    const onOpen = vi.fn();
    const onDelete = vi.fn();
    const focused = renderNavigable({ onOpen, onDelete });
    focused.blur();
    fireEvent.pointerDown(focused);
    focused.focus();
    fireEvent.keyDown(focused, { key: 'Enter' });
    fireEvent.keyDown(focused, { key: 'Delete' });
    expect(onOpen).not.toHaveBeenCalled();
    expect(screen.queryByRole('dialog')).toBeNull();
  });

  it('renames on F2 and gives focus back to the card on Escape', async () => {
    const focused = renderNavigable({ onRename: renameOk() });
    fireEvent.keyDown(focused, { key: 'F2' });
    fireEvent.keyDown(screen.getByLabelText('Workstream name'), {
      key: 'Escape',
    });
    await waitFor(() => expect(document.activeElement).toBe(card()));
  });

  it('does not rename on F2 while blocked', () => {
    const focused = renderNavigable({
      onRename: renameOk(),
      disabledReason: 'Offline',
    });
    fireEvent.keyDown(focused, { key: 'F2' });
    expect(screen.queryByLabelText('Workstream name')).toBeNull();
  });

  // Enter on a session row inside is the row's, not an Open of the workstream.
  it('ignores keys pressed on the items inside it', () => {
    const onOpen = vi.fn();
    renderNavigable({ workstream: workstream('idle', sessions), onOpen });
    const row = screen.getAllByRole('listitem')[0];
    row.focus();
    fireEvent.keyDown(row, { key: 'Enter' });
    expect(onOpen).not.toHaveBeenCalled();
  });

  it('leaves a modified Enter to the browser', () => {
    const onOpen = vi.fn();
    fireEvent.keyDown(renderNavigable({ onOpen }), {
      key: 'Enter',
      ctrlKey: true,
    });
    expect(onOpen).not.toHaveBeenCalled();
  });

  it('tells a screen reader its keys, and those of its rows', () => {
    renderNavigable({ workstream: workstream('idle', sessions) });
    const row = screen.getAllByRole('listitem')[0];
    expect(card().getAttribute('aria-keyshortcuts')).toBe(
      'Enter F2 Delete Shift+F10',
    );
    expect(row.getAttribute('aria-keyshortcuts')).toBe(
      'ArrowRight ArrowLeft Enter F2 Delete Shift+F10',
    );
  });

  it.each([
    ['itself', 'ws-1', 0],
    ['a row', 's1', 1],
  ])('marks the selection as current when it is %s', (_which, id, index) => {
    renderNavigable({
      workstream: workstream('idle', sessions),
      selectedId: id,
    });
    const items = [card(), screen.getAllByRole('listitem')[0]];
    expect(items.map((item) => item.getAttribute('aria-current'))).toEqual(
      items.map((_item, i) => (i === index ? 'true' : null)),
    );
  });

  it('does nothing while the daemon is unreachable', () => {
    const onOpen = vi.fn();
    fireEvent.keyDown(renderNavigable({ onOpen, disabledReason: 'Offline' }), {
      key: 'Enter',
    });
    expect(onOpen).not.toHaveBeenCalled();
  });
});

describe('WorkstreamCard restart with settings', () => {
  const sessions: Workstream['sessions'] = [
    {
      id: 's-1',
      title: 'One',
      agent: 'Claude Code',
      state: 'running',
      provider: 'enclave',
      restartable: true,
    },
    {
      id: 's-2',
      title: 'Two',
      agent: 'Claude Code',
      state: 'running',
      provider: 'local',
    },
  ];

  it('offers it only on the sessions the daemon marks restartable', () => {
    const handle = createRef<WorkstreamCardHandle>();
    const onRestartSession = vi.fn();
    render(
      <WorkstreamCard
        ref={handle}
        workstream={workstream('running', sessions)}
        onRestartSession={onRestartSession}
      />,
    );
    const restartOf = (id: string) =>
      handle.current
        ?.actions(id)
        .find((action) => action.id === 'restartWithSettings');
    expect(restartOf('s-2')).toBeUndefined();
    restartOf('s-1')?.run();
    expect(onRestartSession).toHaveBeenCalledWith('s-1');
  });
});
