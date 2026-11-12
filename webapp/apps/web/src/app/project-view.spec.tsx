import { useMnemonics } from '@homeshell/components';
import type { Project } from '@homeshell/model';
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react';
import type { ReactElement } from 'react';
import { vi } from 'vitest';

import { registryProbe } from '../testing/contributes';
import { CommandsProvider } from './keyboard/commands';
import { ProjectView } from './project-view';
import { SettingsProvider } from './settings/use-settings';

/** Gives the page Alt's letters, as the app does. */
function Mnemonics() {
  useMnemonics();
  return null;
}

/**
 * The removal guard as the browser meets it: the daemon refuses a delete with a
 * 409, the view turns that into a second confirmation naming the sessions, and
 * confirming re-issues the same delete with force. What is asserted here is the
 * mapping from a daemon response to what the user sees and what is sent next —
 * the e2e spec drives the same flow against a real daemon, but cannot easily
 * produce a malformed 409 or a daemon that is simply unreachable.
 */

const project: Project = {
  id: 'p1',
  name: 'alpha',
  path: '/tmp/alpha',
  gitBacked: true,
  state: 'running',
  runtimeState: 'running',
  effectiveState: 'running',
  untrackedWorktrees: [],
  workstreams: [
    {
      id: 'w1',
      name: 'feature/x',
      provider: 'Fake',
      // in-place: the first dialog asks a single yes/no, with no files question.
      strategyId: 'in-place',
      strategy: 'in place',
      inProjectFolder: true,
      state: 'running',
      runtimeState: 'running',
      effectiveState: 'running',
      sessions: [
        {
          id: 's1',
          title: 'Implement',
          agent: 'Claude Code',
          state: 'running',
        },
      ],
    },
  ],
};

/** The 409 the daemon answers with while sessions are busy. */
const busyBody = {
  error: 'cannot remove the workstream while a session is still busy',
  reason: 'sessionsRunning',
  sessions: [{ id: 's1', title: 'Implement', state: 'running' }],
};

/**
 * Answers settings loads so the provider settles, and hands every DELETE the
 * queued responses in order. Returns the recorded DELETE calls.
 */
function stubDaemon(...deletes: { status: number; body?: unknown }[]) {
  const calls: { url: string; body: unknown }[] = [];
  const queue = [...deletes];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init?: RequestInit) => {
      if (init?.method === 'DELETE') {
        calls.push({
          url,
          body: init.body ? JSON.parse(String(init.body)) : undefined,
        });
        const next = queue.shift() ?? { status: 204 };
        return {
          ok: next.status < 400,
          status: next.status,
          json: async () => next.body ?? {},
        };
      }
      // Settings and anything else the view loads.
      return { ok: true, status: 200, json: async () => ({}) };
    }),
  );
  return calls;
}

const view = (shown: Project, disabledReason?: string) => (
  <SettingsProvider>
    <CommandsProvider>
      <ProjectView project={shown} disabledReason={disabledReason} />
      <probe.Probe />
    </CommandsProvider>
  </SettingsProvider>
);

/** The command registry the rendered view contributes to. */
const probe = registryProbe();
const registry = probe.registry;

const card = (
  id: string,
  name: string,
  sessions: Project['workstreams'][number]['sessions'] = [],
  placeholderId?: string,
) => ({
  ...project.workstreams[0],
  id,
  name,
  sessions,
  placeholderId,
});
const worktree = (
  id: string,
  name: string,
  sessions = card(id, name).sessions,
) => ({
  ...card(id, name, sessions),
  strategyId: 'worktree',
  strategy: 'worktree',
  inProjectFolder: false,
  workingDir: `/tmp/alpha-${name}`,
});
const withCards = (...workstreams: Project['workstreams']): Project => ({
  ...project,
  workstreams,
});
const session = (
  id: string,
  state: 'running' | 'idle' | 'stopped' = 'running',
) => ({
  id,
  title: id,
  agent: 'Claude Code',
  provider: 'fake',
  state,
});

/**
 * Answers what the view loads — one creating provider, one session option,
 * and a provider that can stop and start — and each create with `created`,
 * once `answered` has settled, or refuses each with `refusal`.
 */
function stubCreates(
  created: () => unknown,
  answered = Promise.resolve(),
  refusal?: string,
) {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init?: RequestInit) => {
      const json = (body: unknown, status = 200) => ({
        ok: true,
        status,
        json: async () => body,
      });
      // No draft: the form keeps what it was opened with.
      if (url.endsWith('/workstream-draft')) {
        return { ok: false, status: 404, json: async () => ({}) };
      }
      if (init?.method === 'POST') {
        await answered;
        if (refusal) {
          return {
            ok: false,
            status: 409,
            json: async () => ({ error: refusal }),
          };
        }
        return json(created(), 201);
      }
      if (url.endsWith('/workstream-providers')) {
        return json([{ id: 'fake', label: 'Fake', workstreamSchema: {} }]);
      }
      if (url.endsWith('/session-schemas')) {
        return json([
          {
            providerId: 'fake',
            label: 'Fake',
            options: [{ id: 'claude', label: 'Claude Code' }],
          },
        ]);
      }
      if (url === '/api/providers') {
        return json([{ id: 'fake', capabilities: { sessionStartStop: true } }]);
      }
      return json({});
    }),
  );
}

async function renderLive(shown: Project) {
  let rerender: (ui: ReactElement) => void = () => undefined;
  await act(async () => {
    ({ rerender } = render(view(shown)));
  });
  return (next: Project) => act(async () => rerender(view(next)));
}

