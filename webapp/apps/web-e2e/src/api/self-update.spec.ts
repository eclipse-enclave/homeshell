import { expect, test } from '@playwright/test';
import { execFileSync } from 'node:child_process';
import { mkdtempSync, readFileSync, writeFileSync } from 'node:fs';
import { createServer, type Server } from 'node:http';
import type { AddressInfo } from 'node:net';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

import { buildDaemon, isolatedDaemon, type IsolatedDaemon } from '../isolated-daemon';

/**
 * A rolling build against a download server whose checksum does not match the
 * archive: the update must fail, leave the daemon running, and say why.
 */

const archive = `homeshell-${process.platform}-${process.arch === 'x64' ? 'amd64' : process.arch}.tar.gz`;

test.describe('a failed self-update', () => {
  let server: Server;
  let daemon: IsolatedDaemon;

  test.beforeAll(async () => {
    test.setTimeout(120_000);
    // A well-formed archive, so the checksum is what fails rather than the unpacking.
    const dir = mkdtempSync(join(tmpdir(), 'homeshell-self-update-'));
    writeFileSync(join(dir, 'homeshell'), 'not the binary the checksum names');
    execFileSync('tar', ['-czf', join(dir, archive), '-C', dir, 'homeshell']);
    const archiveBytes = readFileSync(join(dir, archive));
    server = createServer((req, res) => {
      const files: Record<string, string | Buffer> = {
        '/rolling/latest': `${'b'.repeat(40)}\n`,
        '/rolling/checksums.txt': `${'0'.repeat(64)}  ${archive}\n`,
        [`/rolling/${archive}`]: archiveBytes,
      };
      const body = files[req.url ?? ''];
      res.writeHead(body === undefined ? 404 : 200).end(body);
    });
    await new Promise<void>((r) => server.listen(0, '127.0.0.1', r));
    const { port } = server.address() as AddressInfo;
    const binary = buildDaemon(
      'self-update',
      `-X main.version=0.0.0-gaaaaaaa -X main.commit=${'a'.repeat(40)} -X main.channel=rolling`,
    );
    daemon = isolatedDaemon(binary, 'self-update', {
      HOMESHELL_UPDATE_BASE_URL: `http://127.0.0.1:${port}`,
    });
    await daemon.start();
  });

  test.afterAll(async () => {
    daemon?.dispose();
    await new Promise((r) => server?.close(r));
  });

  test('reports the checksum mismatch and keeps serving', async () => {
    const res = await daemon.api('/api/update', { method: 'POST' });
    expect(res.status).toBe(500);
    const body = (await res.json()) as { error: string };
    expect(body.error).toContain('checksum mismatch');

    expect((await daemon.api('/api/hello')).status).toBe(200);
  });
});
