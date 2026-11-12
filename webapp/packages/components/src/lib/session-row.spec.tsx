import type { Session } from '@homeshell/model';
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react';

import { createRef } from 'react';

import { useMnemonics } from './mnemonic';
import { SessionRow, type SessionRowHandle } from './session-row';

/** A session in `state`, marked ended where the daemon would mark it. */
const session = (state: Session['state']): Session => ({
  id: 'sess-1',
  title: 'Implement self-update',
  agent: 'Claude Code',
  state,
  ended: state === 'stopped' || state === 'done',
});

const renderRow = (props: Parameters<typeof SessionRow>[0]) =>
  render(
    <ul>
      <SessionRow {...props} />
    </ul>,
  );

const openMenu = () =>
  fireEvent.click(screen.getByRole('button', { name: 'More actions' }));

describe('SessionRow', () => {
  it('shows title, agent and the state word', () => {
    renderRow({ session: session('waiting') });
    expect(screen.getByText('Implement self-update')).toBeTruthy();
    expect(screen.getByText('Claude Code')).toBeTruthy();
    expect(screen.getByTestId('session-state').textContent).toBe(
      'Waiting for input',
    );
  });

  // The UI's own Done (idle, unseen) is a live session, not an ended one.
  it('shows an idle, unseen session as Done, still openable, with no Continue', () => {
    renderRow({
      session: session('idle'),
      displayState: 'done',
      onOpen: vi.fn(),
      onStart: vi.fn(),
    });
    expect(screen.getByTestId('session-state').textContent).toBe('Done');
    expect(
      (screen.getByRole('button', { name: 'Open' }) as HTMLButtonElement)
        .disabled,
    ).toBe(false);
    expect(screen.queryByRole('button', { name: 'Continue' })).toBeNull();
  });

  it('offers Stop as an icon button for a running session with a handler', () => {
    const onStop = vi.fn();
    renderRow({ session: session('running'), onStop });
    const stop = screen.getByRole('button', { name: 'Stop' });
    expect(stop.textContent).toBe('');
    fireEvent.click(stop);
    expect(onStop).toHaveBeenCalled();
  });

  // Stop goes with a live runtime, not with whether the agent is busy.
  it.each([
    ['an idle session shown as Done', session('idle'), 'done' as const],
    ['a session whose state is unknown', session('unknown'), undefined],
  ])('offers Stop for %s', (_, s, displayState) => {
    renderRow({ session: s, displayState, onStop: vi.fn() });
    expect(screen.getByRole('button', { name: 'Stop' })).toBeTruthy();
  });

  it('does not offer Stop without a handler (capability absent)', () => {
    renderRow({ session: session('running') });
    expect(screen.queryByRole('button', { name: 'Stop' })).toBeNull();
  });

  // Stop swaps for Continue in place, so pressing it leaves focus on the button.
  it('keeps focus on Stop as it becomes Continue', () => {
    const props = { onStop: vi.fn(), onStart: vi.fn() };
    const { rerender } = renderRow({ session: session('running'), ...props });
    const stop = screen.getByRole('button', { name: 'Stop' });
    stop.focus();
    rerender(
      <ul>
        <SessionRow session={session('stopped')} {...props} />
      </ul>,
    );
    expect(document.activeElement).toBe(stop);
    expect(stop.getAttribute('aria-label')).toBe('Continue');
    expect(stop.textContent).toBe('');
    fireEvent.click(stop);
    expect(props.onStart).toHaveBeenCalled();
  });

  it('ignores a held Enter on Stop, which would press Continue next', () => {
    renderRow({ session: session('running'), onStop: vi.fn() });
    const stop = screen.getByRole('button', { name: 'Stop' });
    expect(fireEvent.keyDown(stop, { key: 'Enter' })).toBe(true);
    expect(fireEvent.keyDown(stop, { key: 'Enter', repeat: true })).toBe(false);
  });

  it('deletes from the More actions menu, after a confirmation', () => {
    const onRemove = vi.fn();
    renderRow({ session: session('running'), onRemove });
    expect(screen.queryByRole('button', { name: /Delete/ })).toBeNull();
    openMenu();
    fireEvent.click(screen.getByRole('menuitem', { name: 'Delete' }));
    expect(onRemove).not.toHaveBeenCalled();
    fireEvent.click(
      within(screen.getByRole('dialog')).getByRole('button', {
        name: 'Delete',
      }),
    );
    expect(onRemove).toHaveBeenCalled();
  });

  it('does not offer Delete without a handler', () => {
    renderRow({ session: session('running') });
    openMenu();
    expect(screen.queryByRole('menuitem', { name: 'Delete' })).toBeNull();
  });

  it('marks a done session seen when its title is clicked', () => {
    const onSeen = vi.fn();
    renderRow({ session: session('idle'), displayState: 'done', onSeen });
    // The title area is the disclosure; clicking it on a done session both
    // toggles details and clears the attention overlay.
    fireEvent.click(screen.getByText('Implement self-update'));
    expect(onSeen).toHaveBeenCalled();
  });

  it('toggles the details panel when the title is clicked', () => {
    renderRow({ session: { ...session('running'), preview: 'l1\nl2' } });
    expect(screen.queryByTestId('session-preview-full')).toBeNull();
    fireEvent.click(screen.getByText('Implement self-update'));
    expect(screen.getByTestId('session-preview-full')).toBeTruthy();
    fireEvent.click(screen.getByText('Implement self-update'));
    expect(screen.queryByTestId('session-preview-full')).toBeNull();
  });

  it('clicking a non-done title expands without marking it seen', () => {
    const onSeen = vi.fn();
    renderRow({ session: { ...session('running'), preview: 'x' }, onSeen });
    fireEvent.click(screen.getByText('Implement self-update'));
    expect(onSeen).not.toHaveBeenCalled();
    expect(screen.getByTestId('session-preview-full')).toBeTruthy();
  });

  it('disables Open when no onOpen is provided, enables and fires it otherwise', () => {
    renderRow({ session: session('running') });
    expect(
      (screen.getByRole('button', { name: 'Open' }) as HTMLButtonElement)
        .disabled,
    ).toBe(true);

    const onOpen = vi.fn();
    render(
      <ul>
        <SessionRow session={session('idle')} onOpen={onOpen} />
      </ul>,
    );
    const open = screen.getAllByRole('button', { name: 'Open' })[1];
    expect((open as HTMLButtonElement).disabled).toBe(false);
    fireEvent.click(open);
    expect(onOpen).toHaveBeenCalled();
  });

  it('renders the last preview line inline', () => {
    renderRow({
      session: { ...session('running'), preview: 'building\n$ done' },
    });
    expect(screen.getByTestId('session-preview').textContent).toBe('$ done');
  });

  it('titles the inline preview line in full on hover while it is cut off', () => {
    renderRow({
      session: { ...session('running'), preview: 'building\n$ done' },
    });
    const line = screen.getByTestId('session-preview');
    Object.defineProperty(line, 'scrollWidth', { value: 500 });
    fireEvent.pointerEnter(line);
    expect(line.getAttribute('title')).toBe('$ done');
  });

  it('does not render the preview element when preview is empty', () => {
    renderRow({ session: session('running') });
    expect(screen.queryByTestId('session-preview')).toBeNull();
  });

  it('shows the full preview when expanded', () => {
    renderRow({ session: { ...session('running'), preview: 'l1\nl2\nl3' } });
    fireEvent.click(
      screen.getByRole('button', { name: /toggle session details/i }),
    );
    const full = screen.getByTestId('session-preview-full');
    expect(full.textContent).toContain('l1');
    expect(full.textContent).toContain('l3');
  });

  it('toggles aria-expanded on the disclosure button', () => {
    renderRow({ session: session('running') });
    const toggle = screen.getByRole('button', {
      name: /toggle session details/i,
    });
    expect(toggle.getAttribute('aria-expanded')).toBe('false');
    fireEvent.click(toggle);
    expect(toggle.getAttribute('aria-expanded')).toBe('true');
  });

  it('shows the input only when onPrompt is set and sends trimmed text', () => {
    const onPrompt = vi.fn();
    renderRow({ session: { ...session('running'), preview: 'x' }, onPrompt });
    fireEvent.click(
      screen.getByRole('button', { name: /toggle session details/i }),
    );
    const input = screen.getByLabelText(
      /send text to session/i,
    ) as HTMLInputElement;
    fireEvent.change(input, { target: { value: '  hello  ' } });
    fireEvent.click(screen.getByRole('button', { name: /^send$/i }));
    expect(onPrompt).toHaveBeenCalledWith('hello');
    expect(input.value).toBe('');
  });

  it('does not send when the input is empty (or whitespace only)', () => {
    const onPrompt = vi.fn();
    renderRow({ session: { ...session('running'), preview: 'x' }, onPrompt });
    fireEvent.click(
      screen.getByRole('button', { name: /toggle session details/i }),
    );
    fireEvent.click(screen.getByRole('button', { name: /^send$/i }));
    expect(onPrompt).not.toHaveBeenCalled();
  });

  it('renders no input when onPrompt is absent', () => {
    renderRow({ session: { ...session('running'), preview: 'x' } });
    fireEvent.click(
      screen.getByRole('button', { name: /toggle session details/i }),
    );
    expect(screen.queryByLabelText(/send text to session/i)).toBeNull();
  });

  it('shows the input for an idle (live) session, not only running', () => {
    renderRow({
      session: { ...session('idle'), preview: 'x' },
      onPrompt: vi.fn(),
    });
    fireEvent.click(
      screen.getByRole('button', { name: /toggle session details/i }),
    );
    expect(screen.getByLabelText(/send text to session/i)).toBeTruthy();
  });

  it('hides the input for a stopped session even when onPrompt is set', () => {
    renderRow({
      session: { ...session('stopped'), preview: 'x' },
      onPrompt: vi.fn(),
    });
    fireEvent.click(
      screen.getByRole('button', { name: /toggle session details/i }),
    );
    expect(screen.queryByLabelText(/send text to session/i)).toBeNull();
  });

  it('forwards arrow/Enter keys as navigation while the box is empty', () => {
    const onSendKey = vi.fn();
    const onPrompt = vi.fn();
    renderRow({
      session: { ...session('running'), preview: 'x' },
      onPrompt,
      onSendKey,
    });
    fireEvent.click(
      screen.getByRole('button', { name: /toggle session details/i }),
    );
    const input = screen.getByLabelText(/send text to session/i);
    fireEvent.keyDown(input, { key: 'ArrowDown' });
    fireEvent.keyDown(input, { key: 'ArrowUp' });
    fireEvent.keyDown(input, { key: 'Enter' });
    fireEvent.keyDown(input, { key: 'Escape' });
    expect(onSendKey.mock.calls.map((c) => c[0])).toEqual([
      'down',
      'up',
      'enter',
      'escape',
    ]);
    // An empty-box Enter is a bare key, not a text submit.
    expect(onPrompt).not.toHaveBeenCalled();
  });

  it('treats keys as text editing once the box has content', () => {
    const onSendKey = vi.fn();
    const onPrompt = vi.fn();
    renderRow({
      session: { ...session('running'), preview: 'x' },
      onPrompt,
      onSendKey,
    });
    fireEvent.click(
      screen.getByRole('button', { name: /toggle session details/i }),
    );
    const input = screen.getByLabelText(
      /send text to session/i,
    ) as HTMLInputElement;
    fireEvent.change(input, { target: { value: 'hello' } });
    // With text present, ArrowDown edits the field (no key sent)...
    fireEvent.keyDown(input, { key: 'ArrowDown' });
    expect(onSendKey).not.toHaveBeenCalled();
    // ...and Enter submits the typed line via onPrompt.
    fireEvent.click(screen.getByRole('button', { name: /^send$/i }));
    expect(onPrompt).toHaveBeenCalledWith('hello');
  });

  it('limits the expanded preview to previewLines trailing lines', () => {
    renderRow({
      session: { ...session('running'), preview: 'a\nb\nc\nd\ne' },
      previewLines: 2,
    });
    fireEvent.click(
      screen.getByRole('button', { name: /toggle session details/i }),
    );
    const full = screen.getByTestId('session-preview-full');
    // Only the last two lines are shown.
    expect(full.textContent).toBe('d\ne');
  });

  it('does not forward keys when onSendKey is absent', () => {
    renderRow({
      session: { ...session('running'), preview: 'x' },
      onPrompt: vi.fn(),
    });
    fireEvent.click(
      screen.getByRole('button', { name: /toggle session details/i }),
    );
    const input = screen.getByLabelText(/send text to session/i);
    // No onSendKey handler → no hint and keydown is a no-op (no throw).
    expect(screen.queryByText(/navigate/i)).toBeNull();
    fireEvent.keyDown(input, { key: 'ArrowDown' });
  });
});