/** Renders the view and gets as far as the daemon's first answer to a delete. */
async function deleteWorkstream() {
  render(view(project));
  fireEvent.click(
    await screen.findByRole('button', { name: 'Delete workstream feature/x' }),
  );
  fireEvent.click(await screen.findByRole('button', { name: 'Delete' }));
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('ProjectView sections', () => {
  // Both sections are in the one list the arrow keys walk.
  const section = (name: RegExp) =>
    within(screen.getByRole('group', { name: 'Workstreams' })).getByRole(
      'region',
      { name },
    );
  const cardsIn = (region: HTMLElement) =>
    within(region)
      .queryAllByRole('region', { name: /^Workstream / })
      .map((shown) => shown.getAttribute('aria-label'));

  it('shows the project folder first, then the separate folders in daemon order', async () => {
    stubDaemon();
    await renderLive(
      withCards(worktree('w2', 'b'), card('w1', 'here'), worktree('w3', 'a')),
    );

    expect(cardsIn(section(/^In the project folder/))).toEqual([
      'Workstream here',
    ]);
    // The folder is in use: nothing to offer in it.
    expect(screen.queryByRole('button', { name: 'Work here' })).toBeNull();
    const separate = section(/^Separate folders · 2$/);
    expect(cardsIn(separate)).toEqual(['Workstream b', 'Workstream a']);
    expect(
      within(separate).getByRole('button', { name: 'New workstream' }),
    ).toBeTruthy();
  });

  it('says when either section is empty', async () => {
    stubDaemon();
    await renderLive(withCards());

    expect(
      within(section(/^In the project folder/)).getByText(
        'No workstream in the project folder.',
      ),
    ).toBeTruthy();
    expect(
      within(section(/^Separate folders/)).getByText(
        'No separate folders yet.',
      ),
    ).toBeTruthy();
  });

  it('creates the in-place workstream on Work here, once, busy meanwhile, and focuses it', async () => {
    let answer: () => void = () => undefined;
    stubCreates(
      () => card('w1', 'here'),
      new Promise<void>((resolve) => {
        answer = resolve;
      }),
    );
    const rerender = await renderLive(withCards(worktree('w2', 'b')));

    const workHere = screen.getByRole('button', { name: 'Work here' });
    fireEvent.click(workHere);
    fireEvent.click(workHere);
    expect(workHere.getAttribute('aria-busy')).toBe('true');
    await act(async () => answer());
    await rerender(withCards(card('w1', 'here'), worktree('w2', 'b')));

    const posts = vi
      .mocked(fetch)
      .mock.calls.filter(([, init]) => init?.method === 'POST');
    expect(posts).toHaveLength(1);
    expect(document.activeElement).toBe(
      screen.getByRole('region', { name: 'Workstream here' }),
    );
  });

  it('says why the daemon refused Work here', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (_url: string, init?: RequestInit) =>
        init?.method === 'POST'
          ? {
              ok: false,
              status: 409,
              json: async () => ({ error: 'The folder is taken.' }),
            }
          : { ok: true, status: 200, json: async () => ({}) },
      ),
    );
    await renderLive(withCards(worktree('w2', 'b')));

    fireEvent.click(screen.getByRole('button', { name: 'Work here' }));

    expect((await screen.findByRole('alert')).textContent).toContain(
      'The folder is taken.',
    );
  });

  describe('untracked worktrees', () => {
    const untracked = (shown: Project): Project => ({
      ...shown,
      untrackedWorktrees: [
        { name: 'wt-a', branch: 'wt-a', path: '/tmp/wt-a', sessionCount: 0 },
      ],
    });

    it('shows them at the end of Separate folders, outside its count', async () => {
      stubDaemon();
      await renderLive(untracked(withCards(worktree('w2', 'b'))));

      const separate = section(/^Separate folders · 1$/);
      const fold = within(separate).getByRole('button', {
        name: '1 untracked worktree',
      });
      const cardRegion = within(separate).getByRole('region', {
        name: 'Workstream b',
      });
      expect(
        cardRegion.compareDocumentPosition(fold) &
          Node.DOCUMENT_POSITION_FOLLOWING,
      ).toBeTruthy();
    });

    it('posts the path of the one added', async () => {
      stubCreates(() => worktree('w3', 'wt-a'));
      await renderLive(untracked(withCards(worktree('w2', 'b'))));

      fireEvent.click(
        screen.getByRole('button', { name: '1 untracked worktree' }),
      );
      fireEvent.click(screen.getByRole('button', { name: 'Add wt-a' }));

      const post = await waitFor(() => {
        const found = vi
          .mocked(fetch)
          .mock.calls.find(([, init]) => init?.method === 'POST');
        expect(found).toBeDefined();
        return found!;
      });
      expect(post[0]).toBe('/api/projects/p1/worktree-workstreams');
      expect(JSON.parse(String(post[1]?.body))).toEqual({ path: '/tmp/wt-a' });
    });

    it('says why the daemon refused an add', async () => {
      stubCreates(() => ({}), undefined, 'Not an untracked worktree.');
      await renderLive(untracked(withCards(worktree('w2', 'b'))));

      fireEvent.click(
        screen.getByRole('button', { name: '1 untracked worktree' }),
      );
      fireEvent.click(screen.getByRole('button', { name: 'Add wt-a' }));

      expect((await screen.findByRole('alert')).textContent).toContain(
        'Not an untracked worktree.',
      );
    });

    it('lets no second add or Work here start while one is in flight', async () => {
      let answer: () => void = () => undefined;
      stubCreates(
        () => worktree('w3', 'wt-a'),
        new Promise<void>((resolve) => {
          answer = resolve;
        }),
      );
      await renderLive(untracked(withCards(worktree('w2', 'b'))));

      fireEvent.click(
        screen.getByRole('button', { name: '1 untracked worktree' }),
      );
      const add = screen.getByRole('button', { name: 'Add wt-a' });
      fireEvent.click(add);
      fireEvent.click(add);
      expect(add.getAttribute('aria-busy')).toBe('true');
      await act(async () => answer());

      expect(
        vi
          .mocked(fetch)
          .mock.calls.filter(([, init]) => init?.method === 'POST'),
      ).toHaveLength(1);
    });

    it('blocks the adds while Work here is in flight', async () => {
      stubCreates(() => card('w1', 'here'), new Promise<void>(() => undefined));
      await renderLive(untracked(withCards()));

      fireEvent.click(screen.getByRole('button', { name: 'Work here' }));
      fireEvent.click(
        screen.getByRole('button', { name: '1 untracked worktree' }),
      );
      fireEvent.click(screen.getByRole('button', { name: 'Add wt-a' }));

      expect(
        vi
          .mocked(fetch)
          .mock.calls.filter(([, init]) => init?.method === 'POST'),
      ).toHaveLength(1);
    });

    it('does not say there are no separate folders above a fold of them', async () => {
      stubDaemon();
      await renderLive(untracked(withCards()));

      expect(
        within(section(/^Separate folders/)).queryByText(
          'No separate folders yet.',
        ),
      ).toBeNull();
    });
  });

  // One list: the arrows and the tab stop follow the page, not the daemon.
  it('walks from the project folder into the separate folders', async () => {
    stubDaemon();
    await renderLive(
      withCards(worktree('w2', 'b'), card('w1', 'here', [session('s1')])),
    );
    const here = screen.getByRole('region', { name: 'Workstream here' });
    expect(here.getAttribute('tabindex')).toBe('0');

    const row = within(here).getByRole('listitem');
    act(() => row.focus());
    fireEvent.keyDown(row, { key: 'ArrowDown' });

    expect(document.activeElement).toBe(
      screen.getByRole('region', { name: 'Workstream b' }),
    );
  });
});

