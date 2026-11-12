import { expect, test } from '../fixtures';
import { textContrast, useTheme } from './contrast';
import { loadApp } from './keyboard-helpers';
import { selectProject } from './workstream-helpers';

/**
 * Text and ended dots drawn in a state's or the accent's colour read in both themes: the
 * session state words and the project header's git pill, on the seeded demo
 * project, which has sessions in several states (an ended one, on its recessed
 * row, among them), and a workstream's branch.
 */
for (const theme of ['dark', 'light'] as const) {
  test(`state words, ended dots, the git pill and a branch read in the ${theme} theme`, async ({
    page,
  }) => {
    await useTheme(page, theme);
    await loadApp(page);
    await selectProject(page, 'homeshell');

    const words = page.getByRole('main').getByTestId('session-state');
    await expect(words.first()).toBeAttached();
    const measured: string[] = [];
    for (const word of await words.all()) {
      // Running and Idle are only read out: the dot shows them.
      if (((await word.boundingBox())?.width ?? 0) <= 1) continue;
      const text = (await word.textContent()) ?? '';
      measured.push(text);
      expect(await textContrast(word), text).toBeGreaterThanOrEqual(4.5);
    }
    expect(measured).toContain('Ended');
    // The hollow ring of an ended session, a graphic rather than text (3:1).
    const rings = page
      .getByRole('main')
      .getByRole('img', { name: 'Ended', exact: true });
    await expect(rings.first()).toBeAttached();
    for (const ring of await rings.all()) {
      expect(await textContrast(ring, 'borderTopColor')).toBeGreaterThanOrEqual(
        3,
      );
    }
    const gitPill = page.getByRole('main').getByText('git', { exact: true });
    expect(await textContrast(gitPill)).toBeGreaterThanOrEqual(4.5);

    await selectProject(page, 'infra-tools');
    const branch = page.getByRole('main').getByText('main', { exact: true });
    expect(await textContrast(branch)).toBeGreaterThanOrEqual(4.5);
  });
}