// A starting session is the placeholder the daemon holds while a provider brings
// a real one up (building a container). Its whole job is to say what is coming
// and show the provider's startup output while the user waits.
describe('SessionRow while starting', () => {
  const startingSession = (preview?: string): Session => ({
    id: 'starting-abc',
    title: 'Claude session',
    agent: 'Claude',
    state: 'starting',
    ...(preview === undefined ? {} : { preview }),
  });

  it('labels the state Starting and names the agent coming up', () => {
    renderRow({ session: startingSession() });
    expect(screen.getByTestId('session-state').textContent).toBe('Starting');
    expect(screen.getByText('Claude')).toBeTruthy();
  });

  // The output is the reason the row is on screen, so it must not be behind a
  // click the user has to discover.
  it('opens the output panel without being asked', () => {
    renderRow({ session: startingSession('=> [2/7] RUN apt-get install') });
    expect(screen.getByTestId('session-preview-full').textContent).toContain(
      '=> [2/7] RUN apt-get install',
    );
  });

  it('shows every line of the startup tail it was given', () => {
    const lines = Array.from({ length: 10 }, (_, i) => `build step ${i + 1}`);
    renderRow({ session: startingSession(lines.join('\n')) });
    const shown = screen.getByTestId('session-preview-full').textContent ?? '';
    for (const line of lines) expect(shown).toContain(line);
  });

  // The box shows ten lines, but the log behind it is scrollback: a startup log is
  // history to read upwards through, so the per-row display cap must not truncate
  // it. Without this the panel would keep only the newest `previewLines` and
  // scrolling up would hit the top almost immediately.
  it('keeps the whole startup log, past the display cap, to scroll back through', () => {
    const lines = Array.from({ length: 120 }, (_, i) => `build step ${i + 1}`);
    renderRow({
      session: startingSession(lines.join('\n')),
      previewLines: 10,
    });
    const shown = screen.getByTestId('session-preview-full').textContent ?? '';
    // The oldest line is as present as the newest.
    expect(shown).toContain('build step 1');
    expect(shown).toContain('build step 120');
    expect(shown.split('\n')).toHaveLength(120);
  });

  // The same cap still governs a *live* session, whose preview is a screen capture
  // rather than a log — there is no history to scroll back through there.
  it('still caps a live session to previewLines', () => {
    const lines = Array.from({ length: 120 }, (_, i) => `output ${i + 1}`);
    renderRow({
      session: { ...startingSession(lines.join('\n')), state: 'running' },
      previewLines: 10,
    });
    fireEvent.click(
      screen.getByRole('button', { name: 'Toggle session details' }),
    );
    const shown = screen.getByTestId('session-preview-full').textContent ?? '';
    expect(shown.split('\n')).toHaveLength(10);
    expect(shown).not.toContain('output 1\n');
    expect(shown).toContain('output 120');
  });

  // The panel is a scroll container in its own right, and reaching the top of a
  // build log with the keyboard requires it to be focusable.
  it('exposes the output as a focusable, labelled scroll region', () => {
    renderRow({ session: startingSession('=> [1/7] FROM debian') });
    const box = screen.getByTestId('session-preview-full');
    expect(box.getAttribute('tabindex')).toBe('0');
    expect(screen.getByLabelText('Session output')).toBe(box);
  });

  it('says it is starting when no output has arrived yet', () => {
    renderRow({ session: startingSession() });
    expect(screen.getByText('Starting…')).toBeTruthy();
  });

  it('cannot be opened — there is nothing to attach to yet', () => {
    renderRow({ session: startingSession(), onOpen: vi.fn() });
    expect(
      screen.getByRole('button', { name: 'Open' }).getAttribute('disabled'),
    ).not.toBeNull();
  });

  // No agent is listening yet, so the daemon would refuse the text.
  it('hides the send-text box until the session is live', () => {
    renderRow({
      session: startingSession('building'),
      onPrompt: vi.fn(),
      onSendKey: vi.fn(),
    });
    expect(screen.queryByLabelText('Send text to session')).toBeNull();
  });

  it('shows the send-text box once the session is running', () => {
    renderRow({
      session: { ...startingSession('output'), state: 'running' },
      onPrompt: vi.fn(),
    });
    // A running row starts collapsed (only a starting one is forced open), so the
    // panel has to be opened before its send box exists.
    fireEvent.click(
      screen.getByRole('button', { name: 'Toggle session details' }),
    );
    expect(screen.getByLabelText('Send text to session')).toBeTruthy();
  });

  // Only the *initial* expansion is forced: collapsing must stick, or the panel
  // fights the user on every state push while the build runs.
  it('lets the user collapse the panel again', () => {
    renderRow({ session: startingSession('building') });
    fireEvent.click(
      screen.getByRole('button', { name: 'Toggle session details' }),
    );
    expect(screen.queryByTestId('session-preview-full')).toBeNull();
  });

  // A live session keeps the old default: closed until asked.
  it('leaves a running session collapsed as before', () => {
    renderRow({ session: { ...startingSession('output'), state: 'running' } });
    expect(screen.queryByTestId('session-preview-full')).toBeNull();
  });
});

