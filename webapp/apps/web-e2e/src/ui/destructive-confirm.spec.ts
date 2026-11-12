import { type Locator } from '@playwright/test';
import { expect, test } from '../fixtures';
import { styleOf, textContrast, useTheme } from './contrast';
import { loadApp } from './keyboard-helpers';
import { materializedWorkstream, selectProject } from './workstream-helpers';

/**
 * A confirm that destroys something is drawn apart from the accent's harmless
 * primary, and still reads, in both themes.
 */
const backgroundOf = (button: Locator) => styleOf(button, 'background-color');

for (const theme of ['dark', 'light'] as const) {
  test(`destructive confirms look it in the ${theme} theme`, async ({
    page,
    request,
  }) => {
    await useTheme(page, theme);
    const { dirName, workstreamName } = await materializedWorkstream(
      request,
      'hs-ui-danger-',
      'danger',
    );
    await loadApp(page);
    await selectProject(page, dirName);
    const main = page.getByRole('main');
    const primary = await backgroundOf(
      main.getByRole('button', { name: 'New session' }),
    );

    await main
      .getByRole('button', { name: `Delete workstream ${workstreamName}` })
      .click();
    const deleteDialog = page.getByRole('dialog', {
      name: `Delete "${workstreamName}"?`,
    });
    const deleteButton = deleteDialog.getByRole('button', {
      name: 'Delete',
      exact: true,
    });
    expect(await backgroundOf(deleteButton)).not.toBe(primary);
    expect(await textContrast(deleteButton)).toBeGreaterThanOrEqual(4.5);
    await deleteDialog.getByRole('button', { name: 'Cancel' }).click();

    await main
      .getByRole('button', { name: `Delete project ${dirName}`, exact: true })
      .click();
    const removeButton = page
      .getByRole('dialog', { name: `Delete "${dirName}"?` })
      .getByRole('button', { name: 'Delete' });
    expect(await backgroundOf(removeButton)).not.toBe(primary);
    expect(await textContrast(removeButton)).toBeGreaterThanOrEqual(4.5);
  });
}
