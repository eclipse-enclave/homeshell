import { expect, test } from '@playwright/test';
import { mkdtemp, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

async function createTempDir(): Promise<string> {
  return mkdtemp(join(tmpdir(), 'hs-browse-e2e-'));
}

/**
 * Black-box tests for the browse endpoint: directory listing for the folder
 * picker. UI-free so they survive a daemon rewrite.
 */
test.describe('browse API', () => {
  test('GET /api/browse defaults to home directory', async ({ request }) => {
    const res = await request.get('/api/browse');
    expect(res.status()).toBe(200);
    const body = await res.json();
    expect(body.path).toBeTruthy();
    expect(Array.isArray(body.entries)).toBe(true);
    // Root or home should always have a parent (unless it's the filesystem root).
    expect(typeof body.parent).toBe('string');
  });

  test('GET /api/browse lists subdirectories of explicit path', async ({
    request,
  }) => {
    const { mkdir } = await import('node:fs/promises');
    const dir = await createTempDir();
    await mkdir(join(dir, 'alpha'));
    await mkdir(join(dir, 'beta'));
    await writeFile(join(dir, 'file.txt'), 'x');

    const res = await request.get(
      `/api/browse?path=${encodeURIComponent(dir)}`,
    );
    expect(res.status()).toBe(200);
    const body = await res.json();

    expect(body.path).toBe(dir);
    const names = body.entries.map((entry: { name: string }) => entry.name);
    expect(names).toContain('alpha');
    expect(names).toContain('beta');
    // Files should be excluded.
    expect(names).not.toContain('file.txt');
  });

  test('GET /api/browse returns 400 for non-existent path', async ({
    request,
  }) => {
    const res = await request.get(
      '/api/browse?path=/no/such/directory/zzz-e2e',
    );
    expect(res.status()).toBe(400);
  });

  test('GET /api/browse returns 400 for a file path', async ({ request }) => {
    const dir = await createTempDir();
    const filePath = join(dir, 'afile');
    await writeFile(filePath, 'x');

    const res = await request.get(
      `/api/browse?path=${encodeURIComponent(filePath)}`,
    );
    expect(res.status()).toBe(400);
  });

  test('GET /api/browse returns parent path', async ({ request }) => {
    const { mkdir } = await import('node:fs/promises');
    const dir = await createTempDir();
    const child = join(dir, 'child');
    await mkdir(child);

    const res = await request.get(
      `/api/browse?path=${encodeURIComponent(child)}`,
    );
    expect(res.status()).toBe(200);
    const body = await res.json();
    expect(body.parent).toBe(dir);
  });

  test('GET /api/browse excludes hidden directories', async ({ request }) => {
    const { mkdir } = await import('node:fs/promises');
    const dir = await createTempDir();
    await mkdir(join(dir, '.hidden'));
    await mkdir(join(dir, 'visible'));

    const res = await request.get(
      `/api/browse?path=${encodeURIComponent(dir)}`,
    );
    expect(res.status()).toBe(200);
    const body = await res.json();
    const names = body.entries.map((entry: { name: string }) => entry.name);
    expect(names).toContain('visible');
    expect(names).not.toContain('.hidden');
  });
});