// A create that failed leaves its row behind, in `error`, carrying the reason.
// The row is the whole account of the attempt: nothing else records it, and
// nothing but removing it will clear it.
describe('SessionRow that failed to start', () => {
  const failedSession = (error?: string): Session => ({
    id: 'starting-abc',
    title: 'Claude Code session',
    agent: 'Claude Code',
    state: 'error',
    error,
  });

  it('shows the provider\u2019s reason on the row', () => {
    renderRow({ session: failedSession('unknown session option "no-such"') });
    expect(screen.getByTestId('session-error').textContent).toBe(
      'unknown session option "no-such"',
    );
    expect(screen.getByTestId('session-state').textContent).toBe('Error');
  });

  // The reason is the provider's; with none there is nothing honest to show, and
  // an invented line would be worse than the badge alone.
  it('shows no reason block when the daemon sent no message', () => {
    renderRow({ session: failedSession() });
    expect(screen.queryByTestId('session-error')).toBeNull();
  });

  it('cannot be opened, stopped or driven', () => {
    renderRow({
      session: failedSession('boom'),
      onOpen: vi.fn(),
      onStop: vi.fn(),
      onPrompt: vi.fn(),
    });
    expect(
      screen.getByRole('button', { name: 'Open' }).getAttribute('disabled'),
    ).not.toBeNull();
    expect(screen.queryByRole('button', { name: 'Stop' })).toBeNull();
    expect(screen.queryByLabelText('Send text to session')).toBeNull();
  });
});

// A starting or failed placeholder the daemon deletes itself. Deleting a
// starting one cancels a build that may have run for minutes, so it asks first;
// a failed one has nothing to lose.
it.each([
  ['starting', true],
  ['error', false],
] as const)('deletes a %s placeholder; Delete confirms: %s', (state, confirms) => {
  const onRemove = vi.fn();
  renderRow({ session: { ...session(state), id: 'starting-abc' }, onRemove });

  openMenu();
  fireEvent.click(screen.getByRole('menuitem', { name: 'Delete' }));
  const dialog = screen.queryByRole('dialog');
  expect(dialog !== null).toBe(confirms);
  if (dialog) {
    fireEvent.click(within(dialog).getByRole('button', { name: 'Delete' }));
  }
  expect(onRemove).toHaveBeenCalledTimes(1);
});

// Stop on a session still starting cancels its build, which takes the row:
// there is nothing yet to continue. As Delete does, it asks first.
it('stops a starting session by cancelling it, after a confirmation', () => {
  const props = { onStop: vi.fn(), onStart: vi.fn(), onRemove: vi.fn() };
  renderRow({
    session: { ...session('starting'), id: 'starting-abc', placeholder: true },
    ...props,
  });

  fireEvent.click(screen.getByRole('button', { name: 'Stop' }));
  const dialog = screen.getByRole('dialog', { name: /Stop starting/ });
  expect(props.onRemove).not.toHaveBeenCalled();
  fireEvent.click(within(dialog).getByRole('button', { name: 'Stop' }));
  expect(props.onRemove).toHaveBeenCalledTimes(1);
  expect(props.onStop).not.toHaveBeenCalled();
});

// A failed one has nothing to stop; a starting one, where its provider cannot
// stop sessions, would lose Stop again once it runs. A session restarting is
// starting too, but no placeholder: deleting it would delete the session.
it.each([
  ['a failed placeholder', 'error', true, { onStop: vi.fn() }],
  ['a starting one its provider cannot stop', 'starting', true, {}],
  ['a session restarting', 'starting', false, { onStop: vi.fn() }],
] as const)('offers %s no Stop or Continue', (_, state, placeholder, stop) => {
  renderRow({
    session: { ...session(state), id: 'sess-1', placeholder },
    onStart: vi.fn(),
    onRemove: vi.fn(),
    ...stop,
  });
  expect(
    screen.queryByRole('button', { name: /^(Stop|Continue)$/ }),
  ).toBeNull();
});

// Ended (stopped, or done by the daemon): recessed, and continued from Continue.
describe('SessionRow of an ended session', () => {
  const NOW = new Date('2026-11-10T12:00:00Z');
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['Date'] });
    vi.setSystemTime(NOW);
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  const ended = (state: 'stopped' | 'done', endedAt?: string): Session => ({
    ...session(state),
    preview: 'the last thing it said',
    endedAt,
  });

  it.each([
    ['stopped', '2026-11-10T11:00:00Z', 'Stopped', ' · 1 h ago'],
    ['done', '2026-11-10T11:48:00Z', 'Ended', ' · 12 min ago'],
    ['done', undefined, 'Ended', ''],
  ] as const)(
    'a %s one ended at %s reads "%s%s" by a hollow dot, without teaser or Open',
    (state, endedAt, word, ago) => {
      renderRow({ session: ended(state, endedAt), onOpen: vi.fn() });
      const row = screen.getByRole('listitem');
      expect(screen.getByTestId('session-state').textContent).toBe(word);
      expect(row.textContent).toContain(`${word}${ago}`);
      expect(row.textContent?.includes('ago')).toBe(ago !== '');
      expect(screen.getByRole('img', { name: 'Ended' })).toBeTruthy();
      expect(screen.queryByTestId('session-preview')).toBeNull();
      expect(screen.queryByRole('button', { name: 'Open' })).toBeNull();
    },
  );

  it.each(['stopped', 'done'] as const)(
    'continues a %s one from Continue only; a click on the row toggles details',
    (state) => {
      const onStart = vi.fn();
      renderRow({ session: ended(state), onStart });
      fireEvent.click(screen.getByTestId('session-title'));
      expect(onStart).not.toHaveBeenCalled();
      expect(screen.getByTestId('session-preview-full')).toBeTruthy();

      fireEvent.click(screen.getByRole('button', { name: 'Continue' }));
      expect(onStart).toHaveBeenCalledTimes(1);
    },
  );

  // On an ended row Enter shows its details, so Continue does not claim it.
  it('names only Alt+S on Continue in a navigable list', () => {
    renderRow({ session: ended('stopped'), onStart: vi.fn(), tabIndex: 0 });
    expect(
      screen.getByRole('button', { name: 'Continue' }).getAttribute('title'),
    ).toMatch(/^Continue this session \((Alt\+|⌥)S\)$/);
  });

  it('names the exact end time on the relative one', () => {
    const endedAt = '2026-11-10T11:48:00Z';
    renderRow({ session: ended('done', endedAt) });
    expect(screen.getByText('12 min ago').title).toBe(
      new Date(endedAt).toLocaleString(),
    );
  });

  it('moves on from "just now" soon after a minute, whenever it ended', () => {
    vi.useFakeTimers();
    vi.setSystemTime(NOW);
    // Another ended row starts the clock well before this session ends.
    render(
      <ul>
        <SessionRow
          session={{ ...ended('done', '2026-11-10T10:00:00Z'), id: 'old' }}
        />
      </ul>,
    );
    act(() => vi.advanceTimersByTime(50_000));
    renderRow({ session: ended('done', new Date().toISOString()) });
    expect(screen.getByText('just now')).toBeTruthy();

    act(() => vi.advanceTimersByTime(65_000));
    expect(screen.getByText('1 min ago')).toBeTruthy();
  });

  it('offers Continue in its menu as well', () => {
    renderRow({ session: ended('done'), onStart: vi.fn() });
    openMenu();
    expect(screen.getByRole('menuitem', { name: 'Continue' })).toBeTruthy();
  });
});

