import { expect, test } from '../fixtures';
import { existsSync } from 'node:fs';
import { basename, join } from 'node:path';
import {
  buildEnclaveFixture,
  cliContainer,
  isolatedDaemon,
  projectAndWorkstream,
  removeEnclaveFixture,
} from '../enclave-helpers';
import { disposeIsolatedDaemons } from '../isolated-daemon';
import { browserServer } from './browser-server';
import { selectProject } from './workstream-helpers';

const container = 'enclave-codex-0123456789ab-feature';
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
});
