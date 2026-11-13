import { basename } from 'node:path';
import { type APIRequestContext, type Locator, type Page } from '@playwright/test';
import { expect, tempProjectDir, test } from '../fixtures';
import {
  openMoreActions,
  selectProject,
  sessionRows,
} from './workstream-helpers';
import { addEmptyProject } from '../projects';
import { createWorkstream } from '../api/workstream-helpers';

/**
 * What the user sees while a session starts: a row named after the agent coming
 * up, in the `starting` state, with the provider's startup output (the container
 * build log, for Enclave) in the row's own output box — expanded without being
 * asked, because that output is the reason the row is on screen.
 *
 * The window this needs exists because the e2e daemon runs the fake provider with
 * HOMESHELL_FAKE_SESSION_STARTUP_MS set (playwright.config.ts).
 */

/** A project with one workstream, created through the API. */
async function projectWithWorkstream(
  request: APIRequestContext,
): Promise<{ dirName: string; projectId: string; workstreamId: string }> {
  const dir = await tempProjectDir('hs-ui-startup-');
  const projectId = await addEmptyProject(request, dir);

  const workstreamId = await createWorkstream(request, projectId, 'ui-startup');
  return { dirName: basename(dir), projectId, workstreamId };
}

async function openCard(page: Page, dirName: string) {
  await page.goto('/');
  await selectProject(page, dirName);
  const card = page.getByRole('region', { name: 'Workstream ui-startup' });
  await expect(card).toBeVisible();
  return card;
}

test('a starting session shows its name and streams the startup log into its box', async ({
  page,
  request,
}) => {
  const { dirName, projectId, workstreamId } =
    await projectWithWorkstream(request);
  const card = await openCard(page, dirName);

  // Start the session with the page already open, so the row appears live over
  // the event stream rather than on a reload.
  await request.post(
    `/api/projects/${projectId}/workstreams/${workstreamId}/sessions`,
    { data: { providerId: 'fake', optionId: 'claude', config: {} } },
  );

  // The agent coming up is named, in the starting state. The row's own title is
  // the specific locator here: "Claude Code" alone also matches the agent
  // and the workstream's "Starting Claude Code…" line.
  await expect(card.getByTestId('session-state')).toHaveText('Starting');
  await expect(card.getByText('Claude Code session')).toBeVisible();

  // Its output box is open already — no click — and carries the build output.
  const output = card.getByTestId('session-preview-full');
  await expect(output).toBeVisible();
  await expect(output).toContainText('#', { timeout: 10_000 });

  // Nothing to drive yet: no send box, and no Stop, which would be routed to a
  // provider that has never heard of this session. Remove is the exception — the
  // daemon handles its own placeholder row, and removing it cancels the build.
  await expect(card.getByLabel('Send text to session')).toHaveCount(0);
  await expect(card.getByRole('button', { name: 'Stop' })).toHaveCount(0);
  await openMoreActions(sessionRows(card));
  await expect(page.getByRole('menuitem', { name: 'Delete' })).toBeVisible();
  await page.keyboard.press('Escape');

  // And it becomes the real session in place, without a second row appearing.
  await expect(card.getByTestId('session-state')).toHaveText('Idle', {
    timeout: 15_000,
  });
  await expect(card.getByTestId('workstream-sessions')).toHaveAttribute(
    'aria-label',
    /^Sessions: 1 [a-z ]+$/,
  );
});

test('the starting row names the agent, and nothing repeats it on the workstream', async ({
  page,
  request,
}) => {
  const { dirName, projectId, workstreamId } =
    await projectWithWorkstream(request);
  const card = await openCard(page, dirName);

  await request.post(
    `/api/projects/${projectId}/workstreams/${workstreamId}/sessions`,
    { data: { providerId: 'fake', optionId: 'codex', config: {} } },
  );

  // The row says which agent is coming up…
  await expect(card.getByText('Codex session')).toBeVisible();
  await expect(card.getByTestId('session-state')).toHaveText('Starting');

  // …and the workstream adds no "Starting Codex…" line of its own, which would
  // say the same thing twice. The dismiss × only exists when there is a status
  // message, so its absence is the assertion.
  await expect(
    card.getByRole('button', { name: /Dismiss message for workstream/ }),
  ).toHaveCount(0);
});

/**
 * The output box shows ten lines and the rest is scrollback. This needs a real
 * browser: the assertions are about layout (how many lines fit, where the box is
 * scrolled to), which jsdom cannot answer — its scrollHeight is always 0.
 */