// Renaming: a session's title otherwise belongs to its provider, which re-reports
// it every cycle, so the row's job is only to collect the name and hand it over.
describe('SessionRow renaming', () => {
  const named = (): Session => ({
    id: 'sess-1',
    title: 'claude session',
    agent: 'Claude',
    state: 'running',
  });

  // Rename is a gesture on the name (double-click, F2, Alt+R), not a button.
  it('has no Rename button', () => {
    renderRow({ session: named(), onRename: vi.fn() });
    expect(screen.queryByRole('button', { name: /^Rename/ })).toBeNull();
  });

  // Alt+R reaches the name, which takes it as a rename rather than a click:
  // a click on it would toggle the details instead.
  it('starts renaming on Alt+R with the row selected', () => {
    function Mnemonics() {
      useMnemonics();
      return null;
    }
    render(
      <>
        <Mnemonics />
        <ul>
          <SessionRow session={named()} onRename={vi.fn()} selected />
        </ul>
      </>,
    );
    const title = screen.getByTestId('session-title');
    expect(title.getAttribute('data-mnemonic')).toBe('r');
    expect(title.getAttribute('aria-keyshortcuts')).toBe('Alt+R');

    fireEvent.keyDown(window, { key: 'r', code: 'KeyR', altKey: true });
    expect(screen.getByLabelText('Session name')).toBeTruthy();
    expect(
      screen
        .getByRole('button', { name: 'Toggle session details' })
        .getAttribute('aria-expanded'),
    ).toBe('false');
  });

  // A double-click's first click toggles the details like any single click (it
  // cannot wait to see whether a second follows); the rename puts them back.
  describe('a double-click on the title', () => {
    const expandedOf = () =>
      screen
        .getByRole('button', { name: 'Toggle session details' })
        .getAttribute('aria-expanded');
    const doubleClick = () => {
      const title = screen.getByTestId('session-title');
      fireEvent.click(title, { detail: 1 });
      fireEvent.click(title, { detail: 2 });
      fireEvent.doubleClick(title, { detail: 2 });
    };

    it.each([
      ['collapsed', false],
      ['expanded', true],
    ])('renames, and leaves the details %s', (_, open) => {
      renderRow({ session: named(), onRename: vi.fn() });
      if (open) {
        fireEvent.click(
          screen.getByRole('button', { name: 'Toggle session details' }),
          { detail: 1 },
        );
      }
      expect(expandedOf()).toBe(String(open));
      doubleClick();
      expect(screen.getByLabelText('Session name')).toBeTruthy();
      expect(expandedOf()).toBe(String(open));
    });

    it('keeps marking a done session seen', () => {
      const onSeen = vi.fn();
      renderRow({ session: session('done'), onRename: vi.fn(), onSeen });
      doubleClick();
      expect(onSeen).toHaveBeenCalledTimes(1);
      expect(screen.getByLabelText('Session name')).toBeTruthy();
      expect(expandedOf()).toBe('false');
    });

    // Nothing to rename, so nothing to undo: it toggles once, as it did.
    it('toggles once when the title cannot be renamed', () => {
      renderRow({
        session: named(),
        onRename: vi.fn(),
        disabledReason: 'Offline',
      });
      doubleClick();
      expect(screen.queryByLabelText('Session name')).toBeNull();
      expect(expandedOf()).toBe('true');
    });

    it('still lets a single click toggle at once', () => {
      renderRow({ session: named(), onRename: vi.fn() });
      const title = screen.getByTestId('session-title');
      fireEvent.click(title, { detail: 1 });
      expect(expandedOf()).toBe('true');
      fireEvent.click(title, { detail: 1 });
      expect(expandedOf()).toBe('false');
    });
  });

  it('does nothing on a double-click without a handler', () => {
    renderRow({ session: named() });
    fireEvent.doubleClick(screen.getByTestId('session-title'));
    expect(screen.queryByLabelText('Session name')).toBeNull();
  });

  it('swaps the title for an input, seeded with the current name', () => {
    renderRow({ session: named(), onRename: vi.fn() });
    fireEvent.doubleClick(screen.getByTestId('session-title'));
    const input = screen.getByLabelText('Session name') as HTMLInputElement;
    expect(input.value).toBe('claude session');
  });

  // Double-click is mouse-only, and it is now the only pointer route in, so the
  // keyboard needs its own — F2, as everywhere else that renames in place.
  it('starts renaming on F2 with the row focused', () => {
    renderRow({ session: named(), onRename: vi.fn() });
    fireEvent.keyDown(
      screen.getByRole('button', { name: /Toggle session details/ }),
      { key: 'F2' },
    );
    expect(screen.getByLabelText('Session name')).toBeTruthy();
  });

  // F2 is a shortcut *into* renaming, not a reset: pressed again with the box
  // open it used to reseed the draft from the session title and throw away an
  // uncommitted edit, which is the opposite of what F2 does anywhere else.
  it('leaves an open edit alone when F2 is pressed again', () => {
    renderRow({ session: named(), onRename: vi.fn() });
    fireEvent.doubleClick(screen.getByTestId('session-title'));
    const input = screen.getByLabelText('Session name') as HTMLInputElement;
    fireEvent.change(input, { target: { value: 'half-typed' } });

    fireEvent.keyDown(
      screen.getByRole('button', { name: /Toggle session details/ }),
      { key: 'F2' },
    );
    expect(
      (screen.getByLabelText('Session name') as HTMLInputElement).value,
    ).toBe('half-typed');
  });

  it('advertises the gesture on the name, so it is discoverable', () => {
    renderRow({ session: named(), onRename: vi.fn() });
    expect(screen.getByTestId('session-title').getAttribute('title')).toBe(
      'claude session\n\nDouble-click to rename (or press F2)',
    );
  });

  // The hover tint says the name can be renamed, so the name carries the class
  // that draws it, and its letter, only when it can.
  it('marks the name as renamable, for the hover tint', () => {
    renderRow({ session: named(), onRename: vi.fn() });
    const title = screen.getByTestId('session-title');
    expect(title.className).toContain('renamable');
    expect(title.hasAttribute('aria-disabled')).toBe(false);
  });

  it('does not mark a name that cannot be renamed', () => {
    renderRow({ session: named() });
    const title = screen.getByTestId('session-title');
    expect(title.className).not.toContain('renamable');
    expect(title.hasAttribute('data-mnemonic')).toBe(false);
  });

  it('commits the new name on Enter', () => {
    const onRename = vi.fn();
    renderRow({ session: named(), onRename });
    fireEvent.doubleClick(screen.getByTestId('session-title'));
    const input = screen.getByLabelText('Session name');
    fireEvent.change(input, { target: { value: 'Port the CLI' } });
    fireEvent.keyDown(input, { key: 'Enter' });
    expect(onRename).toHaveBeenCalledWith('Port the CLI');
    // Back to the plain row.
    expect(screen.queryByLabelText('Session name')).toBeNull();
  });

  it('commits on blur, so clicking away is not a lost edit', () => {
    const onRename = vi.fn();
    renderRow({ session: named(), onRename });
    fireEvent.doubleClick(screen.getByTestId('session-title'));
    const input = screen.getByLabelText('Session name');
    fireEvent.change(input, { target: { value: 'Port the CLI' } });
    fireEvent.blur(input);
    expect(onRename).toHaveBeenCalledWith('Port the CLI');
  });

  it('trims the name before handing it over', () => {
    const onRename = vi.fn();
    renderRow({ session: named(), onRename });
    fireEvent.doubleClick(screen.getByTestId('session-title'));
    const input = screen.getByLabelText('Session name');
    fireEvent.change(input, { target: { value: '  spaced  ' } });
    fireEvent.keyDown(input, { key: 'Enter' });
    expect(onRename).toHaveBeenCalledWith('spaced');
  });

  it('abandons the edit on Escape', () => {
    const onRename = vi.fn();
    renderRow({ session: named(), onRename });
    fireEvent.doubleClick(screen.getByTestId('session-title'));
    const input = screen.getByLabelText('Session name');
    fireEvent.change(input, { target: { value: 'never mind' } });
    fireEvent.keyDown(input, { key: 'Escape' });
    expect(onRename).not.toHaveBeenCalled();
    expect(screen.getByText('claude session')).toBeTruthy();
  });

  // An emptied box is not a no-op: it asks for the provider's own name back.
  it('reports an emptied box as the empty string (reset to the default)', () => {
    const onRename = vi.fn();
    renderRow({ session: named(), onRename });
    fireEvent.doubleClick(screen.getByTestId('session-title'));
    const input = screen.getByLabelText('Session name');
    fireEvent.change(input, { target: { value: '   ' } });
    fireEvent.keyDown(input, { key: 'Enter' });
    expect(onRename).toHaveBeenCalledWith('');
  });

  it('does not call the handler when the name did not change', () => {
    const onRename = vi.fn();
    renderRow({ session: named(), onRename });
    fireEvent.doubleClick(screen.getByTestId('session-title'));
    fireEvent.keyDown(screen.getByLabelText('Session name'), { key: 'Enter' });
    expect(onRename).not.toHaveBeenCalled();
  });

  // The row stands for a session that does not exist yet, so there is nothing to
  // name — the same reason Remove is withheld while starting.
  it('withholds rename from a starting placeholder', () => {
    renderRow({
      session: { ...named(), id: 'starting-abc', state: 'starting' },
      onRename: vi.fn(),
    });
    fireEvent.doubleClick(screen.getByTestId('session-title'));
    expect(screen.queryByLabelText('Session name')).toBeNull();
  });
});