describe('ProjectView removal guard', () => {
  it('turns the daemon 409 into a confirmation naming the busy sessions', async () => {
    stubDaemon({ status: 409, body: busyBody });
    await deleteWorkstream();

    // The daemon's own words for the session, not the UI's guess at them.
    expect(await screen.findByText(/Implement/)).toBeTruthy();
    expect(
      screen.getByRole('button', { name: /Stop and delete/i }),
    ).toBeTruthy();
  });

  it('re-issues the delete with force once the user confirms', async () => {
    const calls = stubDaemon({ status: 409, body: busyBody });
    await deleteWorkstream();
    fireEvent.click(
      await screen.findByRole('button', { name: /Stop and delete/i }),
    );

    await waitFor(() => expect(calls).toHaveLength(2));
    // The first attempt does not force; the second carries the user's decision
    // and keeps the file choice they already made.
    expect(calls[0].body).toEqual({ keepFiles: false, force: false });
    expect(calls[1].body).toEqual({ keepFiles: false, force: true });
  });

  it('sends nothing more when the user backs out', async () => {
    const calls = stubDaemon({ status: 409, body: busyBody });
    await deleteWorkstream();
    fireEvent.click(await screen.findByRole('button', { name: 'Cancel' }));

    await waitFor(() =>
      expect(
        screen.queryByRole('button', { name: /Stop and delete/i }),
      ).toBeNull(),
    );
    expect(calls).toHaveLength(1);
  });

  // A 409 is only a decision to put back to the user when the daemon says *why*.
  // Any other refusal is a failure to report, or the busy dialog would offer to
  // force past something force does not address.
  it('reports a 409 that names no reason instead of offering force', async () => {
    stubDaemon({ status: 409, body: { error: 'something else entirely' } });
    await deleteWorkstream();

    expect(await screen.findByText('something else entirely')).toBeTruthy();
    expect(
      screen.queryByRole('button', { name: /Stop and delete/i }),
    ).toBeNull();
  });

  it("reports the daemon's message on any other failure", async () => {
    stubDaemon({ status: 500, body: { error: 'the provider fell over' } });
    await deleteWorkstream();

    expect(await screen.findByText('the provider fell over')).toBeTruthy();
  });

  it('reports a daemon it cannot reach at all', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (_url: string, init?: RequestInit) => {
        if (init?.method === 'DELETE') throw new Error('offline');
        return { ok: true, status: 200, json: async () => ({}) };
      }),
    );
    await deleteWorkstream();

    expect(await screen.findByText('Could not reach the daemon.')).toBeTruthy();
  });
});

describe('ProjectView keyboard list', () => {
  const withSessions = (ids: string[]) =>
    withCards(
      card(
        'w1',
        'feature/x',
        ids.map((id) => session(id)),
      ),
    );

  async function renderList(shown = project) {
    stubDaemon();
    const rerender = await renderLive(shown);
    return {
      card: screen.getByRole('region', { name: 'Workstream feature/x' }),
      row: screen.getAllByRole('listitem')[0],
      rerender,
    };
  }

  it('makes the first card the tab stop', async () => {
    const { card, row } = await renderList();
    expect(card.getAttribute('tabindex')).toBe('0');
    expect(row.getAttribute('tabindex')).toBe('-1');
  });

  it.each([
    ['is removed', ['s2']],
    ['is replaced by the real session', ['s1-real', 's2']],
  ])(
    'hands focus to the item in its place when the focused one %s',
    async (_case, remaining) => {
      const { row, rerender } = await renderList(withSessions(['s1', 's2']));
      act(() => row.focus());

      await rerender(withSessions(remaining));

      expect(document.activeElement).toBe(screen.getAllByRole('listitem')[0]);
      expect(document.activeElement?.textContent).toContain(remaining[0]);
    },
  );

  // A projection can list the promoted session while the placeholder, focused
  // as the created item, is still there: the list then shrinks by the
  // placeholder alone, and the flat index would land on the next card.
  it('keeps focus in the card when a placeholder goes after its session came', async () => {
    const twoCards = (ids: string[]) =>
      withCards(
        card(
          'w1',
          'feature/x',
          ids.map((id) => session(id)),
        ),
        card('w2', 'next'),
      );
    const { rerender } = await renderList(
      twoCards(['s1', 'sess-new', 'starting-1']),
    );
    act(() => screen.getAllByRole('listitem')[2].focus());

    await rerender(twoCards(['s1', 'sess-new']));

    expect(document.activeElement?.getAttribute('data-nav-id')).toBe(
      'sess-new',
    );
  });

  // Focus stays in the section the card went from.
  it.each([
    ['the project folder', project, 'Work here'],
    [
      'a separate folder',
      withCards(worktree('w1', 'feature/x', project.workstreams[0].sessions)),
      'New workstream',
    ],
  ])(
    'hands focus to its create button when the focused last card, in %s, goes',
    async (_case, shown, button) => {
      const { card, rerender } = await renderList(shown);
      act(() => card.focus());

      await rerender(withCards());

      expect(document.activeElement).toBe(
        screen.getByRole('button', { name: button }),
      );
    },
  );

  // Blank page is a deliberate place to put focus; a later removal must not
  // pull it back into the list.
  it('leaves focus alone once the user has clicked away', async () => {
    const { row, rerender } = await renderList(withSessions(['s1', 's2']));
    act(() => row.focus());
    act(() => row.blur());

    await rerender(withSessions(['s2']));

    expect(document.activeElement).toBe(document.body);
  });

  it('describes the list keys, with Delete as the removal key here', async () => {
    const { card, row } = await renderList();
    expect(card.getAttribute('aria-keyshortcuts')).toBe(
      'Enter F2 Delete Shift+F10',
    );
    expect(row.getAttribute('aria-keyshortcuts')).toBe(
      'ArrowRight ArrowLeft Enter F2 Delete Shift+F10',
    );
    expect(card.hasAttribute('aria-describedby')).toBe(false);
    expect(row.hasAttribute('aria-describedby')).toBe(false);
    const list = screen.getByRole('group', { name: 'Workstreams' });
    const hint = document.getElementById(
      list.getAttribute('aria-describedby') ?? '',
    );
    expect(hint?.textContent).toContain(
      'Delete: Delete the focused workstream or session.',
    );
  });

  // Tab comes back to where the user left off, not to the top of the list.
  it('moves the tab stop with focus', async () => {
    const { card, row } = await renderList();
    act(() => card.focus());
    fireEvent.keyDown(card, { key: 'ArrowDown' });
    expect(document.activeElement).toBe(row);
    expect(row.getAttribute('tabindex')).toBe('0');
    expect(card.getAttribute('tabindex')).toBe('-1');
  });
  // Its controls are in the Tab order only while it holds the stop.
  it('moves the tab stop to the item a focused control is in', async () => {
    const { card, row } = await renderList();
    act(() => row.focus());
    act(() =>
      within(card).getByRole('button', { name: 'New session' }).focus(),
    );
    expect(card.getAttribute('tabindex')).toBe('0');
    expect(row.getAttribute('tabindex')).toBe('-1');
  });
});

