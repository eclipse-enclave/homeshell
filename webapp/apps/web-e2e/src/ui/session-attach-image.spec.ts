import { basename } from 'node:path';
import {
  type APIRequestContext,
  type Locator,
  type Page,
} from '@playwright/test';
import { expect, tempProjectDir, test } from '../fixtures';
import { createSession, createWorkstream } from '../api/workstream-helpers';
import { addEmptyProject } from '../projects';
import { selectProject } from './workstream-helpers';

/**
 * The Attach image button in a session row, on the demo provider: its sessions
 * declare attachImage, and an attached image's made-up path is echoed into the
 * session's output as `[image: <path>]`, which is what the agent would receive.
 */

const PNG = Buffer.from([
  0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 1, 2, 3,
]);
const JPEG = Buffer.from([0xff, 0xd8, 0xff, 0xe0, 1, 2, 3]);

type Picked = { name: string; mimeType: string; buffer: Buffer };
const png = (name: string): Picked => ({
  name,
  mimeType: 'image/png',
  buffer: PNG,
});
const jpeg = (name: string): Picked => ({
  name,
  mimeType: 'image/jpeg',
  buffer: JPEG,
});

/** A demo session's row, expanded, in a fresh project shown in the page. */
async function demoSessionRow(request: APIRequestContext, page: Page) {
  const dir = await tempProjectDir('hs-ui-attach-');
  const projectId = await addEmptyProject(request, dir);
  const workstreamId = await createWorkstream(request, projectId, 'ui-attach');
  await createSession(request, projectId, workstreamId);

  await page.goto('/');
  await selectProject(page, basename(dir));
  const card = page.getByRole('region', { name: 'Workstream ui-attach' });
  await card.getByRole('button', { name: 'Toggle session details' }).click();
  return card;
}

/** Picks `files` through the row's Attach image button. */
async function attach(page: Page, row: Locator, files: Picked[]) {
  const chooser = page.waitForEvent('filechooser');
  await row.getByRole('button', { name: 'Attach image' }).click();
  await (await chooser).setFiles(files);
}

test('attaching an image hands its path to the agent and names it in the row', async ({
  page,
  request,
}) => {
  const card = await demoSessionRow(request, page);

  await attach(page, card, [png('screenshot.png')]);

  await expect(card.getByRole('status')).toHaveText(
    'Image attached: screenshot.png',
  );
  await expect(card.getByTestId('session-preview-full')).toContainText(
    /\[image: \/mnt\/host-images\/\S+\.png\]/,
  );
});

test('several images arrive one after another, in the order picked', async ({
  page,
  request,
}) => {
  const card = await demoSessionRow(request, page);

  await attach(page, card, [png('first.png'), jpeg('second.jpg')]);

  await expect(card.getByRole('status')).toHaveText(
    'Image attached: second.jpg',
  );
  const output = card.getByTestId('session-preview-full');
  await expect(output).toContainText(/\.jpg\]/);
  const text = (await output.textContent()) ?? '';
  expect(text.indexOf('.png]')).toBeGreaterThanOrEqual(0);
  expect(text.indexOf('.png]')).toBeLessThan(text.indexOf('.jpg]'));
});

test('a file that is not a PNG or JPEG is refused in the page error line', async ({
  page,
  request,
}) => {
  const card = await demoSessionRow(request, page);

  await attach(page, card, [
    { name: 'anim.gif', mimeType: 'image/gif', buffer: Buffer.from('GIF89a') },
  ]);

  await expect(page.getByRole('alert')).toHaveText(
    'Only PNG or JPEG images can be attached',
  );
  await expect(card.getByText(/\[image:/)).toHaveCount(0);
  await expect(card.getByText(/Image attached/)).toHaveCount(0);
});
