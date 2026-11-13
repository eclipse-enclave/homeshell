import type { APIResponse, Locator, Page } from '@playwright/test';
import { expect, test } from '../fixtures';
import { basename } from 'node:path';
import {
  buildEnclaveFixture,
  calls,
  createEnclaveSession,
  type EnclaveWorkstream,
  isolatedDaemon,
  projectAndWorkstream,
  removeEnclaveFixture,
} from '../enclave-helpers';
import { disposeIsolatedDaemons } from '../isolated-daemon';
import { pollFor } from '../api/workstream-helpers';
import { browserServer } from './browser-server';
import { selectProject } from './workstream-helpers';

test.describe('Enclave lifecycle controls', () => {
  test.skip(process.platform === 'win32', 'Host bridge requires Unix PTYs');
  test.beforeAll(buildEnclaveFixture);
  test.afterEach(disposeIsolatedDaemons);
  test.afterAll(removeEnclaveFixture);

  // Opens the card of an idle Enclave session in its own daemon, runs `body`
  // on it, then removes the session, the project and the daemon.
  async function withIdleSession(
    page: Page,
    body: (on: {
      daemon: Awaited<ReturnType<typeof isolatedDaemon>>;
      ws: EnclaveWorkstream;
      session: { id: string };
      prompt: (text: string) => Promise<APIResponse>;
      card: Locator;
      row: Locator;
    }) => Promise<void>,
  ) {
    const daemon = await isolatedDaemon();
    const browser = await browserServer(daemon);
    let removeProject: (() => Promise<void>) | undefined;
    try {
      const ws = await projectAndWorkstream(daemon);
      removeProject = ws.remove;
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
      await body({ daemon, ws, session, prompt, card, row });
    } finally {
      await page.close();
      await browser.close();
      await removeProject?.();
      await daemon.stop();
    }
  }

  test('Stop and Continue preserve a conversation; ambiguous Continue guides a fresh restart', async ({
    page,
  }, testInfo) => {
    test.setTimeout(60_000);
    await withIdleSession(
      page,
      async ({ daemon, ws, session, prompt, card, row }) => {
        await expect(row.getByTestId('session-state')).toHaveText('Idle');
        expect((await prompt('working')).status()).toBe(204);
        await expect(row.getByTestId('session-state')).toHaveText('Running');
        await row.getByRole('button', { name: 'Stop', exact: true }).click();
        await expect(row.getByTestId('session-state')).toHaveText('Stopped');
        await page.screenshot({
          path: testInfo.outputPath('enclave-stopped.png'),
        });
        await row
          .getByRole('button', { name: 'Continue', exact: true })
          .click();
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
        await row
          .getByRole('button', { name: 'Continue', exact: true })
          .click();
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
      },
    );
  });

  test('a stopped row continues, and one that ended on its own too', async ({
    page,
  }) => {
    test.setTimeout(60_000);
    await withIdleSession(page, async ({ daemon, prompt, row }) => {
      const state = row.getByTestId('session-state');

      // "Running" is for screen readers only: the dot already says it.
      expect((await prompt('working')).status()).toBe(204);
      await expect(state).toHaveText('Running');
      expect((await state.boundingBox())?.width).toBeLessThanOrEqual(1);

      await row.getByRole('button', { name: 'Stop', exact: true }).click();
      await expect(state).toHaveText('Stopped');
      await row.getByRole('button', { name: 'Continue', exact: true }).click();
      await expect(state).toHaveText(/^(Idle|Done)$/, { timeout: 15_000 });
      expect(calls(daemon.fixture, 'continue')).toHaveLength(1);

      // The agent exits: the daemon reports the session done, the row "Ended".
      expect((await prompt('exit')).status()).toBe(204);
      await expect(state).toHaveText('Ended', { timeout: 15_000 });
      await row.getByRole('button', { name: 'Continue', exact: true }).click();
      await expect(state).toHaveText(/^(Idle|Done)$/, { timeout: 15_000 });
      expect(calls(daemon.fixture, 'continue')).toHaveLength(2);
    });
  });
});