// Offline handling: every action on the row is a daemon call, so they all go
// disabled — but stay visible — when the app reports it cannot reach the daemon.
// The row is told a *reason*, not a transport state (this package is
// transport-agnostic), and that reason is the tooltip.
describe('SessionRow when the daemon is unreachable', () => {
  const REASON = 'The daemon is offline';

  const renderBlocked = (overrides: Partial<Session> = {}) =>
    renderRow({
      session: { ...session('running'), ...overrides },
      onOpen: vi.fn(),
      onStop: vi.fn(),
      onRemove: vi.fn(),
      onRename: vi.fn(),
      onPrompt: vi.fn(),
      onSendKey: vi.fn(),
      disabledReason: REASON,
    });

  it('disables every daemon-backed action but keeps it on screen', () => {
    renderBlocked();
    expect(
      (screen.getByRole('button', { name: 'Open' }) as HTMLButtonElement)
        .disabled,
    ).toBe(true);
    // Stop only goes inert: it is the button that keeps focus across a flip.
    expect(
      screen
        .getByRole('button', { name: 'Stop' })
        .getAttribute('aria-disabled'),
    ).toBe('true');
    // The menu leaves out what cannot run.
    openMenu();
    expect(screen.queryByRole('menuitem', { name: 'Delete' })).toBeNull();
  });

  // Rename has no button to disable, so what has to hold is that the gesture does
  // nothing — and that the name says why instead of advertising it.
  it('ignores a double-click on the name, and says why', () => {
    renderBlocked();
    fireEvent.doubleClick(screen.getByTestId('session-title'));
    expect(screen.queryByLabelText('Session name')).toBeNull();
    expect(screen.getByTestId('session-title').getAttribute('title')).toBe(
      `Implement self-update\n\n${REASON}`,
    );
  });

  // Nothing should invite a rename that cannot happen: a disabled name has no
  // hover tint, and Alt+R does not reach it.
  // Without its letter, Alt+R does not reach it.
  it('takes the letter off the name', () => {
    renderBlocked();
    expect(
      screen.getByTestId('session-title').hasAttribute('data-mnemonic'),
    ).toBe(false);
  });

  it('ignores F2 as well', () => {
    renderBlocked();
    fireEvent.keyDown(
      screen.getByRole('button', { name: /Toggle session details/ }),
      { key: 'F2' },
    );
    expect(screen.queryByLabelText('Session name')).toBeNull();
  });

  it('explains why on every disabled action', () => {
    renderBlocked();
    for (const name of [/^Open$/, /^Stop$/]) {
      expect(screen.getByRole('button', { name }).getAttribute('title')).toBe(
        REASON,
      );
    }
  });

  it('offers Continue on a stopped session, disabled', () => {
    const onStart = vi.fn();
    renderRow({
      session: session('stopped'),
      onStart,
      disabledReason: REASON,
    });
    const start = screen.getByRole('button', { name: 'Continue' });
    expect(start.getAttribute('aria-disabled')).toBe('true');
    expect(start.getAttribute('title')).toBe(REASON);
  });

  // Reading is not acting: the disclosure and the output panel are local, and a
  // dead daemon is exactly when someone wants to look at the last output.
  it('still opens the details panel', () => {
    renderBlocked({ preview: 'the last thing it said' });
    fireEvent.click(
      screen.getByRole('button', { name: 'Toggle session details' }),
    );
    expect(screen.getByTestId('session-preview-full').textContent).toContain(
      'the last thing it said',
    );
  });

  // The box exists to feed a request, so it goes with the button rather than
  // inviting typing that has nowhere to go.
  it('makes the send-text box inert as well as Send', () => {
    renderRow({
      session: session('running'),
      onPrompt: vi.fn(),
      disabledReason: REASON,
    });
    fireEvent.click(
      screen.getByRole('button', { name: 'Toggle session details' }),
    );
    const box = screen.getByLabelText(
      'Send text to session',
    ) as HTMLInputElement;
    expect(box.readOnly).toBe(true);
    expect(box.getAttribute('aria-disabled')).toBe('true');
    expect(box.getAttribute('title')).toBe(REASON);
    expect(
      screen
        .getByRole('button', { name: 'Send' })
        .getAttribute('aria-disabled'),
    ).toBe('true');
  });

  // A prompt typed before the drop is still there, and still focused, to send
  // once the daemon is back.
  it('keeps what was already typed in the box, and its focus', () => {
    const { rerender } = renderRow({
      session: session('running'),
      onPrompt: vi.fn(),
    });
    fireEvent.click(
      screen.getByRole('button', { name: 'Toggle session details' }),
    );
    const typed = screen.getByLabelText('Send text to session');
    fireEvent.change(typed, { target: { value: 'typed before the drop' } });
    typed.focus();
    rerender(
      <ul>
        <SessionRow
          session={session('running')}
          onPrompt={vi.fn()}
          disabledReason={REASON}
        />
      </ul>,
    );
    const box = screen.getByLabelText(
      'Send text to session',
    ) as HTMLInputElement;
    expect(box.readOnly).toBe(true);
    expect(box.value).toBe('typed before the drop');
    expect(document.activeElement).toBe(box);
  });

  it('does not send the line on Enter while blocked', () => {
    const onPrompt = vi.fn();
    renderRow({
      session: session('running'),
      onPrompt,
      disabledReason: REASON,
    });
    fireEvent.click(
      screen.getByRole('button', { name: 'Toggle session details' }),
    );
    const box = screen.getByLabelText('Send text to session');
    fireEvent.change(box, { target: { value: 'nope' } });
    fireEvent.submit(box.closest('form') as HTMLFormElement);
    expect(onPrompt).not.toHaveBeenCalled();
  });

  // The empty box is a key-navigation surface; those keys are calls too.
  it('does not forward navigation keys while blocked', () => {
    const onSendKey = vi.fn();
    renderRow({
      session: session('running'),
      onPrompt: vi.fn(),
      onSendKey,
      disabledReason: REASON,
    });
    fireEvent.click(
      screen.getByRole('button', { name: 'Toggle session details' }),
    );
    const box = screen.getByLabelText('Send text to session');
    fireEvent.keyDown(box, { key: 'ArrowDown' });
    fireEvent.keyDown(box, { key: 'Enter' });
    expect(onSendKey).not.toHaveBeenCalled();
    // And the hint promising that behaviour is not shown while it is off.
    expect(screen.queryByText(/Empty box:/)).toBeNull();
  });

  // Losing the daemon mid-rename: the box goes dead, and because disabling a
  // focused input blurs it, the commit-on-blur must not fire the request.
  it('disables an open rename box without committing it', () => {
    const onRename = vi.fn();
    const { rerender } = renderRow({ session: session('running'), onRename });
    fireEvent.doubleClick(screen.getByTestId('session-title'));
    fireEvent.change(screen.getByLabelText('Session name'), {
      target: { value: 'half-typed' },
    });

    rerender(
      <ul>
        <SessionRow
          session={session('running')}
          onRename={onRename}
          disabledReason={REASON}
        />
      </ul>,
    );
    const box = screen.getByLabelText('Session name') as HTMLInputElement;
    expect(box.disabled).toBe(true);

    fireEvent.blur(box);
    expect(onRename).not.toHaveBeenCalled();
  });

  it('leaves everything enabled once the reason is gone', () => {
    renderRow({
      session: session('running'),
      onOpen: vi.fn(),
      onStop: vi.fn(),
      onRename: vi.fn(),
    });
    for (const name of [/^Open$/, /^Stop$/]) {
      expect(
        (screen.getByRole('button', { name }) as HTMLButtonElement).disabled,
      ).toBe(false);
    }
    // And the name takes the gesture again.
    fireEvent.doubleClick(screen.getByTestId('session-title'));
    expect(screen.getByLabelText('Session name')).toBeTruthy();
  });
});

