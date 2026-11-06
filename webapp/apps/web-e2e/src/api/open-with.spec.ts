import { expect, test, type APIRequestContext } from '@playwright/test';
import { withSettingsLock } from '../settings-lock';

/**
 * The user's "open with" list as a section of the settings document
 * (`openWith.entries` under GET/PUT /api/settings).
 */

interface Entry {
  id: string;
  label: string;
  command: string;
  inTerminal: boolean;
  /** Daemon-owned on the way out; a client may omit it on the way in. */
  fixed?: boolean;
}

interface SettingsDoc {
  openWith: { entries: Entry[] };
  [key: string]: unknown;
}

// These tests share one settings document, so they run in order in one worker.
test.describe.configure({ mode: 'default' });

const unique = (name: string) =>
  `${name}-${Math.random().toString(36).slice(2, 8)}`;

async function currentSettings(request: APIRequestContext): Promise<SettingsDoc> {
  const res = await request.get('/api/settings');
  expect(res.status()).toBe(200);
  return (await res.json()) as SettingsDoc;
}

/** PUTs the document with `entries` and returns the daemon's echo. */
async function putEntries(
  request: APIRequestContext,
  entries: Partial<Entry>[],
): Promise<Entry[]> {
  const doc = await currentSettings(request);
  doc.openWith.entries = entries as Entry[];
  const put = await request.put('/api/settings', { data: doc });
  expect(put.status()).toBe(200);
  return ((await put.json()) as SettingsDoc).openWith.entries;
}

/** Appends a row and returns the daemon's echo of the whole list. */
async function addEntry(
  request: APIRequestContext,
  entry: Partial<Entry>,
): Promise<Entry[]> {
  const doc = await currentSettings(request);
  return putEntries(request, [...doc.openWith.entries, entry]);
}

/** Drops this test's rows again (matched by label), daemon being shared. */
async function removeByLabel(request: APIRequestContext, label: string) {
  const doc = await currentSettings(request);
  await putEntries(
    request,
    doc.openWith.entries.filter((entry) => entry.label !== label),
  );
}

test('the document ships the seeded default terminal row', async ({
  request,
}) => {
  const doc = await currentSettings(request);
  const terminal = doc.openWith.entries.find((entry) => entry.id === 'terminal');
  expect(terminal).toBeTruthy();
  // Empty means "this host's terminal", resolved by the daemon at launch.
  expect(terminal?.command).toBe('');
  expect(terminal?.inTerminal).toBe(true);
});

test('a row saved without an id is echoed back with a daemon-assigned one', async ({
  request,
}) => {
  await withSettingsLock(async () => {
  const label = unique('VSCode');
  const echoed = await addEntry(request, {
    label,
    command: 'code .',
    inTerminal: false,
  });
  try {
    const added = echoed.find((entry) => entry.label === label);
    expect(added).toBeTruthy();
    expect(added?.id).toMatch(/^open-with-/);
    expect(added?.command).toBe('code .');
    expect(added?.inTerminal).toBe(false);
  } finally {
    await removeByLabel(request, label);
  }
  });
});

test('a first row without an id is added beside the terminal row, not merged into it', async ({
  request,
}) => {
  await withSettingsLock(async () => {
    const before = (await currentSettings(request)).openWith.entries;
    const label = unique('First');
    try {
      const echoed = await putEntries(request, [
        { label, command: 'code {path}' },
      ]);
      const terminal = echoed.find((entry) => entry.id === 'terminal');
      expect(terminal?.label).toBe('Terminal');
      expect(terminal?.command).toBe('');
      const added = echoed.find((entry) => entry.label === label);
      expect(added?.id).toMatch(/^open-with-/);
      expect(added?.command).toBe('code {path}');
      expect(added?.inTerminal).toBe(false);
    } finally {
      await putEntries(request, before);
    }
  });
});

