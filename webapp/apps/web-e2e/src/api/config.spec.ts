import { expect, test } from '@playwright/test';

/**
 * Black-box tests for the daemon config endpoint. The log level is
 * process-global daemon state, so each test restores what it found.
 */
test.describe('daemon config API', () => {
  test('GET /api/config reports the effective log level', async ({ request }) => {
    const res = await request.get('/api/config');
    expect(res.status()).toBe(200);
    expect(res.headers()['content-type']).toContain('application/json');
    const body = await res.json();
    expect(typeof body.logLevel).toBe('string');
    expect(body.logLevel.length).toBeGreaterThan(0);
  });

  test('PATCH /api/config changes the log level at runtime', async ({
    request,
  }) => {
    const original = (await (await request.get('/api/config')).json()).logLevel;
    try {
      const patch = await request.patch('/api/config', {
        data: { logLevel: 'warn' },
      });
      expect(patch.status()).toBe(200);
      expect((await patch.json()).logLevel).toBe('warn');

      const after = await (await request.get('/api/config')).json();
      expect(after.logLevel).toBe('warn');
    } finally {
      await request.patch('/api/config', { data: { logLevel: original } });
    }
  });

  test('PATCH /api/config rejects an invalid level', async ({ request }) => {
    const res = await request.patch('/api/config', {
      data: { logLevel: 'nonsense' },
    });
    expect(res.status()).toBe(400);
  });

  test('rejects unsupported methods on /api/config', async ({ request }) => {
    const res = await request.post('/api/config');
    expect(res.status()).toBe(405);
  });
});
