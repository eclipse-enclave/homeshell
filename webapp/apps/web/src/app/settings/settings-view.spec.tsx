import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react';
import type { Provider } from '@homeshell/model';

import { CATEGORIES, SettingsView } from './settings-view';
import { SettingsProvider } from './use-settings';
import { DEFAULT_MAX_RECORDS } from '../logs/use-log-stream';
import { ThemeProvider } from '../use-theme';
import { playNotificationSound } from '../notification-sound';
import { terminalEmulatorsResponse } from '../../testing/terminal-emulators';

// Sound playback is a Web Audio side effect (no-op in jsdom); the view's
// contract is only *that* it asks for the chosen sound.
vi.mock('../notification-sound', () => ({
  playNotificationSound: vi.fn(),
}));

const providers: Provider[] = [
  {
    id: 'local',
    label: 'Local System',
    status: { state: 'available' },
    capabilities: {
      openModes: [],
      startStop: false,
      oneOffPrompt: false,
      sessionStartStop: false,
      sessionRestart: false,
      maxConcurrentSessions: 0,
      detectsWaiting: false,
    },
  },
];

interface Options {
  category?: string;
  providers?: Provider[];
  logLevel?: string;
  onLogLevelChange?: (level: string) => void;
  onSelectCategory?: (id: string) => void;
  logBufferSize?: number;
  onLogBufferSizeChange?: (size: number) => void;
  disabledReason?: string;
}

// PUT /api/settings bodies captured during a test.
let puts: Record<string, unknown>[] = [];
// POST /api/notifications/test bodies captured during a test.
let notificationTests: Record<string, unknown>[] = [];

function renderView(opts: Options = {}) {
  return render(viewFor(opts));
}

function viewFor(opts: Options) {
  return (
    <ThemeProvider>
      <SettingsProvider>
        <SettingsView
          onClose={vi.fn()}
          category={opts.category}
          onSelectCategory={opts.onSelectCategory ?? vi.fn()}
          providers={opts.providers ?? []}
          logLevel={opts.logLevel}
          onLogLevelChange={opts.onLogLevelChange}
          logBufferSize={opts.logBufferSize}
          onLogBufferSizeChange={opts.onLogBufferSizeChange}
          disabledReason={opts.disabledReason}
        />
      </SettingsProvider>
    </ThemeProvider>
  );
}

/** The most recent settings written to the daemon. */
function lastPut() {
  return puts.at(-1) as Record<string, Record<string, unknown>>;
}