describe('ProjectView commands', () => {
  async function commandLabels(disabledReason?: string) {
    stubDaemon();
    await act(async () => {
      render(view(project, disabledReason));
    });
    return registry()
      .list()
      .map((command) => command.label);
  }

  it('offers a new workstream, the project and a session in its only one', async () => {
    expect(await commandLabels()).toEqual([
      'New workstream',
      'Project "alpha": Rename',
      'Project "alpha": Delete',
      'Workstream "feature/x": New session',
    ]);
  });

  it('offers nothing while the daemon is unreachable', async () => {
    expect(await commandLabels('Offline')).toEqual([]);
  });
});

describe('ProjectView rename', () => {
  it('says a taken workstream name as the daemon words it on the name', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (_url: string, init?: RequestInit) =>
        init?.method === 'PATCH'
          ? {
              ok: false,
              status: 409,
              json: async () => ({
                error:
                  'a workstream with this name already exists in this project: "Copy one"',
                fields: {
                  '/name':
                    '"Copy one" is already used in this project; enter another',
                },
              }),
            }
          : { ok: true, status: 200, json: async () => ({}) },
      ),
    );
    await act(async () => {
      render(view(project));
    });
    fireEvent.doubleClick(
      screen.getByRole('heading', { level: 3, name: 'feature/x' }),
    );
    const box = screen.getByRole('textbox', { name: 'Workstream name' });
    fireEvent.change(box, { target: { value: 'copy one' } });
    await act(async () => {
      fireEvent.keyDown(box, { key: 'Enter' });
    });

    expect(screen.getByRole('alert').textContent).toBe(
      '"Copy one" is already used in this project; enter another',
    );
  });

  /**
   * Answers a project rename with a refusal and a session Open with a failure,
   * so a rename error can be told apart from one the page already showed.
   */
  function stubRefusingRename() {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string, init?: RequestInit) =>
        init?.method === 'PATCH' && url === '/api/projects/p1'
          ? {
              ok: false,
              status: 409,
              json: async () => ({ error: 'a project named "beta" exists' }),
            }
          : init?.method === 'POST' && url.endsWith('/s1/open')
            ? {
                ok: false,
                status: 500,
                json: async () => ({ error: 'open failed' }),
              }
            : { ok: true, status: 200, json: async () => ({}) },
      ),
    );
  }

  async function refuseProjectRename() {
    // Double-click on the name is the pointer's way in.
    fireEvent.doubleClick(
      screen.getByRole('heading', { level: 1, name: 'alpha' }),
    );
    const box = screen.getByRole('textbox', { name: 'Project name' });
    fireEvent.change(box, { target: { value: 'beta' } });
    await act(async () => {
      fireEvent.keyDown(box, { key: 'Enter' });
    });
    return box;
  }

  it('says why a project rename was refused until the name is edited', async () => {
    stubRefusingRename();
    await act(async () => {
      render(view(project));
    });
    const box = await refuseProjectRename();
    expect(screen.getByRole('alert').textContent).toBe(
      'a project named "beta" exists',
    );

    fireEvent.change(box, { target: { value: 'gamma' } });
    expect(screen.queryByRole('alert')).toBeNull();
  });

  // D is an item's Delete: once the last item was gone, Alt+D would delete
  // the project instead. The project has no F2, with nothing selected to
  // press it on.
  it("shows the header's Delete as an icon with no letter, and the name no F2", async () => {
    stubDaemon();
    await act(async () => {
      render(view(project));
    });
    const remove = screen.getByRole('button', { name: 'Delete project alpha' });
    expect(remove.title).toBe('Stop tracking this project');
    expect(remove.textContent).toBe('');
    expect(remove.hasAttribute('data-mnemonic')).toBe(false);
    const title = screen.getByRole('heading', { level: 1, name: 'alpha' });
    expect(title.title).toBe('alpha\n\nDouble-click to rename');
    expect(title.getAttribute('aria-keyshortcuts')).toBe('Alt+R');
  });

  it('has no Rename button', async () => {
    stubDaemon();
    await act(async () => {
      render(view(project));
    });
    expect(screen.queryByRole('button', { name: /^Rename/ })).toBeNull();
  });

  it('renames on Alt+R with nothing selected', async () => {
    stubDaemon();
    await act(async () => {
      render(
        <>
          <Mnemonics />
          {view(project)}
        </>,
      );
    });
    fireEvent.keyDown(window, { key: 'r', code: 'KeyR', altKey: true });
    expect(screen.getByRole('textbox', { name: 'Project name' })).toBeTruthy();
  });

  it('does not rename on a double-click or Alt+R while offline', async () => {
    stubRefusingRename();
    await act(async () => {
      render(
        <>
          <Mnemonics />
          {view(project, 'Daemon offline')}
        </>,
      );
    });
    const title = screen.getByRole('heading', { level: 1, name: 'alpha' });
    expect(title.hasAttribute('data-mnemonic')).toBe(false);
    expect(title.title).toBe('alpha\n\nDaemon offline');
    fireEvent.doubleClick(title);
    fireEvent.keyDown(window, { key: 'r', code: 'KeyR', altKey: true });
    expect(screen.queryByRole('textbox', { name: 'Project name' })).toBeNull();
  });

  it('shows an unrelated failure beside a rename error, and keeps it once the name is edited', async () => {
    stubRefusingRename();
    await act(async () => {
      render(view(project));
    });
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Open' }));
    });
    expect(screen.getByRole('alert').textContent).toBe('open failed');

    const box = await refuseProjectRename();
    expect(
      screen.getAllByRole('alert').map((alert) => alert.textContent),
    ).toEqual(['a project named "beta" exists', 'open failed']);
    fireEvent.change(box, { target: { value: 'gamma' } });
    expect(screen.getByRole('alert').textContent).toBe('open failed');
  });

  // Enter asked for focus back, but the box stayed open; the blur that later
  // commits it must not pull focus back from where the user went.
  it('takes no focus back for an Enter refused while offline', async () => {
    stubDaemon();
    const elsewhere = document.createElement('button');
    document.body.append(elsewhere);
    let rerender: (ui: ReactElement) => void = () => undefined;
    await act(async () => {
      ({ rerender } = render(view(project)));
    });
    fireEvent.doubleClick(
      screen.getByRole('heading', { level: 1, name: 'alpha' }),
    );
    const box = screen.getByRole('textbox', { name: 'Project name' });
    act(() => box.focus());
    fireEvent.change(box, { target: { value: 'beta' } });
    rerender(view(project, 'Offline'));
    fireEvent.keyDown(box, { key: 'Enter' });
    expect(screen.getByRole('alert').textContent).toBe('Offline');

    rerender(view(project));
    await act(async () => elsewhere.focus());

    await waitFor(() =>
      expect(
        screen.queryByRole('textbox', { name: 'Project name' }),
      ).toBeNull(),
    );
    expect(document.activeElement).toBe(elsewhere);
    elsewhere.remove();
  });
});

