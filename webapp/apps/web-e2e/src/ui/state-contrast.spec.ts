import { expect, test } from '../fixtures';
import { textContrast, useTheme } from './contrast';
import { loadApp } from './keyboard-helpers';
import { selectProject } from './workstream-helpers';

/**
 * Text drawn in a state's or the accent's colour reads in both themes: the
 * session state badges and the project header's git pill, on the seeded demo
 * project, which has sessions in several states.
 */
for (const theme of ['dark', 'light'] as const) {
  test(`state badges and the git pill read in the ${theme} theme`, async ({
    page,
  }) => {
    await useTheme(page, theme);
    await loadApp(page);
    await selectProject(page, 'homeshell');

    const badges = page.getByRole('main').getByTestId('session-state');
    await expect(badges.first()).toBeVisible();
    for (const badge of await badges.all()) {
      expect(
        await textContrast(badge),
        `${await badge.textContent()} badge`,
      ).toBeGreaterThanOrEqual(4.5);
    }
    const gitPill = page.getByRole('main').getByText('git', { exact: true });
    expect(await textContrast(gitPill)).toBeGreaterThanOrEqual(4.5);
  });
}
