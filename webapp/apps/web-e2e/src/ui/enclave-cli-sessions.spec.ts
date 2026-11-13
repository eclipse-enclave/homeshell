import type { Page } from '@playwright/test';
import { expect, test } from '../fixtures';
import { existsSync, rmSync } from 'node:fs';
import { basename, join } from 'node:path';
import {
  betweenChecks,
  buildEnclaveFixture,
  calls,
  cliContainer,
  isolatedDaemon,
  projectAndWorkstream,
  removeEnclaveFixture,
} from '../enclave-helpers';
import { disposeIsolatedDaemons } from '../isolated-daemon';
import { browserServer } from './browser-server';
import { selectProject } from './workstream-helpers';

const container = 'enclave-codex-0123456789ab-feature';
// What a plain `enclave` in a terminal starts: the project's default container.
const unnamed = 'enclave-codex-0123456789ab';
const takeOverWarning =
  'This ends the session in its terminal and continues its conversation in HomeShell.';

test.describe('Enclave sessions started with the CLI', () => {
  test.skip(process.platform === 'win32', 'Host bridge requires Unix PTYs');
  test.beforeAll(buildEnclaveFixture);
  test.afterEach(disposeIsolatedDaemons);
  test.afterAll(removeEnclaveFixture);

  test('a foreground CLI session is stopped only after confirming, and restarts in HomeShell', async ({
    page,
  }) => {
    test.setTimeout(90_000);
    const daemon = await isolatedDaemon({ inventoryMs: 200 });
    const browser = await browserServer(daemon);
    const ws = await projectAndWorkstream(daemon);
    try {
      const dir = join(daemon.root, 'project');
      const startInTerminal = () =>
        cliContainer(daemon.fixture, {
          name: container,
          session: '',
          dir,
          background: false,
        });
      startInTerminal();
      await page.goto(browser.url);
      await selectProject(page, basename(dir));
      const card = page.getByRole('region', { name: 'Workstream enclave' });
      const row = card.locator(`[data-nav-id="${container}"]`);
      const state = row.getByTestId('session-state');
      const note =
        'Running in a terminal. HomeShell cannot show its screen or send it text.';

      await expect(state).toHaveText('Running');
      await expect(row).toContainText(note);
      await expect(
        row.getByPlaceholder('Send text to the session…'),
      ).toHaveCount(0);

      // Cancelling the confirmation leaves the container running.
      await row.getByRole('button', { name: 'Stop', exact: true }).click();
      const stop = page.getByRole('dialog', { name: 'Stop "codex session"?' });
      await expect(stop).toContainText(
        'It runs in a terminal; stopping it ends the session there too.',
      );
      await stop.getByRole('button', { name: 'Cancel' }).click();
      await expect(stop).toBeHidden();
      expect(
        existsSync(join(daemon.fixture, `container-${container}.json`)),
      ).toBe(true);
      await expect(state).toHaveText('Running');

      // Restarting it with settings ends it in its terminal too, and says so.
      await row.getByTestId('session-title').click({ button: 'right' });
      await page
        .getByRole('menu', { name: /^Actions for session / })
        .getByRole('menuitem', { name: 'Restart with settings…' })
        .click();
      const restartWithSettings = page.getByRole('dialog', {
        name: 'Restart codex session',
      });
      await expect(restartWithSettings).toContainText(takeOverWarning);
      await restartWithSettings.getByRole('button', { name: 'Cancel' }).click();
      await expect(restartWithSettings).toBeHidden();

      await row.getByRole('button', { name: 'Stop', exact: true }).click();
      await stop.getByRole('button', { name: 'Stop', exact: true }).click();
      await expect(state).toHaveText('Stopped');
      expect(
        existsSync(join(daemon.fixture, `container-${container}.json`)),
      ).toBe(false);

      // A stopped session in the folder would make continuing a conversation
      // ambiguous, so it goes before the terminal starts another.
      expect((await daemon.api.delete(`${ws.base}/${container}`)).ok()).toBe(
        true,
      );
      await expect(row).toHaveCount(0);
      startInTerminal();
      await expect(row).toContainText(note);

      await row
        .getByRole('button', { name: 'Restart in HomeShell', exact: true })
        .click();
      const restart = page.getByRole('dialog', {
        name: 'Restart "codex session" in HomeShell?',
      });
      await expect(restart).toContainText(takeOverWarning);
      await restart
        .getByRole('button', { name: 'Restart in HomeShell', exact: true })
        .click();
      // The restarted session answers to the same row.
      await expect(row.getByText('Ready')).toBeVisible({ timeout: 20_000 });
      await expect(card.getByText(note)).toHaveCount(0);
      await expect(
        card.getByRole('button', { name: 'Restart in HomeShell' }),
      ).toHaveCount(0);
    } finally {
      await page.close();
      await browser.close();
      await ws.remove();
      await daemon.stop();
    }
  });

  // A daemon, a browser on it, and the project card, with an unnamed session
  // started in a terminal.
  async function unnamedInTerminal(page: Page) {
    const daemon = await isolatedDaemon({ inventoryMs: 200 });
    const browser = await browserServer(daemon);
    const ws = await projectAndWorkstream(daemon);
    const dir = join(daemon.root, 'project');
    const startInTerminal = () =>
      cliContainer(daemon.fixture, {
        name: unnamed,
        session: '',
        dir,
        background: false,
      });
    startInTerminal();
    await page.goto(browser.url);
    await selectProject(page, basename(dir));
    const card = page.getByRole('region', { name: 'Workstream enclave' });
    const row = card.locator(`[data-nav-id="${unnamed}"]`);
    const close = async () => {
      await page.close();
      await browser.close();
      await ws.remove();
      await daemon.stop();
    };
    return { daemon, card, row, startInTerminal, close };
  }

  test('Restart in HomeShell keeps the conversation of an unnamed terminal session', async ({
    page,
  }) => {
    test.setTimeout(90_000);
    const { card, row, close } = await unnamedInTerminal(page);
    try {
      await row
        .getByRole('button', { name: 'Restart in HomeShell', exact: true })
        .click();
      await page
        .getByRole('dialog', { name: 'Restart "codex session" in HomeShell?' })
        .getByRole('button', { name: 'Restart in HomeShell', exact: true })
        .click();
      // The same row goes on: under another name its conversation would be
      // missing and the agent would exit.
      await expect(row.getByText('Ready')).toBeVisible({ timeout: 20_000 });
      await expect(card.locator('[data-nav-id]')).toHaveCount(1);
    } finally {
      await close();
    }
  });

  test('Continue of an ended row leaves a new terminal session under its name running', async ({
    page,
  }) => {
    test.setTimeout(90_000);
    const { daemon, row, startInTerminal, close } =
      await unnamedInTerminal(page);
    try {
      const state = row.getByTestId('session-state');
      await expect(state).toHaveText('Running');
      rmSync(join(daemon.fixture, `container-${unnamed}.json`));
      const continueButton = row.getByRole('button', { name: 'Continue' });
      await expect(continueButton).toBeVisible();

      // Started again in the terminal, and Continue clicked before the next
      // check found it.
      const stopsBefore = calls(daemon.fixture, 'stop').length;
      await betweenChecks(daemon.fixture, async () => {
        startInTerminal();
        await continueButton.click();
      });
      await expect(page.getByRole('alert')).toContainText(
        'This session is running again, started outside HomeShell.',
      );
      await expect(state).toHaveText('Running');
      await expect(
        row.getByText('Running in a terminal.', { exact: false }),
      ).toBeVisible();
      expect(calls(daemon.fixture, 'stop')).toHaveLength(stopsBefore);
      expect(calls(daemon.fixture, 'continue')).toHaveLength(0);
    } finally {
      await close();
    }
  });
});
