import { expect, test, type APIRequestContext } from '@playwright/test';
import { withSettingsLock } from '../settings-lock';

/**
 * The terminal-emulator preference: the daemon lists the emulators its platform
 * supports and validates the choice stored in the settings document. Nothing
 * here opens a terminal window.
 */

async function putTerminal(
  request: APIRequestContext,
  terminal: { emulator: string; command: string },
) {
  const current = await (await request.get('/api/settings')).json();
  return request.put('/api/settings', { data: { ...current, terminal } });
}

test('GET /api/terminal-emulators lists the platform emulators', async ({
  request,
}) => {
  const res = await request.get('/api/terminal-emulators');
  expect(res.status()).toBe(200);
  const {
    automatic,
    emulators,
  }: {
    automatic: unknown;
    emulators: { id: string; label: string; installed: unknown }[];
  } = await res.json();
  expect(emulators.length).toBeGreaterThan(0);
  for (const emulator of emulators) {
    expect(emulator.label).not.toBe('');
    // A client wraps a label in "(not installed)" and "Automatic (…)".
    expect(emulator.label).not.toMatch(/[()]/);
    expect(typeof emulator.installed).toBe('boolean');
  }
  // What Automatic opens windows in, by label: a listed emulator, which is then
  // an installed one, or whatever else $TERMINAL names; "" for nothing.
  expect(typeof automatic).toBe('string');
  const listed = emulators.find((emulator) => emulator.label === automatic);
  if (listed) expect(listed.installed).toBe(true);
});

test('PUT /api/settings round-trips a chosen emulator and a custom template', async ({
  request,
}) => {
  const {
    emulators: [first],
  } = await (await request.get('/api/terminal-emulators')).json();
  await withSettingsLock(async () => {
    try {
      for (const terminal of [
        { emulator: first.id, command: '' },
        { emulator: 'custom', command: 'foot -e {command}' },
      ]) {
        const put = await putTerminal(request, terminal);
        expect(put.status()).toBe(200);
        expect((await put.json()).terminal).toEqual(terminal);
        const stored = await (await request.get('/api/settings')).json();
        expect(stored.terminal).toEqual(terminal);
      }
    } finally {
      await putTerminal(request, { emulator: 'auto', command: '' });
    }
  });
});

test('PUT /api/settings rejects a custom template without {command}', async ({
  request,
}) => {
  const res = await putTerminal(request, {
    emulator: 'custom',
    command: 'foot -e',
  });
  expect(res.status()).toBe(400);
  expect((await res.json()).error).toMatch(/^terminal\.command /);
});

test('PUT /api/settings rejects an emulator the platform does not support', async ({
  request,
}) => {
  const res = await putTerminal(request, {
    emulator: 'no-such-terminal',
    command: '',
  });
  expect(res.status()).toBe(400);
  expect((await res.json()).error).toMatch(/^terminal\.emulator /);
});
