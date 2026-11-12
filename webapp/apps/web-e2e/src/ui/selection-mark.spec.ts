import { expect, test } from '../fixtures';
import { styleOf, useTheme } from './contrast';
import { openWorkstreamCard } from './workstream-helpers';

/**
 * One selection mark for the selected project and the selected session row: the
 * same neutral tint, so teal keeps meaning running and primary. A row's
 * bordered buttons carry a fill of their own, so their borders show on it.
 */
for (const theme of ['dark', 'light'] as const) {
  test(`the selected project and session share a mark, and row buttons stand out (${theme})`, async ({
    page,
    request,
  }) => {
    await useTheme(page, theme);
    const { row } = await openWorkstreamCard(
      page,
      request,
      'hs-ui-selection',
      'selection',
    );
    await row.focus();
    await expect(row).toHaveAttribute('aria-current', 'true');
    const project = page
      .getByRole('navigation', { name: 'Projects' })
      .locator('[aria-current="true"]');

    const tint = await styleOf(row, 'background-color');
    expect(await styleOf(project, 'background-color')).toBe(tint);

    // The bar clears the status dot by the same gap on both.
    const barToDot = (item: typeof row) =>
      item.evaluate((node) => {
        type Box = { getBoundingClientRect: () => { left: number } };
        const { getComputedStyle } = globalThis as unknown as {
          getComputedStyle: (
            element: unknown,
            pseudo: string,
          ) => { left: string; width: string };
        };
        const bar = getComputedStyle(node, '::before');
        const dot = (
          node as unknown as { querySelector: (selector: string) => Box }
        ).querySelector('[role="img"]');
        const left = (node as unknown as Box).getBoundingClientRect().left;
        return (
          dot.getBoundingClientRect().left -
          left -
          parseFloat(bar.left) -
          parseFloat(bar.width)
        );
      });
    expect(await barToDot(project)).toBeCloseTo(await barToDot(row), 0);

    const open = row.getByRole('button', { name: 'Open' });
    const fill = await styleOf(open, 'background-color');
    expect(fill).not.toBe(tint);
    expect(fill).not.toMatch(/rgba\(.*, 0\)$|transparent/);
  });
}