describe('ProjectView create refusal', () => {
  it('says why under the field the daemon blames, and focuses it', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string, init?: RequestInit) => {
        if (url.endsWith('/workstream-providers')) {
          return {
            ok: true,
            status: 200,
            json: async () => [
              {
                id: 'fake',
                label: 'Fake',
                workstreamSchema: {
                  properties: {
                    base: { type: 'string', title: 'Base' },
                    branch: { type: 'string', title: 'Branch' },
                  },
                },
              },
            ],
          };
        }
        if (init?.method === 'POST') {
          return {
            ok: false,
            status: 400,
            json: async () => ({
              error: 'configuration is invalid',
              fields: { '/branch': 'required' },
            }),
          };
        }
        return { ok: true, status: 200, json: async () => ({}) };
      }),
    );
    render(view(project));
    fireEvent.click(screen.getByRole('button', { name: 'New workstream' }));
    fireEvent.submit(
      await screen.findByRole('form', { name: 'New workstream' }),
    );

    const branch = screen.getByLabelText('Branch');
    await waitFor(() => expect(document.activeElement).toBe(branch));
    expect(branch.getAttribute('aria-invalid')).toBe('true');
    expect(
      document.getElementById(branch.getAttribute('aria-describedby') ?? '')
        ?.textContent,
    ).toBe('Required');
  });
});

describe('ProjectView create draft', () => {
  it("asks the daemon for the new workstream's defaults and shows them", async () => {
    const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
      const json = (body: unknown) => ({
        ok: true,
        status: 200,
        json: async () => body,
      });
      if (url.endsWith('/workstream-providers')) {
        return json([{ id: 'fake', label: 'Fake', workstreamSchema: {} }]);
      }
      if (init?.method === 'POST' && url.endsWith('/workstream-draft')) {
        return json({ name: 'alpha-2', config: {} });
      }
      return json({});
    });
    vi.stubGlobal('fetch', fetchMock);
    render(view(project));
    fireEvent.click(screen.getByRole('button', { name: 'New workstream' }));

    const name = await screen.findByRole('textbox', { name: 'Name' });
    await waitFor(() => expect(name).toHaveProperty('value', 'alpha-2'));
    expect(fetchMock).toHaveBeenCalledWith(
      '/api/projects/p1/workstream-draft',
      expect.objectContaining({
        method: 'POST',
        body: JSON.stringify({ providerId: 'fake', name: '', config: {} }),
      }),
    );
  });
});

describe('ProjectView focus around creating', () => {
  const opener = () => screen.getByRole('button', { name: 'New workstream' });
  const nameBox = () => screen.findByRole('textbox', { name: 'Name' });

  it('keeps New workstream enabled, and asking again refocuses the form', async () => {
    stubCreates(() => ({}));
    await renderLive(project);
    fireEvent.click(opener());
    const name = await nameBox();
    // The form mounts once the providers load, and focuses its name an
    // effect later: the box can be found before it has focus.
    await waitFor(() => expect(document.activeElement).toBe(name));

    act(() => opener().focus());
    expect((opener() as HTMLButtonElement).disabled).toBe(false);
    act(() => registry().run('newWorkstream'));
    expect(document.activeElement).toBe(name);
  });

  it('gives focus back to what it was opened from on Escape', async () => {
    stubCreates(() => ({}));
    await renderLive(project);
    const focused = screen.getByRole('region', {
      name: 'Workstream feature/x',
    });
    act(() => focused.focus());
    act(() => registry().run('newWorkstream'));
    fireEvent.keyDown(await nameBox(), { key: 'Escape' });

    await waitFor(() => expect(document.activeElement).toBe(focused));
  });

  // The form is in the list, but it is not an item: closing it must not hand
  // focus to the card holding the tab stop on the way back.
  it('gives focus back to New workstream on Escape, selecting no card', async () => {
    stubCreates(() => ({}));
    await renderLive(project);
    fireEvent.click(opener());
    fireEvent.keyDown(await nameBox(), { key: 'Escape' });

    await waitFor(() => expect(document.activeElement).toBe(opener()));
    expect(document.querySelector('[aria-current="true"]')).toBeNull();
  });

  // The placeholder the daemon answered with, and then the real workstream
  // the daemon swaps in for it.
  it('focuses the created workstream, placeholder and then the real one', async () => {
    stubCreates(() => ({ id: 'creating-1' }));
    const rerender = await renderLive(project);
    fireEvent.click(opener());
    fireEvent.submit(
      await screen.findByRole('form', { name: 'New workstream' }),
    );
    await waitFor(() =>
      expect(screen.queryByRole('form', { name: 'New workstream' })).toBeNull(),
    );

    await rerender(
      withCards(project.workstreams[0], card('creating-1', 'new')),
    );
    expect(document.activeElement).toBe(
      screen.getByRole('region', { name: 'Workstream new' }),
    );
    await rerender(withCards(project.workstreams[0], card('w-real', 'real')));
    expect(document.activeElement).toBe(
      screen.getByRole('region', { name: 'Workstream real' }),
    );
  });

  // By the placeholder it names, not as whatever is new: another client's
  // workstream can be listed first.
  it.each([
    ['alone', [card('w-real', 'real', [], 'creating-1')]],
    [
      'after another new one',
      [card('w-other', 'other'), card('w-real', 'real', [], 'creating-1')],
    ],
  ])(
    'focuses the created workstream when the real one is listed first, %s',
    async (_case, listed) => {
      let answer: () => void = () => undefined;
      stubCreates(
        () => ({ id: 'creating-1' }),
        new Promise<void>((resolve) => {
          answer = resolve;
        }),
      );
      const rerender = await renderLive(project);
      fireEvent.click(opener());
      fireEvent.submit(
        await screen.findByRole('form', { name: 'New workstream' }),
      );
      await rerender(withCards(project.workstreams[0], ...listed));
      await act(async () => answer());

      await waitFor(() =>
        expect(document.activeElement).toBe(
          screen.getByRole('region', { name: 'Workstream real' }),
        ),
      );
    },
  );

  // The user moved on, to the sidebar say, before the daemon listed it.
  it('leaves focus where the user moved it before the workstream is listed', async () => {
    stubCreates(() => ({ id: 'creating-1' }));
    const rerender = await renderLive(project);
    fireEvent.click(opener());
    fireEvent.submit(
      await screen.findByRole('form', { name: 'New workstream' }),
    );
    await waitFor(() =>
      expect(screen.queryByRole('form', { name: 'New workstream' })).toBeNull(),
    );
    const sidebar = document.createElement('button');
    document.body.append(sidebar);
    act(() => sidebar.focus());

    await rerender(
      withCards(project.workstreams[0], card('creating-1', 'new')),
    );

    expect(document.activeElement).toBe(sidebar);
    sidebar.remove();
  });

  it('focuses the created session', async () => {
    stubCreates(() => ({ id: 'starting-1' }));
    const rerender = await renderLive(project);
    fireEvent.click(screen.getByRole('button', { name: 'New session' }));
    fireEvent.submit(await screen.findByRole('form', { name: 'New session' }));
    await waitFor(() =>
      expect(screen.queryByRole('form', { name: 'New session' })).toBeNull(),
    );

    await rerender(
      withCards(
        card('w1', 'feature/x', [
          project.workstreams[0].sessions[0],
          session('starting-1'),
        ]),
      ),
    );
    expect(document.activeElement?.getAttribute('data-nav-id')).toBe(
      'starting-1',
    );
  });

  // Stop goes away when the session goes idle rather than stopped.
  it('hands focus to the row when a control in it goes away', async () => {
    stubCreates(() => ({}));
    const rerender = await renderLive(
      withCards(card('w1', 'x', [session('s1')])),
    );
    const row = screen.getAllByRole('listitem')[0];
    act(() => screen.getByRole('button', { name: 'Stop' }).focus());

    await rerender(withCards(card('w1', 'x', [session('s1', 'idle')])));

    expect(screen.queryByRole('button', { name: 'Stop' })).toBeNull();
    expect(document.activeElement).toBe(row);
  });

  it('hands focus to the row when a control in it is disabled', async () => {
    stubCreates(() => ({}));
    const rerender = await renderLive(
      withCards(card('w1', 'x', [session('s1')])),
    );
    const row = screen.getAllByRole('listitem')[0];
    const open = within(row).getByRole('button', { name: 'Open' });
    act(() => open.focus());

    await rerender(withCards(card('w1', 'x', [session('s1', 'stopped')])));
    // The browser blurs a focused control it disables; jsdom does not.
    fireEvent.focusOut(open);

    expect(document.activeElement).toBe(row);
  });
});