// The session's own output must not read as HomeShell's own words. A tail line
// like "Press enter to continue" sat as loose text at card level and was taken
// for the app asking the user to press enter; the expanded box was also the same
// rectangle as the send-text input right below it. The surface does most of the
// work (see the stylesheet); this is the part that is behaviour rather than paint.
describe('SessionRow: session output is marked as the session speaking', () => {
  const withOutput = (): Session => ({
    ...session('running'),
    preview: 'building…\nPress enter to continue',
  });

  it('captions the expanded output box, and the caption names it', () => {
    renderRow({ session: withOutput() });
    fireEvent.click(
      screen.getByRole('button', { name: 'Toggle session details' }),
    );

    // On screen, not only in the accessibility tree.
    expect(screen.getByText('Session output')).toBeTruthy();
    // …and it is the box's accessible name, so it is announced once, not twice.
    expect(screen.getByLabelText('Session output')).toBe(
      screen.getByTestId('session-preview-full'),
    );
  });

  it('does not caption an empty panel — there is no output to attribute', () => {
    renderRow({ session: session('running') });
    fireEvent.click(
      screen.getByRole('button', { name: 'Toggle session details' }),
    );

    expect(screen.queryByText('Session output')).toBeNull();
    // What is there instead is HomeShell talking, and stays HomeShell's own text.
    expect(screen.getByText('No output captured yet.')).toBeTruthy();
  });

  it('keeps the collapsed teaser as the session tail, not a caption on the row', () => {
    renderRow({ session: withOutput() });
    const teaser = screen.getByTestId('session-preview');
    expect(teaser.textContent).toBe('Press enter to continue');
  });
});

// A rename the daemon refuses (a sibling row already shows that title). The
// answer belongs against this row and this box — not on a page-level error line
// somewhere else, and not at the cost of what the user typed.
describe('SessionRow: a refused rename', () => {
  const named = (): Session => ({ ...session('idle'), title: 'Claude Code' });
  const refuse = (message: string) => vi.fn().mockResolvedValue(message);

  const rename = (to: string) => {
    fireEvent.doubleClick(screen.getByTestId('session-title'));
    const input = screen.getByLabelText('Session name');
    fireEvent.change(input, { target: { value: to } });
    fireEvent.keyDown(input, { key: 'Enter' });
  };

  it('warns beside the row instead of anywhere else', async () => {
    renderRow({
      session: named(),
      onRename: refuse('a session in this workstream is already called that'),
    });
    rename('Review');

    const warning = await screen.findByTestId('session-warning');
    expect(warning.textContent).toContain('already called that');
  });

  it('keeps the box open on the rejected value, so it can be fixed', async () => {
    renderRow({ session: named(), onRename: refuse('taken') });
    rename('Review');

    await screen.findByTestId('session-warning');
    const box = screen.getByLabelText('Session name') as HTMLInputElement;
    expect(box.value).toBe('Review');
  });

  it('clears the warning when the next attempt succeeds', async () => {
    const onRename = vi
      .fn()
      .mockResolvedValueOnce('taken')
      .mockResolvedValueOnce(null);
    renderRow({ session: named(), onRename });
    rename('Review');
    await screen.findByTestId('session-warning');

    const box = screen.getByLabelText('Session name');
    fireEvent.change(box, { target: { value: 'Review 2' } });
    fireEvent.keyDown(box, { key: 'Enter' });

    await waitFor(() =>
      expect(screen.queryByTestId('session-warning')).toBeNull(),
    );
    // …and the box closed, which is what success looks like.
    expect(screen.queryByLabelText('Session name')).toBeNull();
  });

  // The warning is about a value the box no longer holds, so editing retires it.
  // Leaving it up contradicts the row: a complaint about "Review" sitting beside
  // a box that now reads "Review 2".
  it('clears the warning as soon as the value is edited', async () => {
    renderRow({ session: named(), onRename: refuse('taken') });
    rename('Review');
    await screen.findByTestId('session-warning');

    fireEvent.change(screen.getByLabelText('Session name'), {
      target: { value: 'Review 2' },
    });
    expect(screen.queryByTestId('session-warning')).toBeNull();
  });

  it('clears the warning when the user starts over', async () => {
    renderRow({ session: named(), onRename: refuse('taken') });
    rename('Review');
    await screen.findByTestId('session-warning');

    // Escape abandons; the stale complaint goes with it.
    fireEvent.keyDown(screen.getByLabelText('Session name'), { key: 'Escape' });
    expect(screen.queryByTestId('session-warning')).toBeNull();
  });

  it('shows no warning when the rename is accepted', async () => {
    const onRename = vi.fn().mockResolvedValue(null);
    renderRow({ session: named(), onRename });
    rename('Review');

    await waitFor(() =>
      expect(screen.queryByLabelText('Session name')).toBeNull(),
    );
    expect(screen.queryByTestId('session-warning')).toBeNull();
  });
});

