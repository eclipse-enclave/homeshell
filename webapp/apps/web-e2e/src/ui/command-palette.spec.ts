import { expect, test } from '../fixtures';
import { loadApp, searchCommands } from './keyboard-helpers';
import { selectProject, workstreamWithSessions } from './workstream-helpers';

/**
 * The VS Code-style entry points: the command palette (Ctrl/Cmd+Shift+P, F1)
 * and quick open (Ctrl/Cmd+P). Ctrl/Cmd+, for settings is in
 * keyboard-panels.spec.ts.
 */

test('the command palette runs a command by its fuzzy name', async ({
  page,
}) => {
  await loadApp(page);
  await page.keyboard.press('ControlOrMeta+Shift+P');
  const palette = page.getByRole('dialog', { name: 'Command palette' });
  await expect(palette).toBeVisible();

  const box = palette.getByRole('combobox');
  await expect(box).toBeFocused();
  await box.fill('>opset');
  await expect(palette.getByRole('option').first()).toContainText(
    'Open settings',
  );
  await page.keyboard.press('Enter');

  await expect(palette).toHaveCount(0);
  await expect(
    page.getByRole('heading', { level: 1, name: 'Settings' }),
  ).toBeVisible();
});

test('the highlighted command stands out, and the highlight moves', async ({
  page,
}) => {
  await loadApp(page);
  await page.keyboard.press('F1');
  const options = page
    .getByRole('dialog', { name: 'Command palette' })
    .getByRole('option');
  await expect(options.nth(1)).toBeVisible();
  const background = (index: number) =>
    options.nth(index).evaluate(
      (element) =>
        (
          globalThis as unknown as {
            getComputedStyle: (e: unknown) => { backgroundColor: string };
          }
        ).getComputedStyle(element).backgroundColor,
    );
  await page.mouse.move(0, 0);
  const highlighted = await background(0);
  expect(highlighted).not.toBe(await background(1));

  await page.keyboard.press('ArrowDown');
  await expect(options.nth(1)).toHaveAttribute('aria-selected', 'true');
  await expect.poll(() => background(1)).toBe(highlighted);

  // A row under the pointer must not pass for a second highlight.
  await options.nth(2).hover();
  await expect.poll(() => background(2)).not.toBe(highlighted);
});

test('a long name leaves the command and its key in view', async ({
  page,
  request,
}) => {
  const { projectPrefix, workstreamName } = await workstreamWithSessions(
    request,
    'hs-ui-palette-long',
    'a-workstream-name-long-enough-to-crowd-out-the-verb',
    0,
  );
  await loadApp(page);
  await selectProject(page, projectPrefix);
  const card = page.getByRole('region', {
    name: `Workstream ${workstreamName}`,
  });
  await card.focus();

  // Found by the end of its name, which the row does not show.
  const palette = await searchCommands(
    page,
    `${workstreamName.slice(-6)} new session`,
  );
  const label = `Workstream "${workstreamName}": New session`;
  // Named as the one label, not as the pieces it is drawn in.
  const option = palette.getByRole('option', { name: label });
  await expect(option).toBeVisible();

  // The name is cut off, and nothing else: the label around it is whole, and
  // the key after it inside the row.
  const name = option.getByText(workstreamName, { exact: true });
  await expect
    .poll(() =>
      name.evaluate((element) => element.scrollWidth > element.clientWidth),
    )
    .toBe(true);
  const whole = name.locator('..');
  await expect
    .poll(() =>
      whole.evaluate((element) => element.scrollWidth <= element.clientWidth),
    )
    .toBe(true);
  const [optionBox, wholeBox, shortcutBox] = await Promise.all([
    option.boundingBox(),
    whole.boundingBox(),
    option.locator('kbd').boundingBox(),
  ]);
  if (!optionBox || !wholeBox || !shortcutBox) {
    throw new Error('the option is not laid out');
  }
  expect(wholeBox.x + wholeBox.width).toBeLessThanOrEqual(shortcutBox.x);
  expect(shortcutBox.x + shortcutBox.width).toBeLessThanOrEqual(
    optionBox.x + optionBox.width,
  );

  // The tooltip is the whole label, not the cut one.
  await name.hover();
  await expect(name).toHaveAttribute('title', label);
});

test('F1 opens it too, and Escape gives focus back', async ({ page }) => {
  await loadApp(page);
  const logs = page.getByRole('button', { name: 'Logs' });
  await logs.focus();
  await page.keyboard.press('F1');
  const palette = page.getByRole('dialog', { name: 'Command palette' });
  await expect(palette).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(palette).toHaveCount(0);
  await expect(logs).toBeFocused();
});

test('picking Go to from the command palette switches it to quick open', async ({
  page,
}) => {
  await loadApp(page);
  await page.keyboard.press('F1');
  const box = page
    .getByRole('dialog', { name: 'Command palette' })
    .getByRole('combobox');
  await box.fill('>Go to a project');
  await page.keyboard.press('Enter');

  const goTo = page.getByRole('dialog', { name: 'Go to' });
  await expect(goTo).toBeVisible();
  await expect(goTo.getByRole('combobox')).toHaveValue('');
  await expect(goTo.getByRole('combobox')).toBeFocused();
});

test('quick open jumps to a workstream in another project', async ({
  page,
  request,
}) => {
  const { workstreamName } = await workstreamWithSessions(
    request,
    'hs-ui-quick-open',
    'quick-open',
  );
  await loadApp(page);
  await page.keyboard.press('ControlOrMeta+P');
  const palette = page.getByRole('dialog', { name: 'Go to' });
  await palette.getByRole('combobox').fill(workstreamName);
  await expect(palette.getByRole('option').first()).toContainText(
    workstreamName,
  );
  await page.keyboard.press('Enter');

  await expect(
    page.getByRole('region', { name: `Workstream ${workstreamName}` }),
  ).toBeFocused();
});

test('a leading > switches between going somewhere and commands', async ({
  page,
}) => {
  await loadApp(page);
  await page.keyboard.press('ControlOrMeta+P');
  await expect(page.getByRole('dialog', { name: 'Go to' })).toBeVisible();
  // One box, whichever of the two the dialog is.
  const box = page.getByRole('dialog').getByRole('combobox');

  await box.press('>');
  await expect(
    page.getByRole('dialog', { name: 'Command palette' }),
  ).toBeVisible();
  await expect(
    page.getByRole('option', { name: /^Open settings/ }),
  ).toBeVisible();

  await box.press('Backspace');
  await expect(page.getByRole('dialog', { name: 'Go to' })).toBeVisible();
});

// Its own keys open it anew, whatever was typed into it.
test('Ctrl/Cmd+P in the command palette opens Go to afresh', async ({
  page,
}) => {
  await loadApp(page);
  await page.keyboard.press('F1');
  await page.keyboard.type('toggle');
  await page.keyboard.press('ControlOrMeta+P');

  const goTo = page.getByRole('dialog', { name: 'Go to' });
  await expect(goTo).toBeVisible();
  await expect(goTo.getByRole('combobox')).toHaveValue('');
  await expect(goTo.getByRole('combobox')).toBeFocused();
});