describe('ProjectView new session command', () => {
  const two = withCards(
    card('w1', 'first', [session('s1')]),
    card('w2', 'second', [session('s2')]),
  );
  const label = () =>
    registry()
      .list()
      .find((command) => command.id === 'newSession')?.label;
  const startIn = (name: string) =>
    within(
      screen.getByRole('region', { name: `Workstream ${name}` }),
    ).findByRole('button', { name: 'Start session' });

  it('opens the form of the workstream a focused row is in', async () => {
    stubCreates(() => ({}));
    await renderLive(two);
    act(() => screen.getAllByRole('listitem')[1].focus());
    expect(label()).toBe('Workstream "second": New session');

    act(() => registry().run('newSession'));

    const start = await startIn('second');
    await waitFor(() => expect(document.activeElement).toBe(start));
  });

  it('asks which workstream when none is selected', async () => {
    stubCreates(() => ({}));
    await renderLive(two);
    expect(label()).toBe('New session…');

    act(() => registry().run('newSession'));
    const pick = screen.getByRole('dialog', { name: 'New session in' });
    const box = within(pick).getByRole('combobox');
    fireEvent.change(box, { target: { value: 'sec' } });
    fireEvent.keyDown(box, { key: 'Enter' });

    expect(screen.queryByRole('dialog')).toBeNull();
    const start = await startIn('second');
    await waitFor(() => expect(document.activeElement).toBe(start));
  });
  const settingUp = {
    ...card('creating-1', 'setting up'),
    effectiveState: 'creating' as const,
  };

  it('starts one in the only workstream that can', async () => {
    stubCreates(() => ({}));
    await renderLive(
      withCards(card('w1', 'first', [session('s1')]), settingUp),
    );
    expect(label()).toBe('Workstream "first": New session');
  });

  it('offers none in a selected workstream that cannot start one', async () => {
    stubCreates(() => ({}));
    await renderLive(
      withCards(card('w1', 'first', [session('s1')]), settingUp),
    );
    act(() =>
      screen.getByRole('region', { name: 'Workstream setting up' }).focus(),
    );
    expect(label()).toBeUndefined();
  });
});

describe('ProjectView selection commands', () => {
  const labels = () =>
    registry()
      .list()
      .filter((command) => command.id.startsWith('selection.'))
      .map((command) => command.label);

  it('offers nothing until the list has had focus', async () => {
    stubCreates(() => ({}));
    await renderLive(withCards(card('w1', 'x', [session('s1')])));
    expect(labels()).toEqual([]);
  });

  // What the commands would act on is shown, focused or not: after a click,
  // which draws no focus ring, and once focus has left the list.
  it('marks the selection, and keeps it marked once focus has left', async () => {
    stubCreates(() => ({}));
    await renderLive(withCards(card('w1', 'x', [session('s1')])));
    const row = screen.getAllByRole('listitem')[0];
    expect(row.getAttribute('aria-current')).toBeNull();

    act(() => row.focus());
    act(() => screen.getByRole('button', { name: 'New workstream' }).focus());

    expect(row.getAttribute('aria-current')).toBe('true');
    expect(
      screen
        .getByRole('region', { name: 'Workstream x' })
        .getAttribute('aria-current'),
    ).toBeNull();
    expect(labels()).toContain('Session "s1": Open');
  });

  it('offers what the focused row does, named after it', async () => {
    stubCreates(() => ({}));
    await renderLive(withCards(card('w1', 'x', [session('s1')])));
    act(() => screen.getAllByRole('listitem')[0].focus());

    expect(labels()).toEqual([
      'Session "s1": Open',
      'Session "s1": Stop',
      'Session "s1": Rename',
      'Session "s1": Show details',
      'Session "s1": Delete',
    ]);
  });

  // Named in full, so the palette matches all of it; the name is the span it
  // cuts off, so the verb after it stays in view.
  it('names a long name in full, marking it as what gives way', async () => {
    stubCreates(() => ({}));
    const name = `${'a'.repeat(60)}-tail`;
    await renderLive(withCards(card('w1', name, [session('s1')])));
    act(() =>
      screen.getByRole('region', { name: `Workstream ${name}` }).focus(),
    );

    const rename = registry()
      .list()
      .find((command) => command.id === 'selection.rename');
    expect(rename?.label).toBe(`Workstream "${name}": Rename`);
    const [start, end] = rename?.elided ?? [0, 0];
    expect(rename?.label.slice(start, end)).toBe(name);
  });

  // The newSession command is that already.
  it('leaves the focused card’s New session to the newSession command', async () => {
    stubCreates(() => ({}));
    await renderLive(withCards(card('w1', 'x', [session('s1')])));
    act(() => screen.getByRole('region', { name: 'Workstream x' }).focus());

    expect(labels()).toContain('Workstream "x": Rename');
    expect(
      registry()
        .list()
        .filter((command) => command.label === 'Workstream "x": New session')
        .map((command) => command.id),
    ).toEqual(['newSession']);
  });

  it('renames the focused workstream from the palette', async () => {
    stubCreates(() => ({}));
    await renderLive(withCards(card('w1', 'x', [session('s1')])));
    act(() => screen.getByRole('region', { name: 'Workstream x' }).focus());

    act(() =>
      registry()
        .list()
        .find((command) => command.label === 'Workstream "x": Rename')
        ?.run(),
    );

    expect(
      screen.getByRole('textbox', { name: 'Workstream name' }),
    ).toBeTruthy();
  });
});

