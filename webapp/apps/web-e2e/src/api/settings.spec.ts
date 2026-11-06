import { expect, test } from '@playwright/test';
import { withSettingsLock } from '../settings-lock';

/**
 * Black-box tests for the daemon-persisted settings endpoint. The daemon is
 * shared, so each successful write holds the settings lock; a rejected PUT
 * stores nothing and needs none.
 */

test('GET /api/settings returns the settings document', async ({ request }) => {
  const res = await request.get('/api/settings');
  expect(res.status()).toBe(200);
  expect(res.headers()['content-type']).toContain('application/json');
  const body = await res.json();
  expect(body.sessions.previewLines).toBeGreaterThan(0);
  expect(body.projects).toHaveProperty('sidebarSort');
});

test('PUT /api/settings accepts and echoes the settings', async ({ request }) => {
  await withSettingsLock(async () => {
    const current = await (await request.get('/api/settings')).json();
    current.workstreams.nameTemplate = '{project}-api';
    const put = await request.put('/api/settings', { data: current });
    expect(put.status()).toBe(200);
    expect((await put.json()).workstreams.nameTemplate).toBe('{project}-api');
  });
});

test('PUT /api/settings rejects invalid settings', async ({ request }) => {
  const current = await (await request.get('/api/settings')).json();
  current.sessions.previewLines = 0;
  const res = await request.put('/api/settings', { data: current });
  expect(res.status()).toBe(400);
});

test('PUT /api/settings accepts a fractional refresh interval', async ({
  request,
}) => {
  await withSettingsLock(async () => {
    const current = await (await request.get('/api/settings')).json();
    current.providers.refreshIntervalSec = 0.25;
    const put = await request.put('/api/settings', { data: current });
    expect(put.status()).toBe(200);
    expect((await put.json()).providers.refreshIntervalSec).toBe(0.25);
  });
});

test('PUT /api/settings rejects a below-floor refresh interval', async ({
  request,
}) => {
  const current = await (await request.get('/api/settings')).json();
  current.providers.refreshIntervalSec = 0.001; // below the 0.1 s floor
  const res = await request.put('/api/settings', { data: current });
  expect(res.status()).toBe(400);
});

test('PUT /api/settings rejects an above-ceiling refresh interval', async ({
  request,
}) => {
  const current = await (await request.get('/api/settings')).json();
  current.providers.refreshIntervalSec = 1e10; // above the 24 h ceiling
  const res = await request.put('/api/settings', { data: current });
  expect(res.status()).toBe(400);
});

test('PUT /api/settings accepts a workstreams location', async ({ request }) => {
  await withSettingsLock(async () => {
    const current = await (await request.get('/api/settings')).json();
    try {
      const put = await request.put('/api/settings', {
        data: {
          ...current,
          workstreams: { ...current.workstreams, location: '../worktrees' },
        },
      });
      expect(put.status()).toBe(200);
      expect((await put.json()).workstreams.location).toBe('../worktrees');
    } finally {
      await request.put('/api/settings', { data: current });
    }
  });
});

test("PUT /api/settings rejects another user's home as workstreams location", async ({
  request,
}) => {
  const current = await (await request.get('/api/settings')).json();
  current.workstreams.location = '~other/worktrees';
  const res = await request.put('/api/settings', { data: current });
  expect(res.status()).toBe(400);
});

test('PUT /api/settings rejects an unknown notification style', async ({
  request,
}) => {
  // Rejected, so nothing is persisted: the shared style is untouched.
  const current = await (await request.get('/api/settings')).json();
  current.notifications = { style: 'telepathy', sound: 'default' };
  const res = await request.put('/api/settings', { data: current });
  expect(res.status()).toBe(400);
});

test('GET /api/settings serves the per-event notification switches', async ({
  request,
}) => {
  const { notifications } = await (await request.get('/api/settings')).json();
  for (const event of ['waiting', 'finished']) {
    expect(typeof notifications[event].notify).toBe('boolean');
    expect(typeof notifications[event].sound).toBe('boolean');
  }
  expect(typeof notifications.quietSeconds).toBe('number');
});

// Writing the per-event switches is deliberately *not* tested here: every write
// against this shared daemon is a full-document PUT, and one that carried a
// notification event back to "off" — or simply lost a race with the style the
// notification specs set — would hang them. Those writes live in
// api/notification-events.spec.ts, which runs a daemon of its own.

test('PUT /api/settings rejects an out-of-range quiet period', async ({
  request,
}) => {
  // Rejected (400), so nothing is persisted and the shared quiet period — which
  // gates how often the notification specs' alerts repeat — is untouched.
  const current = await (await request.get('/api/settings')).json();
  for (const quietSeconds of [-1, 1e10]) {
    const res = await request.put('/api/settings', {
      data: { ...current, notifications: { ...current.notifications, quietSeconds } },
    });
    expect(res.status()).toBe(400);
  }
  const after = await (await request.get('/api/settings')).json();
  expect(after.notifications.quietSeconds).toBe(
    current.notifications.quietSeconds
  );
});

test('rejects unsupported methods on /api/settings', async ({ request }) => {
  const res = await request.post('/api/settings');
  expect(res.status()).toBe(405);
});

/**
 * The upgrade path: a client written against an older schema sends a document
 * without the keys it has never heard of. Those keys must come back at their
 * defaults, not at the zero value — a `false` that means "not mentioned" is
 * indistinguishable from one the user chose, so nothing downstream can catch it
 * and the daemon would simply stop notifying, with a 200 and no complaint.
 *
 * Written as a full-document PUT with one section replaced wholesale, which is
 * exactly what such a client sends.
 */
test('PUT /api/settings keeps defaults for keys the body omits', async ({
  request,
}) => {
  const current = await (await request.get('/api/settings')).json();
  const put = await request.put('/api/settings', {
    data: {
      ...current,
      // A pre-upgrade notifications document: the two keys that existed then.
      notifications: {
        style: current.notifications.style,
        sound: current.notifications.sound,
      },
    },
  });
  expect(put.status()).toBe(200);

  const body = await put.json();
  expect(body.notifications.waiting.notify).toBe(true);
  expect(body.notifications.finished.notify).toBe(true);
  expect(body.notifications.quietSeconds).toBeGreaterThan(0);
});

// The other half of the same rule: an explicit false is the user's choice and
// survives the merge.
test('PUT /api/settings honours an explicitly disabled event', async ({
  request,
}) => {
  const current = await (await request.get('/api/settings')).json();
  const put = await request.put('/api/settings', {
    data: {
      ...current,
      notifications: {
        ...current.notifications,
        waiting: { notify: false, sound: false },
      },
    },
  });
  expect(put.status()).toBe(200);
  expect((await put.json()).notifications.waiting.notify).toBe(false);

  // Put it back, so the notification specs sharing this daemon are unaffected.
  await request.put('/api/settings', { data: current });
});
