import {
  type APIRequestContext,
  type Locator,
  type Page,
} from '@playwright/test';
import { expect, test } from '../fixtures';
import { textContrast, useTheme } from './contrast';
import {
  openNewWorkstreamForm,
  selectEmptyProject,
} from './workstream-helpers';

/**
 * The pills a create form chooses with: the checked one reads as chosen
 * whatever the pointer or focus is doing to the others. And its other fields
 * look disabled while it is.
 */

interface Look {
  color: string;
  backgroundColor: string;
  borderColor: string;
  boxShadow: string;
  fontWeight: string;
  outlineStyle: string;
}

// Runs in the browser, but is type-checked under the e2e tsconfig, which has no
// DOM lib — hence globalThis, as in session-output.spec.ts.
const lookOf = (pill: Locator): Promise<Look> =>
  pill.evaluate((element) => {
    const style = (
      globalThis as unknown as { getComputedStyle: (e: unknown) => Look }
    ).getComputedStyle(element);
    return {
      color: style.color,
      backgroundColor: style.backgroundColor,
      borderColor: style.borderColor,
      boxShadow: style.boxShadow,
      fontWeight: style.fontWeight,
      outlineStyle: style.outlineStyle,
    };
  });

/** Opens a fresh project's new-workstream form, on the name. */
async function openForm(page: Page, request: APIRequestContext) {
  await selectEmptyProject(page, request, 'hs-ui-pills-');
  return openNewWorkstreamForm(page);
}

test('the checked pill keeps its look under the pointer, and stands apart from a hovered one', async ({
  page,
  request,
}) => {
  const form = await openForm(page, request);
  // Without transitions, a look read right after the pointer moves is final.
  await page.addStyleTag({ content: '*{transition:none!important}' });
  await form.getByRole('radio', { name: 'Default' }).click();
  const checked = form.getByRole('radio', { name: 'in-place' });
  const other = form.getByRole('radio', { name: 'copy' });
  await expect(checked).toHaveAttribute('aria-checked', 'true');

  await page.mouse.move(0, 0);
  const resting = await lookOf(checked);
  const otherResting = await lookOf(other);
  expect(Number(resting.fontWeight)).toBeGreaterThanOrEqual(600);
  expect(resting.backgroundColor).not.toBe(otherResting.backgroundColor);

  await checked.hover();
  expect(await lookOf(checked)).toEqual(resting);

  await other.hover();
  const otherHovered = await lookOf(other);
  expect(otherHovered).not.toEqual(otherResting);
  expect(otherHovered.backgroundColor).not.toBe(resting.backgroundColor);
  expect(Number(otherHovered.fontWeight)).toBeLessThan(600);
});

test('the checked pill reads in the light theme', async ({ page, request }) => {
  await useTheme(page, 'light');
  const form = await openForm(page, request);
  const checked = form.locator('[role="radio"][aria-checked="true"]').first();
  expect(await textContrast(checked)).toBeGreaterThanOrEqual(4.5);
});

// The focus ring is the only outline, so a checked pill cannot pass for a
// focused one.
test('the checked pill wears no ring while it lacks focus', async ({
  page,
  request,
}) => {
  const form = await openForm(page, request);
  const checked = form.locator('[role="radio"][aria-checked="true"]').first();
  await expect(checked).not.toBeFocused();
  const look = await lookOf(checked);
  expect(look.outlineStyle).toBe('none');
  expect(look.boxShadow).toBe('none');
});

test('a pill focused from the keyboard shows a focus ring', async ({
  page,
  request,
}) => {
  const form = await openForm(page, request);

  // The provider pills come before the name.
  await page.keyboard.press('Shift+Tab');
  const focused = form.locator('[role="radio"]:focus-visible');
  await expect(focused).toHaveCount(1);
  expect((await lookOf(focused)).outlineStyle).toBe('solid');
});

test('Home and End choose the first and last pill', async ({
  page,
  request,
}) => {
  const form = await openForm(page, request);
  await form.getByRole('radio', { name: 'Default' }).click();
  const group = form
    .getByRole('radiogroup')
    .filter({ has: page.getByRole('radio', { name: 'in-place' }) });
  const pills = group.getByRole('radio');
  await group.getByRole('radio', { name: 'in-place' }).focus();

  await page.keyboard.press('End');
  await expect(pills.last()).toBeFocused();
  await expect(pills.last()).toHaveAttribute('aria-checked', 'true');

  await page.keyboard.press('Home');
  await expect(pills.first()).toBeFocused();
  await expect(pills.first()).toHaveAttribute('aria-checked', 'true');
});

test('a checkbox field looks disabled while the form is', async ({
  page,
  request,
}) => {
  const form = await openForm(page, request);
  await form.getByRole('radio', { name: 'Fake Provider' }).click();
  const label = form
    .locator('label')
    .filter({ has: page.getByRole('checkbox', { name: 'Copy .env' }) });
  const opacity = () =>
    label.evaluate(
      (element) =>
        (
          globalThis as unknown as {
            getComputedStyle: (e: unknown) => { opacity: string };
          }
        ).getComputedStyle(element).opacity,
    );
  expect(await opacity()).toBe('1');

  // The create never answers, so the form stays inert while it is out.
  await page.route('**/api/projects/*/workstreams', async (route) => {
    if (route.request().method() !== 'POST') await route.continue();
  });
  await form.getByRole('button', { name: 'Create' }).click();
  await expect(form.getByRole('button', { name: 'Create' })).toHaveAttribute(
    'aria-disabled',
    'true',
  );
  expect(Number(await opacity())).toBeLessThan(1);
});