describe('SessionRow as a keyboard list item', () => {
  const focusedRow = (props: Parameters<typeof SessionRow>[0]) => {
    renderRow({ tabIndex: 0, ...props });
    const row = screen.getByRole('listitem');
    row.focus();
    return row;
  };

  it('is focusable only when its list hands it a tab index', () => {
    renderRow({ session: session('running') });
    expect(screen.getByRole('listitem').hasAttribute('tabindex')).toBe(false);
    renderRow({ session: session('running'), tabIndex: -1 });
    expect(screen.getAllByRole('listitem')[1].getAttribute('tabindex')).toBe(
      '-1',
    );
  });

  // Tab from the row holding the tab stop walks its controls and then leaves
  // the list, rather than going on through every other row's.
  it.each([
    [-1, ['-1', '-1', '-1', '-1', '-1']],
    [0, [null, null, null, null, null]],
  ])('gives its controls tab index %s', (tabIndex, expected) => {
    renderRow({
      session: session('running'),
      tabIndex,
      onOpen: vi.fn(),
      onStop: vi.fn(),
      onRemove: vi.fn(),
    });
    const controls = within(screen.getByRole('listitem')).getAllByRole(
      'button',
    );
    expect(controls.map((control) => control.getAttribute('tabindex'))).toEqual(
      expected,
    );
  });

  it.each([
    ['running', true],
    ['idle', true],
    ['unknown', true],
    ['stopped', false],
  ] as const)('opens a %s session on Enter: %s', (state, opens) => {
    const onOpen = vi.fn();
    fireEvent.keyDown(focusedRow({ session: session(state), onOpen }), {
      key: 'Enter',
    });
    expect(onOpen).toHaveBeenCalledTimes(opens ? 1 : 0);
  });

  it("shows an ended session's details on Enter, without continuing it", () => {
    const onStart = vi.fn();
    fireEvent.keyDown(focusedRow({ session: session('stopped'), onStart }), {
      key: 'Enter',
    });
    expect(onStart).not.toHaveBeenCalled();
    expect(
      screen
        .getByRole('button', { name: 'Toggle session details' })
        .getAttribute('aria-expanded'),
    ).toBe('true');
  });

  it.each([
    ['Delete', {}, 'Linux'],
    ['Cmd+Backspace', { key: 'Backspace', metaKey: true }, 'Macintosh'],
  ])('asks before removing on %s', (key, modifiers, platform) => {
    const userAgent = vi
      .spyOn(navigator, 'userAgent', 'get')
      .mockReturnValue(platform);
    const onRemove = vi.fn();
    fireEvent.keyDown(focusedRow({ session: session('running'), onRemove }), {
      key,
      ...modifiers,
    });
    userAgent.mockRestore();
    expect(
      screen.getByRole('dialog', { name: 'Delete "Implement self-update"?' }),
    ).toBeTruthy();
    expect(onRemove).not.toHaveBeenCalled();
  });

  it.each([
    ['Ctrl', { ctrlKey: true }],
    ['Shift', { shiftKey: true }],
    ['Alt', { altKey: true }],
    ['Cmd', { metaKey: true }],
  ])('leaves %s+Enter to the browser', (_modifier, modifiers) => {
    const onOpen = vi.fn();
    fireEvent.keyDown(focusedRow({ session: session('running'), onOpen }), {
      key: 'Enter',
      ...modifiers,
    });
    expect(onOpen).not.toHaveBeenCalled();
  });

  it('announces the keys it answers', () => {
    const row = focusedRow({ session: session('running') });
    expect(row.getAttribute('aria-keyshortcuts')).toBe(
      'ArrowRight ArrowLeft Enter F2 Delete Shift+F10',
    );
  });

  it('is marked as current while it is the selection', () => {
    const row = focusedRow({ session: session('running'), selected: true });
    expect(row.getAttribute('aria-current')).toBe('true');
  });

  it('opens once for a held Enter', () => {
    const onOpen = vi.fn();
    const row = focusedRow({ session: session('running'), onOpen });
    fireEvent.keyDown(row, { key: 'Enter' });
    fireEvent.keyDown(row, { key: 'Enter', repeat: true });
    expect(onOpen).toHaveBeenCalledTimes(1);
  });

  // A failed session is removed without confirmation, so a held Delete would
  // go on to the row that takes its place.
  it('removes once for a held Delete', () => {
    const onRemove = vi.fn();
    const row = focusedRow({ session: session('error'), onRemove });
    fireEvent.keyDown(row, { key: 'Delete' });
    fireEvent.keyDown(row, { key: 'Delete', repeat: true });
    expect(onRemove).toHaveBeenCalledTimes(1);
  });

  // A click on its output focuses the row too, but with no ring to show it.
  it('ignores its keys when focus came from a click', () => {
    const onRemove = vi.fn();
    const row = focusedRow({
      session: session('error'),
      onRemove,
      onRename: vi.fn(),
    });
    row.blur();
    fireEvent.pointerDown(row);
    row.focus();
    fireEvent.keyDown(row, { key: 'Delete' });
    fireEvent.keyDown(row, { key: 'F2' });
    expect(onRemove).not.toHaveBeenCalled();
    expect(screen.queryByLabelText('Session name')).toBeNull();
  });

  it('ignores a plain Backspace', () => {
    fireEvent.keyDown(
      focusedRow({ session: session('running'), onRemove: vi.fn() }),
      { key: 'Backspace' },
    );
    expect(screen.queryByRole('dialog')).toBeNull();
  });

  it('expands on Right and collapses on Left', () => {
    const row = focusedRow({ session: session('running') });
    const toggle = screen.getByRole('button', {
      name: 'Toggle session details',
    });
    fireEvent.keyDown(row, { key: 'ArrowRight' });
    expect(toggle.getAttribute('aria-expanded')).toBe('true');
    fireEvent.keyDown(row, { key: 'ArrowLeft' });
    expect(toggle.getAttribute('aria-expanded')).toBe('false');
  });

  // A collapsed row leaves Left to the list, which moves to the card.
  it('leaves Left unhandled while collapsed', () => {
    const row = focusedRow({ session: session('running') });
    expect(fireEvent.keyDown(row, { key: 'ArrowLeft' })).toBe(true);
  });

  it('marks a done session seen when expanded by keyboard', () => {
    const onSeen = vi.fn();
    fireEvent.keyDown(
      focusedRow({ session: session('idle'), displayState: 'done', onSeen }),
      { key: 'ArrowRight' },
    );
    expect(onSeen).toHaveBeenCalled();
  });

  // Enter on a button inside the row is that button's click, not an Open.
  it('leaves keys pressed on its own controls alone', () => {
    const onOpen = vi.fn();
    focusedRow({ session: session('running'), onOpen });
    fireEvent.keyDown(screen.getByRole('button', { name: 'Open' }), {
      key: 'Enter',
    });
    expect(onOpen).not.toHaveBeenCalled();
  });

  // Delete then Enter must not remove a running session.
  it('opens its Remove dialog on Cancel', () => {
    fireEvent.keyDown(
      focusedRow({ session: session('running'), onRemove: vi.fn() }),
      { key: 'Delete' },
    );
    expect(document.activeElement).toBe(
      within(screen.getByRole('dialog')).getByRole('button', {
        name: 'Cancel',
      }),
    );
  });

  // The dialog is inside the row, so the row's keys would reach behind it.
  it('does not rename behind its Remove dialog on F2', () => {
    fireEvent.keyDown(
      focusedRow({
        session: session('running'),
        onRemove: vi.fn(),
        onRename: vi.fn(),
      }),
      { key: 'Delete' },
    );
    fireEvent.keyDown(document.activeElement as HTMLElement, { key: 'F2' });
    expect(screen.queryByLabelText('Session name')).toBeNull();
  });

  it('does nothing while the daemon is unreachable', () => {
    const onOpen = vi.fn();
    const row = focusedRow({
      session: session('running'),
      onOpen,
      onRemove: vi.fn(),
      disabledReason: 'Offline',
    });
    fireEvent.keyDown(row, { key: 'Enter' });
    fireEvent.keyDown(row, { key: 'Delete' });
    expect(onOpen).not.toHaveBeenCalled();
    expect(screen.queryByRole('dialog')).toBeNull();
  });

  it.each(['Enter', 'Escape'])(
    'returns focus to the row when a rename is left with %s',
    async (key) => {
      const row = focusedRow({
        session: session('running'),
        onRename: vi.fn(),
      });
      fireEvent.keyDown(row, { key: 'F2' });
      fireEvent.keyDown(screen.getByLabelText('Session name'), { key });
      await waitFor(() => expect(document.activeElement).toBe(row));
    },
  );
});

