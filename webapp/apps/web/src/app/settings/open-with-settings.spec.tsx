import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';

import { OpenWithSettings } from './open-with-settings';
import { SettingsProvider } from './use-settings';
import type { OpenWithEntry } from './settings-types';

// The panel renders a local draft of `openWith.entries` and writes only the
// complete rows back through the settings document's debounced PUT.

// PUT /api/settings bodies captured during a test.
let puts: Record<string, unknown>[] = [];

function stubFetch(entries: OpenWithEntry[] | null) {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (_url: string, init?: RequestInit) => {
      if (init?.method === 'PUT') {
        puts.push(JSON.parse(init.body as string));
        return { ok: true, json: async () => ({}) } as Response;
      }
      return {
        ok: true,
        json: async () => (entries === null ? null : { openWith: { entries } }),
      } as Response;
    })
  );
}

function renderPanel(entries: OpenWithEntry[] | null = null) {
  stubFetch(entries);
  return render(
    <SettingsProvider>
      <OpenWithSettings />
    </SettingsProvider>
  );
}

/** The entries of the most recent settings written to the daemon. */
function lastPutEntries(): OpenWithEntry[] {
  const body = puts.at(-1) as { openWith: { entries: OpenWithEntry[] } };
  return body.openWith.entries;
}

describe('OpenWithSettings', () => {
  beforeEach(() => {
    puts = [];
  });

  afterEach(() => vi.unstubAllGlobals());

  it('renders the default terminal row before anything is stored', async () => {
    renderPanel(null);
    const label = (await screen.findByLabelText(
      'Label of entry 1'
    )) as HTMLInputElement;
    expect(label.value).toBe('Terminal');
    // The default row is the terminal itself: no command, run in a terminal.
    expect(
      (screen.getByLabelText('Command of entry 1') as HTMLInputElement).value
    ).toBe('');
    expect(
      (screen.getByLabelText('Run entry 1 in a terminal') as HTMLInputElement)
        .checked
    ).toBe(true);
  });

  it('lists the stored entries with their own statements', async () => {
    renderPanel([
      { id: 'terminal', label: 'Terminal', command: '', inTerminal: true, fixed: true },
      { id: 'e1', label: 'VS Code', command: 'code .', inTerminal: false },
    ]);
    await waitFor(() =>
      expect(
        (screen.getByLabelText('Label of entry 2') as HTMLInputElement).value
      ).toBe('VS Code')
    );
    expect(
      (screen.getByLabelText('Command of entry 2') as HTMLInputElement).value
    ).toBe('code .');
    expect(
      (screen.getByLabelText('Run entry 2 in a terminal') as HTMLInputElement)
        .checked
    ).toBe(false);
  });

  it('persists an edited row through the settings document', async () => {
    renderPanel([
      { id: 'e1', label: 'VS Code', command: 'code', inTerminal: false },
    ]);
    // The default row renders first and is replaced when the load resolves.
    await waitFor(() =>
      expect(
        (screen.getByLabelText('Command of entry 1') as HTMLInputElement).value
      ).toBe('code')
    );
    fireEvent.change(screen.getByLabelText('Command of entry 1'), {
      target: { value: 'code .' },
    });
    // Edits commit on blur, not per keystroke.
    fireEvent.blur(screen.getByLabelText('Command of entry 1'));
    // The write is debounced.
    await waitFor(() =>
      expect(lastPutEntries()).toEqual([
        {
          id: 'e1',
          label: 'VS Code',
          command: 'code .',
          inTerminal: false,
          fixed: false,
        },
      ])
    );
  });

  it('persists the "in a terminal" statement — the user says it, nothing is detected', async () => {
    renderPanel([
      { id: 'e1', label: 'Neovim', command: 'nvim .', inTerminal: false },
    ]);
    await waitFor(() =>
      expect(
        (screen.getByLabelText('Label of entry 1') as HTMLInputElement).value
      ).toBe('Neovim')
    );
    fireEvent.click(screen.getByLabelText('Run entry 1 in a terminal'));
    await waitFor(() =>
      expect(lastPutEntries()[0]).toMatchObject({ inTerminal: true })
    );
  });

  it('deletes a row', async () => {
    renderPanel([
      { id: 'terminal', label: 'Terminal', command: '', inTerminal: true, fixed: true },
      { id: 'e1', label: 'VS Code', command: 'code .', inTerminal: false },
    ]);
    const remove = await screen.findByRole('button', {
      name: 'Delete VS Code',
    });
    // The label it shows is in its accessible name.
    expect(remove.textContent).toBe('Delete');
    fireEvent.click(remove);
    await waitFor(() =>
      expect(lastPutEntries().map((entry) => entry.id)).toEqual(['terminal'])
    );
  });

  it('focuses the label of an added row', async () => {
    renderPanel([
      { id: 'terminal', label: 'Terminal', command: '', inTerminal: true, fixed: true },
    ]);
    fireEvent.click(await screen.findByRole('button', { name: 'Add entry' }));
    expect(document.activeElement).toBe(screen.getByLabelText('Label of entry 2'));
  });

  it.each([
    ['the next row’s Delete', 'Delete VS Code', 'Delete Vim'],
    ['Add entry, with no row after it', 'Delete Vim', 'Add entry'],
  ])('moves focus to %s after a removal', async (_case, removed, focused) => {
    renderPanel([
      { id: 'terminal', label: 'Terminal', command: '', inTerminal: true, fixed: true },
      { id: 'e1', label: 'VS Code', command: 'code .', inTerminal: false },
      { id: 'e2', label: 'Vim', command: 'vim .', inTerminal: true },
    ]);
    fireEvent.click(await screen.findByRole('button', { name: removed }));
    await waitFor(() =>
      expect(document.activeElement).toBe(
        screen.getByRole('button', { name: focused })
      )
    );
  });

  it('adds a row locally and saves it only once it is complete', async () => {
    renderPanel([
      { id: 'terminal', label: 'Terminal', command: '', inTerminal: true, fixed: true },
    ]);
    const add = await screen.findByRole('button', { name: 'Add entry' });
    // Editing is gated on a successful load.
    await waitFor(() =>
      expect(
        (screen.getByLabelText('Label of entry 1') as HTMLInputElement).value
      ).toBe('Terminal')
    );
    fireEvent.click(add);
    // The new row is on screen and marked unsaved, with nothing written.
    expect(screen.getByLabelText('Label of entry 2')).toBeTruthy();
    expect(screen.getByText('unsaved')).toBeTruthy();
    await new Promise((resolve) => setTimeout(resolve, 400)); // past the debounce
    expect(puts).toHaveLength(0);

    fireEvent.change(screen.getByLabelText('Label of entry 2'), {
      target: { value: 'VS Code' },
    });
    fireEvent.blur(screen.getByLabelText('Label of entry 2'));
    // A label alone is still not saveable outside a terminal: no command.
    await new Promise((resolve) => setTimeout(resolve, 400));
    expect(puts).toHaveLength(0);

    fireEvent.change(screen.getByLabelText('Command of entry 2'), {
      target: { value: 'code .' },
    });
    fireEvent.blur(screen.getByLabelText('Command of entry 2'));
    await waitFor(() => {
      const entries = lastPutEntries();
      expect(entries).toHaveLength(2);
      expect(entries[1].id).toMatch(/^open-with-/);
      expect(entries[1]).toMatchObject({
        label: 'VS Code',
        command: 'code .',
        inTerminal: false,
      });
    });
    expect(screen.queryByText('unsaved')).toBeNull();
  });

  it('offers no way to remove the default row, but lets it be repurposed', async () => {
    renderPanel([
      { id: 'terminal', label: 'Terminal', command: '', inTerminal: true, fixed: true },
      { id: 'e1', label: 'VS Code', command: 'code .', inTerminal: false },
    ]);
    await waitFor(() =>
      expect(
        (screen.getByLabelText('Label of entry 2') as HTMLInputElement).value
      ).toBe('VS Code')
    );
    // Scoped to the list: the panel's own prose mentions the default row too.
    const rows = screen.getByRole('list', { name: 'Open with entries' });
    expect(within(rows).getByText('default')).toBeTruthy();
    expect(
      screen.queryByRole('button', { name: 'Delete Terminal' }),
    ).toBeNull();
    expect(screen.getByRole('button', { name: 'Delete VS Code' })).toBeTruthy();

    fireEvent.change(screen.getByLabelText('Command of entry 1'), {
      target: { value: 'code .' },
    });
    fireEvent.blur(screen.getByLabelText('Command of entry 1'));
    await waitFor(() =>
      expect(lastPutEntries()[0]).toMatchObject({
        id: 'terminal',
        command: 'code .',
      })
    );
  });

  it('lets the default row be renamed', async () => {
    renderPanel([
      { id: 'terminal', label: 'Terminal', command: '', inTerminal: true, fixed: true },
    ]);
    const label = (await screen.findByLabelText('Label of entry 1')) as HTMLInputElement;
    await waitFor(() => expect(label.value).toBe('Terminal'));
    fireEvent.change(label, { target: { value: 'Shell' } });
    fireEvent.blur(label);
    await waitFor(() =>
      expect(lastPutEntries()[0]).toMatchObject({ id: 'terminal', label: 'Shell' })
    );
  });

  it('keeps the default row as last saved while its label is cleared', async () => {
    renderPanel([
      { id: 'e1', label: 'VS Code', command: 'code .', inTerminal: false },
      { id: 'terminal', label: 'Shell', command: 'zsh', inTerminal: true, fixed: true },
    ]);
    const label = (await screen.findByLabelText('Label of entry 2')) as HTMLInputElement;
    await waitFor(() => expect(label.value).toBe('Shell'));
    fireEvent.change(label, { target: { value: '' } });
    fireEvent.blur(label);
    fireEvent.click(screen.getByRole('button', { name: 'Move VS Code down' }));
    await waitFor(() =>
      expect(lastPutEntries()).toMatchObject([
        { id: 'terminal', label: 'Shell', command: 'zsh' },
        { id: 'e1' },
      ])
    );
  });

  it('moves a row and keeps focus on the button that moved it', async () => {
    renderPanel([
      { id: 'terminal', label: 'Terminal', command: '', inTerminal: true, fixed: true },
      { id: 'e1', label: 'VS Code', command: 'code .', inTerminal: false },
    ]);
    const up = await screen.findByRole('button', { name: 'Move VS Code up' });
    expect(
      screen.getByRole('button', { name: 'Move Terminal up' }).getAttribute('aria-disabled')
    ).toBe('true');
    fireEvent.click(up);
    await waitFor(() =>
      expect(lastPutEntries().map((entry) => entry.id)).toEqual(['e1', 'terminal'])
    );
    expect(document.activeElement).toBe(
      screen.getByRole('button', { name: 'Move VS Code up' })
    );
    expect(
      (screen.getByLabelText('Label of entry 1') as HTMLInputElement).value
    ).toBe('VS Code');
  });

  it('says so when the list is empty', async () => {
    renderPanel([]);
    expect(await screen.findByText('No entries. Add one below.')).toBeTruthy();
  });
});