test('the startup log shows ten lines and scrolls back to the first', async ({
  page,
  request,
}) => {
  type Measured = {
    visibleLines: number;
    distanceFromBottom: number;
    scrollTopAfterScrollingUp: number;
    text: string;
  };
  // Either a finished measurement or why the box could not be measured yet, so a
  // real regression fails with numbers instead of a bare timeout.
  type Probe =
    | { ready: true; box: Measured }
    | { ready: false; reason: string; overflow: number; fromBottom: number };

  /**
   * Measures the box, or says why it is not ready.
   *
   * Two things make a naive read wrong, and both used to show up as flakes under
   * parallel workers while passing when run alone:
   *
   *  - the panel follows the tail from a React **effect**, which runs after the
   *    commit that made the box taller — so between a batch of lines landing in the
   *    DOM and that effect running, the box is legitimately not at the bottom yet
   *    (measured 101px, about one boxful, and 34px mid-scroll). Waiting for the
   *    effect is the fix; asserting into the gap is what produced the flake.
   *  - the log has to be taller than the box before there is anything to measure.
   */
  const probe = (box: Locator, options?: { timeout?: number }) =>
    box.evaluate(
      (el) => {
        // This callback runs in the browser but is type-checked against this
        // project's tsconfig, which has no DOM lib (`lib: ["es2022"]`,
        // `types: node`) — hence globalThis rather than the bare global.
        const style = (
          globalThis as unknown as {
            getComputedStyle: (e: unknown) => Record<string, string>;
          }
        ).getComputedStyle(el);
        const overflow = el.scrollHeight - el.clientHeight;
        const distanceFromBottom =
          el.scrollHeight - el.scrollTop - el.clientHeight;
        if (overflow <= 0) {
          return {
            ready: false,
            reason: 'the log is not yet taller than its box',
            overflow,
            fromBottom: distanceFromBottom,
          };
        }
        if (distanceFromBottom > 4) {
          return {
            ready: false,
            reason:
              'the follow-the-tail effect has not caught up with the newest lines',
            overflow,
            fromBottom: distanceFromBottom,
          };
        }
        const inner =
          el.clientHeight -
          parseFloat(style.paddingTop) -
          parseFloat(style.paddingBottom);
        // Prove the top is reachable in the same trip, then leave it there.
        el.scrollTop = 0;
        return {
          ready: true,
          box: {
            visibleLines: Math.round(inner / parseFloat(style.lineHeight)),
            distanceFromBottom,
            scrollTopAfterScrollingUp: el.scrollTop,
            text: el.textContent ?? '',
          },
        };
      },
      undefined,
      options,
    );

  /**
   * Starts one session and watches its startup log, returning a measurement or the
   * reason this attempt came up empty.
   *
   * The window closes on its own: the fake provider streams 26 lines over
   * HOMESHELL_FAKE_SESSION_STARTUP_MS (1.2s), the box only overflows once ~11 of
   * them have arrived, and when the stream ends the placeholder — with its box — is
   * replaced by the real session, whose preview is a terminal capture rather than a
   * build log. That leaves roughly 700ms to measure in. A loaded box can miss it
   * altogether, and missing it used to cost the **whole 30s test timeout**, because
   * the next evaluate() sat waiting on an element that had already gone. So each
   * probe is bounded, a vanished box ends the attempt at once, and the caller
   * simply tries again on a fresh session.
   */
  const watchOneStartup = async (): Promise<Probe> => {
    const { dirName, projectId, workstreamId } =
      await projectWithWorkstream(request);
    const card = await openCard(page, dirName);
    await request.post(
      `/api/projects/${projectId}/workstreams/${workstreamId}/sessions`,
      { data: { providerId: 'fake', optionId: 'claude', config: {} } },
    );

    const output = card.getByTestId('session-preview-full');
    let last: Probe = {
      ready: false,
      reason: 'the startup box never appeared',
      overflow: 0,
      fromBottom: 0,
    };
    // Polled tightly rather than every 80ms: the samples have to fall inside that
    // ~700ms window, and each one costs a round-trip.
    const deadline = Date.now() + 20_000;
    while (Date.now() < deadline) {
      try {
        last = (await probe(output, { timeout: 1_000 })) as Probe;
      } catch {
        // The box went away: the placeholder was promoted before the log ever
        // overflowed. Nothing further can be measured on this session.
        return {
          ready: false,
          reason:
            'the placeholder was promoted before the log overflowed its box',
          overflow: last.ready ? 0 : last.overflow,
          fromBottom: last.ready ? 0 : last.fromBottom,
        };
      }
      if (last.ready) return last;
      await page.waitForTimeout(25);
    }
    return last;
  };

  // Three attempts, because one missed window is not a regression — it is a busy
  // machine. A real break (the box never following the tail, or never overflowing)
  // fails all three and reports what it last saw.
  let measured: Probe | null = null;
  for (let attempt = 0; attempt < 3; attempt++) {
    measured = await watchOneStartup();
    if (measured.ready) break;
  }
  if (!measured?.ready) {
    throw new Error(
      `the startup log never settled at the tail in three attempts — ${measured?.reason} ` +
        `(overflow ${measured?.overflow}px, ${measured?.fromBottom}px from the bottom)`,
    );
  }
  const box = measured.box;

  // Exactly ten lines fit — measured against the box's own line-height with its
  // padding subtracted (clientHeight includes padding), so this checks the height
  // the stylesheet computes rather than restating the number.
  expect(box.visibleLines).toBe(10);
  // It was following the tail: the newest output is what you land on, not the
  // oldest ten lines with the live end somewhere below the fold. (A few pixels of
  // slack for fractional line heights.) The probe above is what enforces this — it
  // only reports ready at the tail — so this restates the property, keeping the
  // condition from being loosened there unnoticed.
  expect(box.distanceFromBottom).toBeLessThanOrEqual(4);
  // And the whole log is reachable upwards, first build line included.
  expect(box.scrollTopAfterScrollingUp).toBe(0);
  expect(box.text).toContain(
    '#1 [internal] load build definition from Dockerfile',
  );
});
