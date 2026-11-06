import { basename } from 'node:path';
import { type Locator } from '@playwright/test';
import { expect, test } from '../fixtures';
import { loadApp } from './keyboard-helpers';
import {
  openWorkstreamCard,
  selectProject,
  unique,
} from './workstream-helpers';
import { awaitRealWorkstream, createProject } from '../api/workstream-helpers';
import { gitProject } from '../git-repo';

async function box(locator: Locator) {
  const bounds = await locator.boundingBox();
  if (!bounds) throw new Error('element has no box');
  return bounds;
}

type Box = Awaited<ReturnType<typeof box>>;

function overlaps(a: Box, b: Box) {
  return (
    a.x < b.x + b.width &&
    b.x < a.x + a.width &&
    a.y < b.y + b.height &&
    b.y < a.y + a.height
  );
}

/** Asserts each action is whole on screen, and inside the card. */
async function expectWhole(card: Locator, actions: Locator[]) {
  const cardBox = await box(card);
  for (const action of actions) {
    await expect(action).toBeInViewport({ ratio: 1 });
    const actionBox = await box(action);
    expect(actionBox.x + actionBox.width).toBeLessThanOrEqual(
      cardBox.x + cardBox.width,
    );
  }
}

/**
 * A narrow window takes its width out of the workstream's name and pills, never
 * out of its actions: every one stays whole on screen, wrapping to a line of
 * their own and then onto more.
 */
test.use({ viewport: { width: 900, height: 800 } });

/** A window narrower than the card's actions on one line. */
const NARROW = { width: 480, height: 800 };

test('a narrow window keeps every workstream action in view', async ({
  page,
  request,
}) => {
  const { dir, projectId } = await gitProject(request, 'hs-ui-card-width-');
  const name = unique(`a workstream name ${'long enough to crowd '.repeat(3)}`);
  const created = await request.post(`/api/projects/${projectId}/workstreams`, {
    data: {
      providerId: 'core',
      name,
      config: {
        strategy: 'worktree',
        branch: unique('topic/a-branch-name-long-enough-to-crowd'),
      },
    },
  });
  expect(created.status()).toBe(201);
  await awaitRealWorkstream(request, projectId, 15_000);

  await loadApp(page);
  await selectProject(page, basename(dir));
  const card = page.getByRole('region', { name: `Workstream ${name}` });
  await expect(card.getByRole('heading', { name })).toBeVisible();

  const actions = [
    card.getByRole('button', { name: `Open workstream ${name}`, exact: true }),
    card.getByRole('button', { name: `Open workstream ${name} with…` }),
    card.getByRole('button', { name: 'New session' }),
    card.getByRole('button', { name: `Notes for workstream ${name}` }),
    card.getByRole('button', { name: `Delete workstream ${name}` }),
  ];
  await expectWhole(card, actions);
  await page.setViewportSize(NARROW);
  await expectWhole(card, actions);
});

test('a narrow window keeps every session action in view', async ({
  page,
  request,
}) => {
  await page.setViewportSize(NARROW);
  const { card, row } = await openWorkstreamCard(
    page,
    request,
    'hs-ui-row-width',
    'row-width',
  );
  const actions = await row.getByRole('button').all();
  expect(actions.length).toBeGreaterThan(1);
  await expectWhole(card, actions);
  // Wrapped actions make room for the row's name and pills instead of being
  // painted over them.
  // The session's title and its agent pill both read "Claude Code".
  const labels = [
    ...(await row.getByText('Claude Code', { exact: true }).all()),
    row.getByTestId('session-state'),
  ];
  expect(labels).toHaveLength(3);
  const others = await row
    .getByRole('button')
    .filter({ hasNot: page.getByTestId('session-title') })
    .all();
  for (const label of labels) {
    const labelBox = await box(label);
    for (const action of others) {
      expect(overlaps(labelBox, await box(action))).toBe(false);
    }
  }
});

// The name is no wider than its text, so a short one leaves no gap before its
// pills.
test('a short workstream name sits right against its pills', async ({
  page,
  request,
}) => {
  const projectPrefix = `${unique('hs-ui-card-short')}-`;
  const projectId = await createProject(request, projectPrefix);
  const created = await request.post(`/api/projects/${projectId}/workstreams`, {
    data: { providerId: 'fake', name: 'ab', config: { branch: 'main' } },
  });
  expect(created.status()).toBe(201);

  await loadApp(page);
  await selectProject(page, projectPrefix);
  const card = page.getByRole('region', { name: 'Workstream ab' });
  const [headingBox, pillBox] = await Promise.all([
    box(card.getByRole('heading', { name: 'ab' })),
    box(card.getByText('Fake Provider', { exact: true })),
  ]);
  // No more than the header's gap between them.
  expect(pillBox.x - (headingBox.x + headingBox.width)).toBeLessThanOrEqual(9);
});

test('the project name sits against its git pill', async ({
  page,
  request,
}) => {
  const { dir } = await gitProject(request, 'hs-ui-header-pill-');
  await loadApp(page);
  const project = basename(dir);
  await selectProject(page, project);
  const heading = page.getByRole('heading', { level: 1, name: project });
  const [headingBox, gitBox] = await Promise.all([
    box(heading),
    box(page.getByRole('main').getByText('git', { exact: true })),
  ]);
  // No more than the title row's gap between them.
  expect(gitBox.x - (headingBox.x + headingBox.width)).toBeLessThanOrEqual(10);
});