test('label and command are trimmed on the way in', async ({ request }) => {
  await withSettingsLock(async () => {
    const label = unique('Padded');
    const echoed = await addEntry(request, {
      label: `  ${label}  `,
      command: '  code .  ',
    });
    try {
      const added = echoed.find((entry) => entry.label === label);
      expect(added).toBeTruthy();
      expect(added?.command).toBe('code .');
    } finally {
      await removeByLabel(request, label);
    }
  });
});

test('a row without a label is refused', async ({ request }) => {
  const doc = await currentSettings(request);
  doc.openWith.entries = [
    ...doc.openWith.entries,
    { id: '', label: '   ', command: 'code .', inTerminal: false },
  ];
  const res = await request.put('/api/settings', { data: doc });
  expect(res.status()).toBe(400);
  expect((await res.json()).error).toContain('label');
});

test('an empty command is refused unless the row runs in a terminal', async ({
  request,
}) => {
  const doc = await currentSettings(request);
  doc.openWith.entries = [
    ...doc.openWith.entries,
    { id: '', label: unique('Blank'), command: '', inTerminal: false },
  ];
  const res = await request.put('/api/settings', { data: doc });
  expect(res.status()).toBe(400);
  expect((await res.json()).error).toContain('command');
});

test('duplicate row ids are refused', async ({ request }) => {
  const doc = await currentSettings(request);
  doc.openWith.entries = [
    ...doc.openWith.entries,
    { id: 'dup-1', label: unique('One'), command: 'code .', inTerminal: false },
    { id: 'dup-1', label: unique('Two'), command: 'vim .', inTerminal: true },
  ];
  const res = await request.put('/api/settings', { data: doc });
  expect(res.status()).toBe(400);
  expect((await res.json()).error).toContain('duplicate');
});

// Safe on the shared daemon because the row comes back: the list written here
// is the one every other worker sees.
test('the default-open row is restored when a client drops it', async ({
  request,
}) => {
  await withSettingsLock(async () => {
    const doc = await currentSettings(request);
    const withoutDefault = doc.openWith.entries.filter(
      (entry) => entry.id !== 'terminal',
    );
    const echoed = await putEntries(request, withoutDefault);

    const terminal = echoed.find((entry) => entry.id === 'terminal');
    expect(terminal).toBeTruthy();
    expect(echoed[0].id).toBe('terminal');
    expect(terminal?.label).toBeTruthy();
    expect(terminal?.inTerminal).toBe(true);
  });
});

// Clients decide whether to offer a remove action from this flag rather than
// knowing which id is special, so the daemon owns it and overrides what is sent.
test('the daemon owns the fixed flag on every row', async ({ request }) => {
  await withSettingsLock(async () => {
    const label = unique('Claims');
    const doc = await currentSettings(request);
    const echoed = await putEntries(request, [
      ...doc.openWith.entries.map((entry) =>
        entry.id === 'terminal' ? { ...entry, fixed: false } : entry,
      ),
      { label, command: 'code .', inTerminal: false, fixed: true },
    ]);
    try {
      const terminal = echoed.find((entry) => entry.id === 'terminal');
      expect(terminal?.fixed).toBe(true);
      expect(echoed.find((entry) => entry.label === label)?.fixed).toBe(false);
    } finally {
      await removeByLabel(request, label);
    }
  });
});

test('the default-open row keeps the name and place a client gives it', async ({
  request,
}) => {
  await withSettingsLock(async () => {
    const before = (await currentSettings(request)).openWith.entries;
    const label = unique('Editor');
    const name = unique('Shell');
    try {
      const echoed = await putEntries(request, [
        { label, command: 'code .', inTerminal: false },
        ...before.map((entry) =>
          entry.id === 'terminal' ? { ...entry, label: name } : entry,
        ),
      ]);
      expect(echoed[0].label).toBe(label);
      expect(echoed[1]).toMatchObject({ id: 'terminal', label: name, fixed: true });
    } finally {
      await putEntries(request, before);
    }
  });
});
