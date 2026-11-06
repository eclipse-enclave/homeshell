import { type Locator, type Page } from '@playwright/test';
import { expect, test } from '../fixtures';
import { controlEventStream } from './event-stream';
import { openWorkstreamCard } from './workstream-helpers';

/**
 * A title is how what it names is renamed: double-click, F2 or Alt+R. There is
 * no control beside it; a faint tint on hovering it (and while Alt reaches it)
 * says it can be, and nothing moves when the tint shows or the rename starts.
 */

test.use({ viewport: { width: 1400, height: 900 } });

async function box(locator: Locator) {
  const bounds = await locator.boundingBox();
  if (!bounds) throw new Error('element has no box');
  return bounds;
}

/** What the tint is drawn with. */
const tint = (title: Locator) =>
  title.evaluate((element) => {
    const style = (
      globalThis as unknown as {
        getComputedStyle: (e: unknown) => {
          backgroundColor: string;
          boxShadow: string;
        };
      }
    ).getComputedStyle(element);
    return `${style.backgroundColor} ${style.boxShadow}`;
  });

async function titles(
  page: Page,
  request: Parameters<typeof openWorkstreamCard>[1],
) {
  const opened = await openWorkstreamCard(
    page,
    request,
    'hs-ui-title',
    'title',
  );
  // The tint is read as it shows, not after its transition.
  await page.addStyleTag({ content: '* { transition: none !important; }' });
  const { card, row, workstreamName, projectPrefix } = opened;
  return {
    ...opened,
    project: page.getByRole('heading', {
      level: 1,
      name: new RegExp(`^${projectPrefix}`),
    }),
    cardTitle: card.getByRole('heading', { name: workstreamName }),
    cardPill: card.getByText('Fake Provider', { exact: true }),
    sessionTitle: row.getByTestId('session-title'),
    // The session's title and its agent pill both read "Claude Code".
    sessionPill: row.getByText('Claude Code', { exact: true }).nth(1),
  };
}

test('hovering a title and holding Alt moves nothing', async ({
  page,
  request,
}) => {
  const { cardTitle, cardPill, sessionTitle, sessionPill, card } = await titles(
    page,
    request,
  );
  await card.focus();
  for (const [title, pill] of [
    [cardTitle, cardPill],
    [sessionTitle, sessionPill],
  ]) {
    const before = await Promise.all([box(title), box(pill)]);
    await title.hover();
    expect(await Promise.all([box(title), box(pill)])).toEqual(before);
    await page.keyboard.down('Alt');
    expect(await Promise.all([box(title), box(pill)])).toEqual(before);
    await page.keyboard.up('Alt');
  }
});

test('a title is tinted on hover, and while Alt+R would rename it', async ({
  page,
  request,
}) => {
  const { cardTitle, sessionTitle, card } = await titles(page, request);
  for (const title of [cardTitle, sessionTitle]) {
    await page.mouse.move(0, 0);
    const rest = await tint(title);
    await title.hover();
    expect(await tint(title)).not.toBe(rest);
  }

  // Alt tints the title of the selection only.
  await page.mouse.move(0, 0);
  await card.focus();
  const rest = await tint(cardTitle);
  await page.keyboard.down('Alt');
  expect(await tint(cardTitle)).not.toBe(rest);
  expect(await tint(sessionTitle)).toBe(rest);
  await page.keyboard.up('Alt');
});

test('a title that cannot be renamed is not tinted', async ({
  page,
  request,
}) => {
  const stream = await controlEventStream(page);
  const { cardTitle, sessionTitle } = await titles(page, request);
  await stream.drop();
  await expect(page.getByText('daemon offline')).toBeVisible();
  for (const title of [cardTitle, sessionTitle]) {
    await expect(title).not.toHaveAttribute('data-mnemonic');
    await page.mouse.move(0, 0);
    const rest = await tint(title);
    await title.hover();
    expect(await tint(title)).toBe(rest);
  }
  await stream.restore();
});

