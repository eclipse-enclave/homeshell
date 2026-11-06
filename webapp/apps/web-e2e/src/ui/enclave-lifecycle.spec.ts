import { expect, test } from '@playwright/test';
import { createServer, request as proxyRequest } from 'node:http';
import { basename } from 'node:path';
import {
  buildEnclaveFixture,
  calls,
  createEnclaveSession,
  isolatedDaemon,
  projectAndWorkstream,
  removeEnclaveFixture,
} from '../enclave-helpers';
import { pollFor } from '../api/workstream-helpers';
import { selectProject } from './workstream-helpers';

// Serve Vite and the isolated real daemon at one origin, including streaming SSE.
// No provider responses or lifecycle endpoints are mocked in these UI tests.
async function browserServer(
  daemon: Awaited<ReturnType<typeof isolatedDaemon>>,
) {
  const server = createServer((request, response) => {
    const target = new URL(
      request.url ?? '/',
      request.url?.startsWith('/api/')
        ? daemon.url
        : (process.env.HOMESHELL_WEB_URL ?? 'http://localhost:4201'),
    );
    const upstream = proxyRequest(
      target,
      {
        method: request.method,
        headers: {
          ...request.headers,
          host: target.host,
          origin: target.origin,
          authorization: `Bearer ${daemon.token}`,
        },
      },
      (incoming) => {
        response.writeHead(incoming.statusCode ?? 502, incoming.headers);
        incoming.pipe(response);
      },
    );
    upstream.on('error', () => {
      response.writeHead(502);
      response.end();
    });
    response.on('close', () => upstream.destroy());
    request.pipe(upstream);
  });
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve));
  const address = server.address();
  if (!address || typeof address === 'string')
    throw new Error('proxy has no address');
  return {
    url: `http://127.0.0.1:${address.port}`,
    close: async () => {
      server.closeAllConnections();
      await new Promise<void>((resolve, reject) =>
        server.close((error) => (error ? reject(error) : resolve())),
      );
    },
  };
}

test.describe('Enclave lifecycle controls', () => {
  test.skip(process.platform === 'win32', 'Host bridge requires Unix PTYs');
  test.beforeAll(() => {
    test.setTimeout(120_000);
    buildEnclaveFixture();
  });
  test.afterAll(removeEnclaveFixture);

  test('Stop and Start preserve a conversation; ambiguous Start guides a fresh restart', async ({
    page,
  }, testInfo) => {
    test.setTimeout(60_000);
    const daemon = await isolatedDaemon();
    const browser = await browserServer(daemon);
    let project: string | undefined;
    try {
      const ws = await projectAndWorkstream(daemon);
      project = ws.id;
      await createEnclaveSession(daemon.api, ws.base);
      const session = await pollFor('idle Enclave session', async () =>
        (await ws.sessions()).find((s) => s.state === 'idle'),
      );
      const prompt = (text: string) =>
        daemon.api.post(`${ws.base}/${session.id}/prompt`, { data: { text } });
      await page.goto(browser.url);
      await selectProject(page, basename(`${daemon.root}/project`));
      const card = page.getByRole('region', { name: 'Workstream enclave' });
      const row = card.locator(`[data-nav-id="${session.id}"]`);
      await expect(row.getByTestId('session-state')).toHaveText('Idle');
      expect((await prompt('working')).status()).toBe(204);
      await expect(row.getByTestId('session-state')).toHaveText('Running');
      await row.getByRole('button', { name: 'Stop', exact: true }).click();
      await expect(row.getByTestId('session-state')).toHaveText('Stopped');
      await page.screenshot({
        path: testInfo.outputPath('enclave-stopped.png'),
      });
      await row.getByRole('button', { name: 'Start', exact: true }).click();
      await expect(row.getByTestId('session-state')).toHaveText(
        /^(Idle|Done)$/,
        { timeout: 15_000 },
      );
      expect(calls(daemon.fixture, 'continue')).toHaveLength(1);
      expect((await ws.sessions())[0]).toMatchObject({
        id: session.id,
        state: 'idle',
      });

      // A second conversation of the same tool makes `continue` ambiguous.
      await createEnclaveSession(daemon.api, ws.base);
      await expect(card.getByTestId('session-state')).toHaveText([
        /^(Idle|Done)$/,
        /^(Idle|Done)$/,
      ]);
      expect((await prompt('working')).status()).toBe(204);
      await expect(row.getByTestId('session-state')).toHaveText('Running');
      await row.getByRole('button', { name: 'Stop', exact: true }).click();
      await expect(row.getByTestId('session-state')).toHaveText('Stopped');
      await row.getByRole('button', { name: 'Start', exact: true }).click();
      await expect(
        page
          .getByRole('alert')
          .filter({ hasText: 'cannot start this session safely' }),
      ).toContainText('Restart with settings');
      await expect(row.getByTestId('session-state')).toHaveText('Stopped');
      expect(calls(daemon.fixture, 'continue')).toHaveLength(1);
      await row.focus();
      await page.keyboard.press('Shift+F10');
      await page
        .getByRole('menuitem', { name: 'Restart with settings…' })
        .click();
      const dialog = page.getByRole('dialog', { name: /^Restart / });
      await expect(
        dialog.getByRole('checkbox', { name: 'Start a fresh conversation' }),
      ).toBeChecked();
      await page.screenshot({
        path: testInfo.outputPath('enclave-fresh-restart.png'),
      });
      await dialog
        .getByRole('button', { name: 'Restart', exact: true })
        .click();
      await expect(row.getByTestId('session-state')).toHaveText(
        /^(Idle|Done)$/,
        { timeout: 15_000 },
      );
      expect(calls(daemon.fixture, 'run')).toHaveLength(3);
    } finally {
      await page.close();
      await browser.close();
      if (project) await daemon.api.delete(`/api/projects/${project}`);
      await daemon.stop();
    }
  });
});