describe('ProjectView restart with settings', () => {
  const restartable = (id: string) => ({ ...session(id), restartable: true });
  const schema = {
    type: 'object',
    properties: {
      model: { type: 'string', enum: ['opus', 'sonnet'], default: 'sonnet' },
      enclave: {
        type: 'object',
        title: 'Container',
        properties: {
          allowDomains: {
            type: 'array',
            title: 'Extra allowed domains',
            items: { type: 'string' },
            default: [],
          },
        },
      },
    },
  };

  /**
   * A provider advertising sessionRestart (the menu itself is gated on the
   * session's restartable flag), the launch config `launched` answers with once
   * `answered` settles, and each restart answered with `restart`. Returns the
   * restart, launch-config and session-schemas requests.
   */
  function stubRestart(
    launched: { status: number; body?: unknown },
    restart: { status: number; body?: unknown } = { status: 202 },
    answered: Promise<void> = Promise.resolve(),
  ) {
    const calls: { url: string; body: unknown }[] = [];
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string, init?: RequestInit) => {
        const json = (body: unknown, status = 200) => ({
          ok: status < 400,
          status,
          json: async () => body,
        });
        if (url.endsWith('/restart')) {
          calls.push({ url, body: JSON.parse(String(init?.body)) });
          return json(restart.body ?? {}, restart.status);
        }
        if (url.endsWith('/launch-config')) {
          calls.push({ url, body: undefined });
          await answered;
          return json(launched.body ?? { error: 'not found' }, launched.status);
        }
        if (url.endsWith('/session-schemas')) {
          calls.push({ url, body: undefined });
          return json([]);
        }
        if (url === '/api/providers') {
          return json([
            {
              id: 'fake',
              capabilities: { sessionStartStop: true, sessionRestart: true },
            },
          ]);
        }
        return json({});
      }),
    );
    return calls;
  }

  const restarts = <Call extends { url: string }>(calls: Call[]) =>
    calls.filter((call) => call.url.endsWith('/restart'));

  const restartCommand = () =>
    registry()
      .list()
      .find((command) => command.id === 'selection.restartWithSettings');

  async function openDialog() {
    await renderLive(withCards(card('w1', 'x', [restartable('s1')])));
    act(() => screen.getAllByRole('listitem')[0].focus());
    await waitFor(() => expect(restartCommand()).toBeDefined());
    expect(restartCommand()?.label).toBe(
      'Session "s1": Restart with settings…',
    );
    await act(async () => restartCommand()?.run());
    return screen.findByRole('dialog', { name: 'Restart s1' });
  }

  it('opens on the launch config and restarts with what was chosen', async () => {
    const calls = stubRestart({
      status: 200,
      body: {
        providerId: 'fake',
        optionId: 'claude',
        config: { model: 'opus', enclave: { allowDomains: ['a.example'] } },
        schema,
      },
    });
    const dialog = await openDialog();
    // The launch config carries the option's schema: the session types are not
    // listed to find it.
    expect(
      calls.filter((call) => call.url.endsWith('/session-schemas')),
    ).toHaveLength(0);
    expect(
      within(dialog)
        .getByRole('radio', { name: 'opus' })
        .getAttribute('aria-checked'),
    ).toBe('true');
    expect(within(dialog).getByText('a.example')).toBeTruthy();

    fireEvent.click(within(dialog).getByRole('radio', { name: 'sonnet' }));
    fireEvent.click(within(dialog).getByRole('button', { name: 'Restart' }));

    await waitFor(() => expect(restarts(calls)).toHaveLength(1));
    expect(restarts(calls)[0].url).toBe(
      '/api/projects/p1/workstreams/w1/sessions/s1/restart',
    );
    expect(restarts(calls)[0].body).toEqual({
      config: { model: 'sonnet', enclave: { allowDomains: ['a.example'] } },
      fresh: false,
    });
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  });

  /** A session the daemon adopted: the option it restarts as, no config. */
  const unrecorded = {
    status: 200,
    body: { providerId: 'fake', optionId: 'claude', config: null, schema },
  };

  it('restarts fresh when the daemon says the conversation cannot continue', async () => {
    const calls = stubRestart({
      status: 200,
      body: {
        ...unrecorded.body,
        continueRefusal: 'another claude session works in /work',
      },
    });
    const dialog = await openDialog();
    expect(
      within(dialog).getByText('Another claude session works in /work'),
    ).toBeTruthy();
    fireEvent.click(within(dialog).getByRole('button', { name: 'Restart' }));
    await waitFor(() => expect(restarts(calls)).toHaveLength(1));
    expect(restarts(calls)[0].body).toMatchObject({ fresh: true });
  });

  it('opens the option the daemon names, on defaults, without a record', async () => {
    const calls = stubRestart(unrecorded);
    const dialog = await openDialog();
    fireEvent.click(within(dialog).getByRole('button', { name: 'Restart' }));
    await waitFor(() => expect(restarts(calls)).toHaveLength(1));
    expect(restarts(calls)[0].body).toEqual({
      config: { model: 'sonnet', enclave: { allowDomains: [] } },
      fresh: false,
    });
  });

  it('restarts on the recorded settings when the option has none to show', async () => {
    const calls = stubRestart({
      status: 200,
      body: {
        providerId: 'fake',
        optionId: 'claude',
        config: null,
        schema: {},
      },
    });
    const dialog = await openDialog();
    fireEvent.click(within(dialog).getByRole('button', { name: 'Restart' }));
    await waitFor(() => expect(restarts(calls)).toHaveLength(1));
    expect(restarts(calls)[0].body).toEqual({ fresh: false });
  });

  it('names the session as it is now, and closes once it is gone', async () => {
    stubRestart(unrecorded);
    const show = await renderLive(
      withCards(card('w1', 'x', [restartable('s1')])),
    );
    act(() => screen.getAllByRole('listitem')[0].focus());
    await waitFor(() => expect(restartCommand()).toBeDefined());
    await act(async () => restartCommand()?.run());
    await screen.findByRole('dialog', { name: 'Restart s1' });

    await show(
      withCards(card('w1', 'x', [{ ...restartable('s1'), title: 'Renamed' }])),
    );
    expect(
      screen.getByRole('dialog', { name: 'Restart Renamed' }),
    ).toBeTruthy();

    await show(withCards(card('w1', 'x', [])));
    expect(screen.queryByRole('dialog')).toBeNull();
    // Not lost with the row: on the list's tab stop, its card.
    expect(document.activeElement).toBe(
      screen.getByRole('region', { name: 'Workstream x' }),
    );
  });

  it('keeps the dialog open on a refusal, explaining the field', async () => {
    stubRestart(unrecorded, {
      status: 400,
      body: {
        error: 'invalid config',
        fields: { '/enclave/allowDomains/0': 'must not be empty' },
      },
    });
    const dialog = await openDialog();
    fireEvent.click(within(dialog).getByRole('button', { name: 'Restart' }));
    expect(await within(dialog).findByText('Must not be empty')).toBeTruthy();
    expect(screen.getByRole('dialog', { name: 'Restart s1' })).toBeTruthy();
  });

  it('keeps the dialog open on a provider it cannot read, saying so', async () => {
    stubRestart(unrecorded, {
      status: 503,
      body: {
        error:
          "provider is unavailable: could not read Fake's settings for the session; try again (exit status 1)",
      },
    });
    const dialog = await openDialog();
    fireEvent.click(within(dialog).getByRole('button', { name: 'Restart' }));
    expect((await within(dialog).findByRole('alert')).textContent).toBe(
      "Provider is unavailable: could not read Fake's settings for the session; try again (exit status 1)",
    );
  });

  it('drops the settings a project switch overtook', async () => {
    let answer = () => undefined as void;
    stubRestart(
      unrecorded,
      undefined,
      new Promise<void>((resolve) => (answer = resolve)),
    );
    const show = await renderLive(
      withCards(card('w1', 'x', [restartable('s1')])),
    );
    act(() => screen.getAllByRole('listitem')[0].focus());
    await waitFor(() => expect(restartCommand()).toBeDefined());
    act(() => {
      void restartCommand()?.run();
    });
    await show({ ...project, id: 'p2', name: 'beta', workstreams: [] });
    await act(async () => answer());
    expect(screen.queryByRole('dialog')).toBeNull();
  });

  it('loads once when asked twice, saying it is busy meanwhile', async () => {
    let answer = () => undefined as void;
    const calls = stubRestart(
      unrecorded,
      undefined,
      new Promise<void>((resolve) => (answer = resolve)),
    );
    await renderLive(withCards(card('w1', 'x', [restartable('s1')])));
    act(() => screen.getAllByRole('listitem')[0].focus());
    await waitFor(() => expect(restartCommand()).toBeDefined());
    // One live region, there before it speaks, so it is heard.
    const status = screen.getByRole('status');
    expect(status.textContent).toBe('');
    act(() => {
      void restartCommand()?.run();
    });
    act(() => {
      void restartCommand()?.run();
    });
    expect(status.textContent).toBe('Loading settings…');
    await act(async () => answer());
    expect(
      await screen.findByRole('dialog', { name: 'Restart s1' }),
    ).toBeTruthy();
    expect(
      calls.filter((call) => call.url.endsWith('/launch-config')),
    ).toHaveLength(1);
    expect(status.textContent).toBe('');
  });

  // Alt+S is the dialog's Restart, not the row's Stop behind it.
  it('lets no letter reach the page while the dialog loads', async () => {
    let answer = () => undefined as void;
    stubRestart(
      unrecorded,
      undefined,
      new Promise<void>((resolve) => (answer = resolve)),
    );
    await act(async () => {
      render(
        <>
          <Mnemonics />
          {view(withCards(card('w1', 'x', [restartable('s1')])))}
        </>,
      );
    });
    act(() => screen.getAllByRole('listitem')[0].focus());
    await waitFor(() => expect(restartCommand()).toBeDefined());
    act(() => {
      void restartCommand()?.run();
    });
    fireEvent.keyDown(window, { key: 's', code: 'KeyS', altKey: true });
    await act(async () => answer());
    await screen.findByRole('dialog', { name: 'Restart s1' });
    expect(
      vi
        .mocked(fetch)
        .mock.calls.filter(([url]) => String(url).endsWith('/stop')),
    ).toHaveLength(0);
  });

  /** Answers `url`s ending in `suffix` with `answer`, the rest as before. */
  function answerWith(
    suffix: string,
    answer: () => Promise<{ ok: boolean; status: number; json: () => unknown }>,
  ) {
    const stub = vi.mocked(fetch);
    const before = stub.getMockImplementation() ?? fetch;
    stub.mockImplementation((url, init) =>
      String(url).endsWith(suffix)
        ? (answer() as unknown as Promise<Response>)
        : before(url, init),
    );
  }

  async function restartFails() {
    await renderLive(withCards(card('w1', 'x', [restartable('s1')])));
    act(() => screen.getAllByRole('listitem')[0].focus());
    await waitFor(() => expect(restartCommand()).toBeDefined());
    await act(async () => restartCommand()?.run());
    const alert = await screen.findByRole('alert');
    expect(screen.queryByRole('dialog')).toBeNull();
    return alert.textContent;
  }

  it('says the daemon could not be reached when a load does not get there', async () => {
    stubRestart(unrecorded);
    answerWith('/launch-config', () =>
      Promise.reject(new TypeError('Failed to fetch')),
    );
    expect(await restartFails()).toBe('Could not reach the daemon.');
  });

  it('says what failed when the daemon answers with something other than JSON', async () => {
    stubRestart(unrecorded);
    answerWith('/launch-config', async () => ({
      ok: true,
      status: 200,
      json: async () => {
        throw new SyntaxError("Unexpected token '<'");
      },
    }));
    expect(await restartFails()).toBe('Could not load the session settings.');
  });

  it('says so when the daemon does not offer the session type any more', async () => {
    const refusal =
      'the session type it was started as is no longer offered; start a new session instead ("gone")';
    stubRestart({ status: 409, body: { error: refusal } });
    expect(await restartFails()).toBe(
      refusal.charAt(0).toUpperCase() + refusal.slice(1),
    );
  });

  it('says why, rather than opening, when the provider cannot read the settings', async () => {
    stubRestart({
      status: 503,
      body: {
        error:
          "provider is unavailable: could not read Fake's settings for the session; try again (exit status 1)",
      },
    });
    expect(await restartFails()).toBe(
      "Provider is unavailable: could not read Fake's settings for the session; try again (exit status 1)",
    );
  });

  it('says why, rather than opening, when the daemon has no settings', async () => {
    const calls = stubRestart({
      status: 409,
      body: { error: 'session never started; remove it and start a new one' },
    });
    await renderLive(withCards(card('w1', 'x', [restartable('s1')])));
    act(() => screen.getAllByRole('listitem')[0].focus());
    await waitFor(() => expect(restartCommand()).toBeDefined());
    await act(async () => restartCommand()?.run());
    expect((await screen.findByRole('alert')).textContent).toBe(
      'Session never started; remove it and start a new one',
    );
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(restarts(calls)).toHaveLength(0);
  });

  it('offers no restart for a session the daemon says cannot be', async () => {
    stubRestart({ status: 404 });
    await renderLive(withCards(card('w1', 'x', [session('s1')])));
    act(() => screen.getAllByRole('listitem')[0].focus());
    expect(restartCommand()).toBeUndefined();
  });
});
