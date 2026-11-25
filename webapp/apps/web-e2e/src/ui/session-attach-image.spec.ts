import { basename } from 'node:path';
import {
  type APIRequestContext,
  type Locator,
  type Page,
} from '@playwright/test';
import { expect, tempProjectDir, test } from '../fixtures';
import { createSession, createWorkstream } from '../api/workstream-helpers';
import { addEmptyProject } from '../projects';
import { selectProject, sessionRows } from './workstream-helpers';

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

/** A 1×1 PNG: the clipboard decodes what it is given, so it must be a real one. */
const PIXEL_PNG =
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==';

/** The browser APIs the page-side helpers below use; this project has no DOM lib. */
type PageGlobals = {
  navigator: { clipboard: { write(items: unknown[]): Promise<void> } };
  ClipboardItem: new (items: Record<string, Blob>) => unknown;
  DataTransfer: new () => { items: { add(file: File): void } };
};

/** Puts a PNG, and `text` as plain text when given, on the page's clipboard. */
async function copyImage(page: Page, text?: string) {
  await page.evaluate(
    async ({ base64, text }) => {
      const bytes = Uint8Array.from(atob(base64), (c) => c.charCodeAt(0));
      const items: Record<string, Blob> = {
        'image/png': new Blob([bytes], { type: 'image/png' }),
      };
      if (text) items['text/plain'] = new Blob([text], { type: 'text/plain' });
      const { navigator, ClipboardItem } = globalThis as unknown as PageGlobals;
      await navigator.clipboard.write([new ClipboardItem(items)]);
    },
    { base64: PIXEL_PNG, text },
  );
}

/** Drags `files` over the row and drops them there, as a file manager would. */
async function dropFiles(page: Page, row: Locator, files: Picked[]) {
  const dataTransfer = await page.evaluateHandle(
    (files) => {
      const { DataTransfer } = globalThis as unknown as PageGlobals;
      const transfer = new DataTransfer();
      for (const { name, mimeType, bytes } of files) {
        transfer.items.add(
          new File([new Uint8Array(bytes)], name, { type: mimeType }),
        );
      }
      return transfer;
    },
    files.map(({ name, mimeType, buffer }) => ({
      name,
      mimeType,
      bytes: [...buffer],
    })),
  );
  await row.dispatchEvent('dragenter', { dataTransfer });
  await row.dispatchEvent('dragover', { dataTransfer });
  return {
    drop: () => row.dispatchEvent('drop', { dataTransfer }),
    leave: () => row.dispatchEvent('dragleave', { dataTransfer }),
  };
}

test('pasting an image with no text attaches it', async ({
  page,
  context,
  request,
}) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write']);
  const card = await demoSessionRow(request, page);
  const box = card.getByRole('textbox', { name: 'Send text to session' });

  await copyImage(page);
  await box.focus();
  await page.keyboard.press('ControlOrMeta+V');

  await expect(card.getByRole('status')).toHaveText(/^Image attached: /);
  await expect(card.getByTestId('session-preview-full')).toContainText(
    /\[image: \/mnt\/host-images\/\S+\.png\]/,
  );
  await expect(box).toHaveValue('');
});

test('pasting text that comes with an image stays a text paste', async ({
  page,
  context,
  request,
}) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write']);
  const card = await demoSessionRow(request, page);
  const box = card.getByRole('textbox', { name: 'Send text to session' });

  await copyImage(page, 'copied from a page');
  await box.focus();
  await page.keyboard.press('ControlOrMeta+V');

  await expect(box).toHaveValue('copied from a page');
  await expect(card.getByText(/Image attached/)).toHaveCount(0);
  await expect(card.getByText(/\[image:/)).toHaveCount(0);
});

test('dropping images on the row highlights it, then attaches them in order', async ({
  page,
  request,
}) => {
  const card = await demoSessionRow(request, page);
  const row = sessionRows(card).first();

  const drag = await dropFiles(page, row, [
    png('first.png'),
    jpeg('second.jpg'),
  ]);
  await expect(row).toHaveAttribute('data-drop', 'ready');
  await drag.drop();

  await expect(row).not.toHaveAttribute('data-drop');
  await expect(card.getByRole('status')).toHaveText(
    'Image attached: second.jpg',
  );
  const output = card.getByTestId('session-preview-full');
  await expect(output).toContainText(/\.jpg\]/);
  const text = (await output.textContent()) ?? '';
  expect(text.indexOf('.png]')).toBeGreaterThanOrEqual(0);
  expect(text.indexOf('.png]')).toBeLessThan(text.indexOf('.jpg]'));
});

test('a stopped session refuses dragged images, saying why, like its disabled button', async ({
  page,
  request,
}) => {
  const card = await demoSessionRow(request, page);
  const row = sessionRows(card).first();
  await row.getByRole('button', { name: 'Stop', exact: true }).click();
  await expect(card.getByText('Ended · 1')).toBeVisible({ timeout: 15_000 });

  const drag = await dropFiles(page, row, [png('late.png')]);
  await expect(row).toHaveAttribute('data-drop', 'refused');
  await expect(card.getByRole('status')).toHaveText(
    'Images can be attached while the session is idle or running',
  );
  // A refused drag never drops in a real browser: the user lets go and it leaves.
  await drag.leave();

  await expect(row).not.toHaveAttribute('data-drop');
  await expect(card.getByText(/Image attached/)).toHaveCount(0);
  await expect(card.getByText(/\[image:/)).toHaveCount(0);
});