describe('SessionRow restart with settings', () => {
  const actionsOf = (props: Omit<Parameters<typeof SessionRow>[0], 'ref'>) => {
    const ref = createRef<SessionRowHandle>();
    renderRow({ ...props, ref });
    return ref.current?.actions() ?? [];
  };

  it('offers it, and runs it, when set', () => {
    const onRestartWithSettings = vi.fn();
    const restart = actionsOf({
      session: session('running'),
      onRestartWithSettings,
    }).find((action) => action.id === 'restartWithSettings');
    expect(restart?.label).toBe('Restart with settings…');
    restart?.run();
    expect(onRestartWithSettings).toHaveBeenCalled();
  });

  it('offers it on a stopped session and on one whose restart failed', () => {
    for (const state of ['stopped', 'error'] as const) {
      expect(
        actionsOf({
          session: session(state),
          onRestartWithSettings: vi.fn(),
        }).map((action) => action.id),
      ).toContain('restartWithSettings');
    }
  });

  // Whether a session can be restarted is the daemon's call (its restartable
  // flag, which the card turns into the prop), not the row's.
  it('offers it on a starting session when set', () => {
    expect(
      actionsOf({
        session: session('starting'),
        onRestartWithSettings: vi.fn(),
      }).map((action) => action.id),
    ).toContain('restartWithSettings');
  });

  it('leaves it out unset', () => {
    expect(
      actionsOf({ session: session('running') }).map((action) => action.id),
    ).not.toContain('restartWithSettings');
  });

  it('is disabled while the daemon is unreachable', () => {
    const restart = actionsOf({
      session: session('running'),
      onRestartWithSettings: vi.fn(),
      disabledReason: 'Offline',
    }).find((action) => action.id === 'restartWithSettings');
    expect(restart?.disabled).toBe(true);
  });
});

describe('SessionRow outdated settings', () => {
  const outdated = {
    ...session('running'),
    outdatedSettings: ['allow_domains', 'features'],
  };

  it('names the settings that changed, and how to apply them', () => {
    renderRow({ session: outdated, onRestartWithSettings: vi.fn() });
    expect(screen.getByTestId('session-outdated').textContent).toBe(
      'Settings changed since it started: allow_domains, features. Restart it with settings to apply them.',
    );
  });

  it('does not offer a restart the session cannot have', () => {
    renderRow({ session: outdated });
    expect(screen.getByTestId('session-outdated').textContent).toBe(
      'Settings changed since it started: allow_domains, features.',
    );
  });

  it('says nothing for a session that is up to date', () => {
    renderRow({ session: session('running') });
    expect(screen.queryByTestId('session-outdated')).toBeNull();
  });
});

describe('SessionRow running in a terminal', () => {
  const warning = 'Stopping it ends the session there too.';
  const takeOverWarning = 'This ends it there and continues it here.';
  const note = 'Running elsewhere.';
  const terminal: Session = {
    ...session('running'),
    restartable: true,
    capabilities: {
      live: false,
      takeOver: true,
      stopWarning: warning,
      takeOverWarning,
      note,
    },
  };

  it('shows the note its provider sends', () => {
    renderRow({ session: terminal });
    expect(screen.getByTestId('session-note').textContent).toBe(note);
    cleanup();
    renderRow({ session: session('running') });
    expect(screen.queryByTestId('session-note')).toBeNull();
  });

  it('offers no send box for a session HomeShell cannot type into', () => {
    renderRow({ session: terminal, onPrompt: vi.fn() });
    fireEvent.click(
      screen.getByRole('button', { name: 'Toggle session details' }),
    );
    expect(
      screen.queryByRole('textbox', { name: 'Send text to session' }),
    ).toBeNull();
  });

  it('confirms Stop with the session warning', () => {
    const onStop = vi.fn();
    renderRow({ session: terminal, onStop });
    fireEvent.click(screen.getByRole('button', { name: 'Stop' }));
    const dialog = screen.getByRole('dialog', {
      name: 'Stop "Implement self-update"?',
    });
    expect(within(dialog).getByText(warning)).toBeTruthy();
    expect(onStop).not.toHaveBeenCalled();
    fireEvent.click(within(dialog).getByRole('button', { name: /Stop/ }));
    expect(onStop).toHaveBeenCalledOnce();
  });

  it('stops at once without a warning', () => {
    const onStop = vi.fn();
    renderRow({ session: session('running'), onStop });
    fireEvent.click(screen.getByRole('button', { name: 'Stop' }));
    expect(onStop).toHaveBeenCalledOnce();
  });

  it('warns on delete that the session ends in its terminal too', () => {
    renderRow({ session: terminal, onRemove: vi.fn() });
    openMenu();
    fireEvent.click(screen.getByRole('menuitem', { name: /Delete/ }));
    const dialog = screen.getByRole('dialog', {
      name: 'Delete "Implement self-update"?',
    });
    expect(within(dialog).getByText(warning)).toBeTruthy();
  });

  it('confirms Restart in HomeShell first', () => {
    const onTakeOver = vi.fn();
    renderRow({ session: terminal, onTakeOver });
    fireEvent.click(
      screen.getByRole('button', { name: 'Restart in HomeShell' }),
    );
    const dialog = screen.getByRole('dialog', {
      name: 'Restart "Implement self-update" in HomeShell?',
    });
    expect(within(dialog).getByText(takeOverWarning)).toBeTruthy();
    expect(onTakeOver).not.toHaveBeenCalled();
    fireEvent.click(
      within(dialog).getByRole('button', { name: 'Restart in HomeShell' }),
    );
    expect(onTakeOver).toHaveBeenCalledOnce();
  });

  it('does not offer Restart in HomeShell unless the session allows a take-over', () => {
    renderRow({
      session: { ...terminal, capabilities: { live: false, takeOver: false } },
      onTakeOver: vi.fn(),
    });
    expect(
      screen.queryByRole('button', { name: 'Restart in HomeShell' }),
    ).toBeNull();
  });

  it('stays busy while Restart in HomeShell runs, so it cannot be posted twice', async () => {
    let finish: () => void = () => undefined;
    const onTakeOver = vi.fn(
      () => new Promise<void>((resolve) => (finish = resolve)),
    );
    renderRow({ session: terminal, onTakeOver });
    const restart = () =>
      screen.getByRole('button', { name: 'Restart in HomeShell' });
    fireEvent.click(restart());
    fireEvent.click(
      within(screen.getByRole('dialog')).getByRole('button', {
        name: 'Restart in HomeShell',
      }),
    );
    expect(restart().getAttribute('aria-disabled')).toBe('true');
    fireEvent.click(restart());
    expect(screen.queryByRole('dialog')).toBeNull();
    finish();
    await waitFor(() =>
      expect(restart().getAttribute('aria-disabled')).not.toBe('true'),
    );
    expect(onTakeOver).toHaveBeenCalledOnce();
  });

  it('does not repeat the warning when deleting an ended row', () => {
    renderRow({
      session: { ...terminal, state: 'stopped' },
      onRemove: vi.fn(),
    });
    openMenu();
    fireEvent.click(screen.getByRole('menuitem', { name: /Delete/ }));
    expect(within(screen.getByRole('dialog')).queryByText(warning)).toBeNull();
  });
});
