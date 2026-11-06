import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';

import { TerminalSettings } from './terminal-settings';
import { SettingsProvider } from './use-settings';
import type { Settings } from './settings-types';
import type { TerminalEmulatorList } from './use-terminal-emulators';
import {
  TERMINAL_EMULATORS,
  terminalEmulatorsResponse,
} from '../../testing/terminal-emulators';

// PUT /api/settings bodies captured during a test.
let puts: Settings[] = [];

function renderPanel(
  terminal: Settings['terminal'] = { emulator: 'auto', command: '' },
  {
    listed = true,
    emulators = TERMINAL_EMULATORS,
  }: { listed?: boolean; emulators?: TerminalEmulatorList } = {}
) {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init?: RequestInit) => {
      if (init?.method === 'PUT') {
        const body: Settings = JSON.parse(init.body as string);
        puts.push(body);
        // Like the daemon, echo the stored copy with the template trimmed.
        const stored = {
          ...body,
          terminal: { ...body.terminal, command: body.terminal.command.trim() },
        };
        return { ok: true, json: async () => stored } as Response;
      }
      if (url === '/api/terminal-emulators') {
        return terminalEmulatorsResponse(listed ? emulators : null);
      }
      return { ok: true, json: async () => ({ terminal }) } as Response;
    })
  );
  return render(
    <SettingsProvider>
      <TerminalSettings />
    </SettingsProvider>
  );
}

const emulatorSelect = () =>
  screen.getByLabelText('Terminal emulator') as HTMLSelectElement;

describe('TerminalSettings', () => {
  beforeEach(() => {
    puts = [];
  });

  afterEach(() => vi.unstubAllGlobals());

  it('offers the daemon-listed emulators between automatic and custom', async () => {
    renderPanel();
    await screen.findByRole('option', { name: 'Ghostty' });
    expect(
      Array.from(emulatorSelect().options).map((option) => option.textContent)
    ).toEqual([
      'Automatic (Ghostty)',
      'Ghostty',
      'kitty (not installed)',
      'Custom command',
    ]);
  });

  it('says so when Automatic has nothing to open', async () => {
    renderPanel(undefined, {
      emulators: {
        automatic: '',
        emulators: TERMINAL_EMULATORS.emulators.map((emulator) => ({
          ...emulator,
          installed: false,
        })),
      },
    });
    expect(
      (await screen.findByRole('option', { name: 'Automatic (none installed)' }))
        .parentElement
    ).toBe(emulatorSelect());
    expect(screen.getByText(/None of the emulators listed here is installed/)).toBeTruthy();
  });

  // $TERMINAL can name an emulator the list does not know; the daemon says what
  // Automatic resolves to, so that is not taken for none installed.
  it('names what Automatic opens even when no listed emulator is installed', async () => {
    renderPanel(undefined, {
      emulators: {
        automatic: 'st',
        emulators: TERMINAL_EMULATORS.emulators.map((emulator) => ({
          ...emulator,
          installed: false,
        })),
      },
    });
    expect(
      (await screen.findByRole('option', { name: 'Automatic (st)' })).parentElement
    ).toBe(emulatorSelect());
    expect(screen.queryByText(/None of the emulators listed here is installed/)).toBeNull();
  });

  it('says so when the daemon cannot list its emulators', async () => {
    renderPanel(undefined, { listed: false });
    expect((await screen.findByRole('alert')).textContent).toBe(
      'Could not load the emulators from the daemon, so only Automatic and Custom are listed.'
    );
    expect(
      Array.from(emulatorSelect().options).map((option) => option.textContent)
    ).toEqual(['Automatic', 'Custom command']);
  });

  it('persists a chosen emulator', async () => {
    renderPanel();
    await screen.findByRole('option', { name: 'Ghostty' });
    fireEvent.change(emulatorSelect(), { target: { value: 'ghostty' } });
    await waitFor(() =>
      expect(puts.at(-1)?.terminal).toEqual({ emulator: 'ghostty', command: '' })
    );
  });

  it('shows the stored custom template', async () => {
    renderPanel({ emulator: 'custom', command: 'foot -e {command}' });
    expect(
      ((await screen.findByLabelText('Custom terminal command')) as HTMLInputElement)
        .value
    ).toBe('foot -e {command}');
  });

  it('holds a custom choice back until its template includes {command}', async () => {
    renderPanel();
    await screen.findByRole('option', { name: 'Ghostty' });
    fireEvent.change(emulatorSelect(), { target: { value: 'custom' } });
    const field = screen.getByLabelText('Custom terminal command');
    fireEvent.change(field, { target: { value: 'foot -e' } });
    expect(screen.getByText('Not saved until it includes {command}.')).toBeTruthy();

    fireEvent.change(field, { target: { value: 'foot -e {command}' } });
    await waitFor(() =>
      expect(puts.at(-1)?.terminal).toEqual({
        emulator: 'custom',
        command: 'foot -e {command}',
      })
    );
    expect(puts.every((body) => body.terminal.command.includes('{command}'))).toBe(true);
    expect(screen.queryByText('Not saved until it includes {command}.')).toBeNull();
  });

  it('keeps a trailing space typed into the template when the daemon trims it', async () => {
    renderPanel({ emulator: 'custom', command: 'foot -e {command}' });
    const field = (await screen.findByLabelText(
      'Custom terminal command'
    )) as HTMLInputElement;
    fireEvent.change(field, { target: { value: 'foot -e {command} ' } });
    await waitFor(() => expect(puts.at(-1)?.terminal.command).toBe('foot -e {command} '));
    // Let the echoed copy land.
    await act(() => new Promise((resolve) => setTimeout(resolve, 0)));
    expect(field.value).toBe('foot -e {command} ');
  });

  it('shows a stored emulator the daemon did not list as itself', async () => {
    renderPanel({ emulator: 'ghostty', command: '' }, { listed: false });
    await waitFor(() => expect(emulatorSelect().value).toBe('ghostty'));
  });

  it('stores no template when it is emptied before switching away from custom', async () => {
    renderPanel({ emulator: 'custom', command: 'foot -e {command}' });
    const field = await screen.findByLabelText('Custom terminal command');
    fireEvent.change(field, { target: { value: '' } });
    fireEvent.change(emulatorSelect(), { target: { value: 'auto' } });
    await waitFor(() =>
      expect(puts.at(-1)?.terminal).toEqual({ emulator: 'auto', command: '' })
    );
  });

  it('hides the template field unless custom is chosen', async () => {
    renderPanel({ emulator: 'ghostty', command: 'foot -e {command}' });
    await screen.findByRole('option', { name: 'Ghostty' });
    expect(screen.queryByLabelText('Custom terminal command')).toBeNull();
  });
});