test('the rename box sits where the title was', async ({ page, request }) => {
  const { project, cardTitle, sessionTitle, card, row } = await titles(
    page,
    request,
  );
  for (const [title, input] of [
    [project, page.getByRole('textbox', { name: 'Project name' })],
    [cardTitle, card.getByRole('textbox', { name: 'Workstream name' })],
    [sessionTitle, row.getByRole('textbox', { name: 'Session name' })],
  ]) {
    const before = await box(title);
    await title.dblclick();
    await expect(input).toBeFocused();
    const after = await box(input);
    expect(Math.abs(after.x - before.x)).toBeLessThanOrEqual(2);
    expect(Math.abs(after.y - before.y)).toBeLessThanOrEqual(2);
    expect(Math.abs(after.height - before.height)).toBeLessThanOrEqual(2);
    await page.keyboard.press('Escape');
    await expect(input).toHaveCount(0);
  }
});

// The box must have focus before the next key arrives: with no wait between
// Alt+R and typing, a rename that starts a task late loses the first keys to
// whatever had focus (the regression was "er-…-renamed" for a typed name).
test('keys typed straight after Alt+R all reach the rename box', async ({
  page,
  request,
}) => {
  const { card, row } = await titles(page, request);
  const header = page.getByRole('main').locator('header').first();
  const selection = page.locator('[data-mnemonic-scope][aria-current="true"]');
  for (const [focus, input] of [
    // Nothing selected: Alt+R is the page's, which renames the project.
    [
      header.getByRole('button', { name: 'New workstream' }),
      page.getByRole('textbox', { name: 'Project name' }),
    ],
    [card, card.getByRole('textbox', { name: 'Workstream name' })],
    [row, row.getByRole('textbox', { name: 'Session name' })],
  ] as const) {
    await focus.focus();
    if (focus === card || focus === row) {
      await expect(focus).toHaveAttribute('aria-current', 'true');
    } else {
      await expect(selection).toHaveCount(0);
    }
    await page.keyboard.press('Alt+r');
    await page.keyboard.type('abcdefgh');
    await expect(input).toHaveValue('abcdefgh');
    await page.keyboard.press('Escape');
    await expect(input).toHaveCount(0);
  }
});

// A session's header toggles its details on a click, and the first click of a
// double-click is one; renaming by double-click must still leave them as they
// were, while a single click keeps toggling at once.
test('double-clicking a session title renames it and leaves its details as they were', async ({
  page,
  request,
}) => {
  const { row, sessionTitle } = await titles(page, request);
  const toggle = row.getByRole('button', { name: 'Toggle session details' });
  const panelId = await toggle.getAttribute('aria-controls');
  const panel = page.locator(`[id="${panelId}"]`);
  const input = row.getByRole('textbox', { name: 'Session name' });
  const expectDetails = async (open: boolean) => {
    await expect(toggle).toHaveAttribute('aria-expanded', String(open));
    await expect(panel).toHaveCount(open ? 1 : 0);
  };

  for (const open of [false, true]) {
    if ((await toggle.getAttribute('aria-expanded')) !== String(open)) {
      await toggle.click();
    }
    await expectDetails(open);

    await sessionTitle.dblclick();
    await expect(input).toBeFocused();
    await expectDetails(open);
    await page.keyboard.press('Escape');
    await expect(input).toHaveCount(0);
    await expectDetails(open);

    // A single click on the title still toggles them.
    await sessionTitle.click();
    await expectDetails(!open);
    await sessionTitle.click();
    await expectDetails(open);
  }
});

test("a card's and a session's menu show Rename with F2", async ({
  page,
  request,
}) => {
  const { cardTitle, sessionTitle } = await titles(page, request);
  for (const title of [cardTitle, sessionTitle]) {
    await title.click({ button: 'right' });
    const rename = page.getByRole('menuitem', { name: 'Rename' });
    await expect(rename).toHaveText(/^Rename\s*F2$/);
    await expect(rename).toHaveAttribute('aria-keyshortcuts', 'F2');
    await page.keyboard.press('Escape');
    await expect(page.getByRole('menu')).toHaveCount(0);
  }
});