describe('SettingsView', () => {
  beforeEach(() => {
    puts = [];
    notificationTests = [];
    vi.mocked(playNotificationSound).mockClear();
    localStorage.clear();
    document.documentElement.removeAttribute('data-theme');
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string, init?: RequestInit) => {
        if (init?.method === 'PUT') {
          puts.push(JSON.parse(init.body as string));
          return { ok: true, json: async () => ({}) } as Response;
        }
        if (init?.method === 'POST' && url === '/api/notifications/test') {
          notificationTests.push(JSON.parse(init.body as string));
          return { ok: true, json: async () => ({ channels: [] }) } as Response;
        }
        return { ok: true, json: async () => null } as Response;
      })
    );
  });

  afterEach(() => vi.unstubAllGlobals());

  it('lists every domain group in the category master list', () => {
    renderView();
    const nav = screen.getByRole('navigation', { name: 'Settings categories' });
    for (const c of CATEGORIES) {
      expect(within(nav).getByRole('button', { name: c.label })).toBeTruthy();
    }
  });

  it('opens on the selected category', () => {
    renderView({ category: 'terminal' });
    const nav = screen.getByRole('navigation', { name: 'Settings categories' });
    expect(document.activeElement).toBe(
      within(nav).getByRole('button', { name: 'Terminal' })
    );
  });

  it('defaults to the first category and shows its fields', () => {
    renderView();
    expect(screen.getByRole('radio', { name: 'System' })).toBeTruthy();
  });

  // Every category with something to restore gets its own Reset beside "Reset
  // all": the daemon-backed ones and Diagnostics. Appearance and the read-only
  // groups have nothing a Reset could restore.
  it('offers a per-category Reset wherever there is something to restore', () => {
    for (const c of CATEGORIES) {
      const { unmount } = renderView({ category: c.id });
      expect(screen.getByRole('button', { name: 'Reset all' })).toBeTruthy();
      const resettable = c.section !== undefined || c.id === 'diagnostics';
      expect(
        screen.queryAllByRole('button', { name: 'Reset' }),
      ).toHaveLength(resettable ? 1 : 0);
      unmount();
    }
  });

  it("restores only the selected category's defaults from its Reset", async () => {
    const { rerender } = renderView({ category: 'notifications' });
    const style = screen.getByLabelText(
      'Notification style'
    ) as HTMLSelectElement;
    await waitFor(() => expect(style.disabled).toBe(false));
    fireEvent.change(style, { target: { value: 'both' } });

    rerender(viewFor({ category: 'projects' }));
    const confirm = screen.getByRole('checkbox', {
      name: 'Confirm before deleting a project',
    });
    fireEvent.click(confirm);
    await waitFor(() =>
      expect(lastPut().projects).toMatchObject({ confirmRemove: false })
    );

    fireEvent.click(screen.getByRole('button', { name: 'Reset' }));
    await waitFor(() =>
      expect(lastPut().projects).toMatchObject({ confirmRemove: true })
    );
    expect(lastPut().notifications).toMatchObject({ style: 'both' });
  });

  it('restores the settings defaults from "Reset all"', async () => {
    renderView({ category: 'projects' });
    const confirm = screen.getByRole('checkbox', {
      name: 'Confirm before deleting a project',
    }) as HTMLInputElement;
    await waitFor(() => expect(confirm.disabled).toBe(false));
    fireEvent.click(confirm);
    await waitFor(() =>
      expect(lastPut().projects).toMatchObject({ confirmRemove: false })
    );

    fireEvent.click(screen.getByRole('button', { name: 'Reset all' }));
    await waitFor(() =>
      expect(lastPut().projects).toMatchObject({ confirmRemove: true })
    );
  });

  it('selecting a category asks the parent to route there', () => {
    const onSelectCategory = vi.fn();
    renderView({ onSelectCategory });
    fireEvent.click(
      within(
        screen.getByRole('navigation', { name: 'Settings categories' })
      ).getByRole('button', { name: 'Workstreams' })
    );
    expect(onSelectCategory).toHaveBeenCalledWith('workstreams');
  });

  it('changes the theme and applies it to the document (browser-local)', () => {
    renderView({ category: 'appearance' });
    fireEvent.click(screen.getByRole('radio', { name: 'Light' }));
    expect(document.documentElement.getAttribute('data-theme')).toBe('light');
    expect(localStorage.getItem('hs-theme')).toBe('light');
    // Theme is not part of the daemon-persisted settings.
    expect(puts).toHaveLength(0);
  });

  it('persists the default filesystem strategy to the daemon', async () => {
    renderView({ category: 'workstreams' });
    const strategy = screen.getByLabelText(
      'Default filesystem strategy'
    ) as HTMLSelectElement;
    // Editing is gated on a successful load.
    await waitFor(() => expect(strategy.disabled).toBe(false));
    fireEvent.change(strategy, { target: { value: 'worktree' } });
    // The write is debounced.
    await waitFor(() =>
      expect(lastPut().workstreams.defaultStrategy).toMatchObject({
        value: 'worktree',
      })
    );
  });

  it('lists the provider catalogue in the default-provider choices', () => {
    renderView({ category: 'workstreams', providers });
    const select = screen.getByLabelText('Default provider');
    expect(
      within(select).getByRole('option', { name: 'Local System' })
    ).toBeTruthy();
  });

  it('disables the value control when Last used is selected', async () => {
    renderView({ category: 'workstreams', providers });
    // Editing is gated on a successful load.
    await waitFor(() =>
      expect(
        (screen.getAllByRole('checkbox', { name: /Last used/ })[0] as HTMLInputElement)
          .disabled
      ).toBe(false)
    );
    const lastUsed = screen.getAllByRole('checkbox', { name: /Last used/ });
    fireEvent.click(lastUsed[0]);
    expect(
      (screen.getByLabelText('Default provider') as HTMLSelectElement).disabled
    ).toBe(true);
    // The write is debounced.
    await waitFor(() =>
      expect(lastPut().workstreams.defaultProvider).toMatchObject({
        useLastUsed: true,
      })
    );
  });

  it('shows the daemon rejection message beneath the offending field', async () => {
    const message = 'sessions.previewLines must be between 1 and 24, got 0';
    vi.stubGlobal(
      'fetch',
      vi.fn(async (_url: string, init?: RequestInit) => {
        if (init?.method === 'PUT') {
          return {
            ok: false,
            status: 400,
            json: async () => ({ error: message }),
          } as Response;
        }
        return { ok: true, json: async () => null } as Response;
      })
    );
    renderView({ category: 'sessions' });
    const lines = screen.getByLabelText(
      'Session preview lines'
    ) as HTMLInputElement;
    await waitFor(() => expect(lines.disabled).toBe(false));
    fireEvent.change(lines, { target: { value: '0' } });
    fireEvent.blur(lines, { target: { value: '0' } });
    await waitFor(() =>
      expect(screen.getByRole('alert').textContent).toContain(message)
    );
  });

  it('shows a rejected workstreams location beneath the field', async () => {
    // A path-prefixed validation message surfaces beneath the field.
    const message = 'workstreams.location path must be absolute (got "relative")';
    vi.stubGlobal(
      'fetch',
      vi.fn(async (_url: string, init?: RequestInit) => {
        if (init?.method === 'PUT') {
          return {
            ok: false,
            status: 400,
            json: async () => ({ error: message }),
          } as Response;
        }
        return { ok: true, json: async () => null } as Response;
      })
    );
    renderView({ category: 'workstreams' });
    const location = screen.getByLabelText(
      'Worktree / copy location'
    ) as HTMLInputElement;
    await waitFor(() => expect(location.disabled).toBe(false));
    fireEvent.change(location, { target: { value: 'relative' } });
    await waitFor(() =>
      expect(screen.getByRole('alert').textContent).toContain(message)
    );
  });

  it('shows a rejected terminal emulator beneath the select', async () => {
    const message = 'terminal.emulator must be "auto", "custom", or an emulator this platform supports, got "ghostty"';
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string, init?: RequestInit) => {
        if (init?.method === 'PUT') {
          return {
            ok: false,
            status: 400,
            json: async () => ({ error: message }),
          } as Response;
        }
        if (url === '/api/terminal-emulators') {
          return terminalEmulatorsResponse();
        }
        return { ok: true, json: async () => null } as Response;
      })
    );
    renderView({ category: 'terminal' });
    await screen.findByRole('option', { name: 'Ghostty' });
    const select = screen.getByLabelText('Terminal emulator') as HTMLSelectElement;
    await waitFor(() => expect(select.disabled).toBe(false));
    fireEvent.change(select, { target: { value: 'ghostty' } });
    await waitFor(() =>
      expect(screen.getByRole('alert').textContent).toContain(message)
    );
  });

  it('accepts a fractional refresh interval and clamps below-min on blur', async () => {
    renderView({ category: 'providers' });
    const refresh = screen.getByLabelText(
      'Session-state refresh interval (seconds)'
    ) as HTMLInputElement;
    await waitFor(() => expect(refresh.disabled).toBe(false));

    expect(refresh.value).toBe('0.5');

    // Fractional seconds are allowed; the value commits on blur, not per keystroke.
    fireEvent.change(refresh, { target: { value: '0.25' } });
    fireEvent.blur(refresh, { target: { value: '0.25' } });
    await waitFor(() =>
      expect(lastPut().providers.refreshIntervalSec).toBe(0.25)
    );

    // A below-min value is corrected to the floor on blur.
    fireEvent.change(refresh, { target: { value: '0' } });
    fireEvent.blur(refresh, { target: { value: '0' } });
    await waitFor(() =>
      expect(lastPut().providers.refreshIntervalSec).toBe(0.1)
    );
  });

  it('persists the notification style to the daemon', async () => {
    renderView({ category: 'notifications' });
    const style = screen.getByLabelText(
      'Notification style'
    ) as HTMLSelectElement;
    await waitFor(() => expect(style.disabled).toBe(false));
    fireEvent.change(style, { target: { value: 'both' } });
    await waitFor(() =>
      expect(lastPut().notifications).toMatchObject({ style: 'both' })
    );
  });

  it('asks the daemon for a test notification in the style just picked', async () => {
    renderView({ category: 'notifications' });
    const style = screen.getByLabelText(
      'Notification style'
    ) as HTMLSelectElement;
    await waitFor(() => expect(style.disabled).toBe(false));
    // Opening the panel notifies nobody; only a pick demonstrates.
    expect(notificationTests).toHaveLength(0);

    fireEvent.change(style, { target: { value: 'both' } });
    await waitFor(() => expect(notificationTests).toEqual([{ style: 'both' }]));

    // Including None: what "none" looks like is the daemon's answer to give.
    fireEvent.change(style, { target: { value: 'none' } });
    await waitFor(() =>
      expect(notificationTests.at(-1)).toEqual({ style: 'none' })
    );
  });

  it('disables the sound effect when the style is None', async () => {
    renderView({ category: 'notifications' });
    const style = screen.getByLabelText(
      'Notification style'
    ) as HTMLSelectElement;
    const sound = screen.getByLabelText('Sound effect') as HTMLSelectElement;
    await waitFor(() => expect(style.disabled).toBe(false));
    // Applicable by default (the default style delivers).
    expect(sound.disabled).toBe(false);
    fireEvent.change(style, { target: { value: 'none' } });
    expect(sound.disabled).toBe(true);
  });

  it('plays the sound that was just picked, and only then', async () => {
    renderView({ category: 'notifications' });
    const sound = screen.getByLabelText('Sound effect') as HTMLSelectElement;
    await waitFor(() => expect(sound.disabled).toBe(false));
    // Opening the panel must stay silent — only a pick previews.
    expect(playNotificationSound).not.toHaveBeenCalled();

    fireEvent.change(sound, { target: { value: 'glass' } });
    expect(playNotificationSound).toHaveBeenCalledWith('glass');
    // The pick is still persisted; the preview is on top of that, not instead.
    await waitFor(() =>
      expect(lastPut().notifications).toMatchObject({ sound: 'glass' })
    );

    fireEvent.change(sound, { target: { value: 'chime' } });
    expect(playNotificationSound).toHaveBeenLastCalledWith('chime');
  });

  it('turns off the alert for one event without touching the other', async () => {
    renderView({ category: 'notifications' });
    const finished = screen.getByRole('checkbox', {
      name: 'Finished working',
    }) as HTMLInputElement;
    await waitFor(() => expect(finished.disabled).toBe(false));
    fireEvent.click(finished);
    await waitFor(() =>
      expect(lastPut().notifications).toMatchObject({
        finished: { notify: false, sound: true },
        waiting: { notify: true, sound: true },
      })
    );
  });

  it('mutes an event while keeping its notification', async () => {
    renderView({ category: 'notifications' });
    // Each event row carries its own "Play a sound" switch; take the one in the
    // row whose alert switch is the waiting event.
    const rows = screen.getAllByRole('checkbox', { name: 'Play a sound' });
    await waitFor(() =>
      expect((rows[0] as HTMLInputElement).disabled).toBe(false)
    );
    fireEvent.click(rows[0]);
    await waitFor(() =>
      expect(lastPut().notifications).toMatchObject({
        waiting: { notify: true, sound: false },
      })
    );
  });

  it("disables an event's sound switch when its alert is off", async () => {
    renderView({ category: 'notifications' });
    const waiting = screen.getByRole('checkbox', {
      name: 'Needs my input',
    }) as HTMLInputElement;
    const sound = screen.getAllByRole('checkbox', {
      name: 'Play a sound',
    })[0] as HTMLInputElement;
    await waitFor(() => expect(waiting.disabled).toBe(false));
    expect(sound.disabled).toBe(false);
    fireEvent.click(waiting);
    expect(sound.disabled).toBe(true);
  });

  it('disables every per-event switch when the style is None', async () => {
    renderView({ category: 'notifications' });
    const style = screen.getByLabelText(
      'Notification style'
    ) as HTMLSelectElement;
    await waitFor(() => expect(style.disabled).toBe(false));
    fireEvent.change(style, { target: { value: 'none' } });
    for (const name of ['Needs my input', 'Finished working', 'Play a sound']) {
      for (const box of screen.getAllByRole('checkbox', { name })) {
        expect((box as HTMLInputElement).disabled).toBe(true);
      }
    }
    expect(
      (screen.getByLabelText(
        'Quiet period for repeats (seconds)'
      ) as HTMLInputElement).disabled
    ).toBe(true);
  });

  it('persists the quiet period on blur', async () => {
    renderView({ category: 'notifications' });
    const quiet = screen.getByLabelText(
      'Quiet period for repeats (seconds)'
    ) as HTMLInputElement;
    await waitFor(() => expect(quiet.disabled).toBe(false));
    fireEvent.change(quiet, { target: { value: '30' } });
    fireEvent.blur(quiet);
    await waitFor(() =>
      expect(lastPut().notifications).toMatchObject({ quietSeconds: 30 })
    );
  });

  it('tags daemon-only fields as applying when supported', () => {
    renderView({ category: 'sessions' });
    expect(screen.getAllByText('applies when supported').length).toBeGreaterThan(0);
  });

  it('reflects and changes the daemon log level', () => {
    const onLogLevelChange = vi.fn();
    renderView({ category: 'diagnostics', logLevel: 'debug', onLogLevelChange });
    const select = screen.getByLabelText('Daemon log level') as HTMLSelectElement;
    expect(select.value).toBe('debug');
    fireEvent.change(select, { target: { value: 'warn' } });
    expect(onLogLevelChange).toHaveBeenCalledWith('warn');
  });

  it('disables the log level control when the daemon is unavailable', () => {
    renderView({ category: 'diagnostics' });
    expect(
      (screen.getByLabelText('Daemon log level') as HTMLSelectElement).disabled
    ).toBe(true);
  });

  it('resets the Diagnostics controls to their defaults', () => {
    const onLogLevelChange = vi.fn().mockResolvedValue(null);
    const onLogBufferSizeChange = vi.fn();
    renderView({
      category: 'diagnostics',
      logLevel: 'debug',
      onLogLevelChange,
      logBufferSize: 5000,
      onLogBufferSizeChange,
    });
    fireEvent.click(screen.getByRole('button', { name: 'Reset' }));
    expect(onLogLevelChange).toHaveBeenCalledWith('info'); // level → default
    expect(onLogBufferSizeChange).toHaveBeenCalledWith(1000); // buffer → default
  });

  it('changes the log buffer size (a browser-local preference)', () => {
    const onLogBufferSizeChange = vi.fn();
    renderView({ category: 'diagnostics', logBufferSize: 1000, onLogBufferSizeChange });
    const input = screen.getByLabelText(
      'Log buffer size (entries)'
    ) as HTMLInputElement;
    expect(input.value).toBe('1000');
    fireEvent.change(input, { target: { value: '2500' } });
    expect(onLogBufferSizeChange).toHaveBeenCalledWith(2500);
  });

  it('lets a multi-digit buffer size be typed and clamps below-min on blur', () => {
    const onLogBufferSizeChange = vi.fn();
    renderView({ category: 'diagnostics', logBufferSize: 1000, onLogBufferSizeChange });
    const input = screen.getByLabelText('Log buffer size (entries)');
    // Typing toward 2000 must not snap the first digit to the min (100).
    fireEvent.change(input, { target: { value: '2' } });
    expect(onLogBufferSizeChange).toHaveBeenLastCalledWith(2);
    // A below-min value is corrected only on blur.
    fireEvent.change(input, { target: { value: '10' } });
    fireEvent.blur(input, { target: { value: '10' } });
    expect(onLogBufferSizeChange).toHaveBeenLastCalledWith(100);
  });

  // Offline: every daemon-backed category is read-only, and so are the two resets
  // — they PUT the whole document like any other write. The browser-local
  // preferences are untouched, because nothing about them needs the daemon.
  describe('when the daemon is unreachable', () => {
    const REASON = 'The daemon is offline';

    // Asserted on the fieldset rather than the checkbox: the gating is native
    // `fieldset[disabled]`, and jsdom does not propagate that to a control's own
    // `disabled` the way a browser does. What has to hold is that the control sits
    // inside the disabled set, which is what the browser then acts on.
    it('makes a daemon-backed category read-only', async () => {
      renderView({ category: 'projects', disabledReason: REASON });
      const confirm = await screen.findByRole('checkbox', {
        name: 'Confirm before deleting a project',
      });
      const fields = confirm.closest('fieldset') as HTMLFieldSetElement;
      expect(fields.disabled).toBe(true);
      expect(fields.title).toBe(REASON);
    });

    // A title on the fieldset is not an explanation: the load failure beside it
    // renders a visible line, and a disabled control must carry its reason.
    it('says why on screen, not only in a tooltip', () => {
      renderView({ category: 'projects', disabledReason: REASON });
      expect(screen.getByRole('alert').textContent).toBe(REASON);
    });

    it('disables both resets, so neither can PUT', () => {
      renderView({ category: 'projects', disabledReason: REASON });
      for (const name of ['Reset all', 'Reset']) {
        const button = screen.getByRole('button', {
          name,
        }) as HTMLButtonElement;
        expect(button.disabled).toBe(true);
        expect(button.title).toBe(REASON);
      }
    });

    // Appearance is browser-local, so it keeps working while the daemon is gone.
    it('leaves the local preferences editable', async () => {
      renderView({ category: 'appearance', disabledReason: REASON });
      const light = await screen.findByRole('radio', { name: 'Light' });
      expect(
        (light.closest('fieldset') as HTMLFieldSetElement).disabled,
      ).toBe(false);
      expect(screen.queryByRole('alert')).toBeNull();
    });

    // Diagnostics' reset spans both halves: the log level is a daemon call, the
    // buffer size is a localStorage write. Offline, the local half must still run
    // — its own field right below stays editable — and the daemon half must not
    // fire.
    it('still resets the browser-local buffer size while offline', () => {
      const onLogLevelChange = vi.fn();
      const onLogBufferSizeChange = vi.fn();
      renderView({
        category: 'diagnostics',
        logLevel: 'debug',
        logBufferSize: 250,
        onLogLevelChange,
        onLogBufferSizeChange,
        disabledReason: REASON,
      });
      const reset = screen.getByRole('button', {
        name: 'Reset',
      }) as HTMLButtonElement;
      expect(reset.disabled).toBe(false);
      fireEvent.click(reset);
      expect(onLogBufferSizeChange).toHaveBeenCalledWith(DEFAULT_MAX_RECORDS);
      expect(onLogLevelChange).not.toHaveBeenCalled();
    });
  });
});
