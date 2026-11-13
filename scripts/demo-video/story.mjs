// The README demo's story, played on the virtual desktop against a seeded daemon (seed.mjs):
// a new worktree with an Enclave session the user briefs in a terminal, another agent's question
// answered, the finished feature reviewed in its folder, and an old conversation continued.
// Waiting on the agents is marked "fast" in the timeline, which encode.sh speeds up.
//
// Usage: node scripts/demo-video/story.mjs <base-url> <out-dir>
// Writes <out-dir>/raw.mkv and <out-dir>/timeline.json.
import { mkdirSync } from 'node:fs';
import { join } from 'node:path';
import { allSessions, layout, rename, setBase, startClaude, waitForSession } from './api.mjs';
import * as desk from './desktop.mjs';

const [base, outDir] = process.argv.slice(2);
setBase(base);
const { pause } = desk;
const SHOP = 'acme-webshop';
const FEATURE = layout.projects[SHOP].onCamera;

const FEATURE_SPEC =
  'Add coupon codes to the checkout. SAVE10 takes 10% off the subtotal, FREESHIP waives shipping, and ' +
  'any other code is rejected with an error. Put the rules in src/checkout/coupons.js, apply them in ' +
  'cartTotals, add node:test tests and run npm test.';
const QUESTION_PROMPT =
  'Webhook deliveries in internal/webhooks/retry.go are retried at once in a tight loop. Read it, then ' +
  'use AskUserQuestion to ask me one question: retry with exponential backoff in process, or through a ' +
  'persistent retry queue. Change nothing before I answer; then implement my choice with a test.';
const FOLLOW_UP = 'Sounds good, let us build it. Implement the plan with tests.';

let page;
let capture;

/** Clicks `locator` once no notification toast covers it (they go after 6 s); the wait is cut. */
async function click(locator) {
  await locator.waitFor({ state: 'visible' });
  await locator.scrollIntoViewIfNeeded();
  let waited = false;
  for (let i = 0; i < 40; i++) {
    const box = await locator.boundingBox();
    const toasts = await page.getByRole('alert').evaluateAll((els) => els.map((e) => e.getBoundingClientRect().toJSON()));
    const covered = box && toasts.some((t) => box.x < t.right && box.x + box.width > t.left && box.y < t.bottom && box.y + box.height > t.top);
    if (!covered) break;
    if (!waited) capture?.mark('cut', 'toast');
    waited = true;
    await pause(250);
  }
  if (waited) capture?.mark('normal');
  await desk.click(page, locator);
}

const sidebar = (name) => page.getByRole('button', { name: new RegExp(`\\b${name}\\b.*workstreams?`) });
const card = (name) => page.getByRole('region', { name: `Workstream ${name}` });
const row = (wsName, title) => card(wsName).getByRole('listitem').filter({ hasText: title });

/** Waits on an agent with the recording sped up; `what` resolves when the wait is over. */
const fastForward = (label, what) => waitMarked('fast', label, what);
/** Waits with the recording cut, for waits a viewer would not usually sit through. */
const skip = (label, what) => waitMarked('cut', label, what);

async function waitMarked(mode, label, what) {
  capture.mark(mode, label);
  try {
    return await what;
  } finally {
    capture.mark('normal');
  }
}

/** Clicks `button`, waits for the terminal window it opens and returns that window. */
async function openTerminal(button) {
  const before = desk.windowIds();
  await click(button);
  const win = await desk.waitNewWindow(before);
  await pause(1200);
  return win;
}

async function newWorktree() {
  await click(page.getByRole('button', { name: 'New workstream' }));
  const name = page.getByRole('textbox', { name: 'Name' });
  await click(name);
  desk.key('ctrl+a');
  await desk.typeText(FEATURE, 60);
  await pause(1600); // the branch and folder follow the name
  const create = page.getByRole('button', { name: 'Create' });
  await click(create);
  // A click while the name's draft re-renders the form can be lost; a person would click again.
  if (!(await card(FEATURE).waitFor({ timeout: 5000 }).then(() => true, () => false))) await click(create);
  await card(FEATURE).waitFor();
  await pause(800);
}

async function newEnclaveSession() {
  const ws = card(FEATURE);
  await click(ws.getByRole('button', { name: 'New session' }));
  const mode = ws.getByRole('combobox', { name: 'Permission mode' });
  await click(mode);
  await pause(400);
  // The native list is a popup; type-ahead picks the entry like a person would.
  await desk.typeText('bypass', 70);
  desk.key('Return');
  await pause(600);
  await click(ws.getByRole('button', { name: /^Container/ }));
  await pause(500);
  await click(ws.getByRole('button', { name: /more$/ }));
  await pause(400);
  await click(ws.getByRole('checkbox', { name: /Playwright/ }));
  await pause(900);
  await click(ws.getByRole('button', { name: 'Start session' }));
}

async function briefInTerminal() {
  // The container's start is cut once the session shows "Starting": usually quick, it can rebuild
  // the image for minutes. A fresh Enclave session reads running, then idle at its empty prompt;
  // HomeShell announces that as "finished", so the toast passes in the cut too.
  await pause(600);
  await skip(
    'container starts',
    (async () => {
      await waitForSession(SHOP, FEATURE, ['idle'], { timeoutMs: 600_000 });
      await pause(1500);
      await page.getByRole('alert').first().waitFor({ state: 'detached', timeout: 10_000 }).catch(() => {});
    })(),
  );
  await pause(800);
  const win = await openTerminal(row(FEATURE, 'Claude Code').getByRole('button', { name: 'Open' }));
  await desk.typeText(FEATURE_SPEC, 28);
  await pause(400);
  desk.key('Return');
  // A brief that never reached the agent would leave nothing to review: it must be at work.
  await waitForSession(SHOP, FEATURE, ['running'], { timeoutMs: 30_000 });
  await pause(4000);
  await desk.closeWindow(win);
  await pause(1000);
}

async function answerQuestion() {
  await fastForward(
    'question',
    waitForSession('payments-api', 'refund-webhooks', ['waiting'], { timeoutMs: 600_000 }),
  );
  await pause(1800); // the toast, and the project's mark turning orange
  await click(sidebar('payments-api'));
  await pause(900);
  const r = row('refund-webhooks', 'Webhook retries');
  await click(r.getByRole('button', { name: 'Toggle session details' }));
  await pause(3000); // read the question in the preview
  const win = await openTerminal(r.getByRole('button', { name: 'Open' }));
  await pause(1500);
  desk.key('Down');
  await pause(700);
  desk.key('Return');
  await pause(3500);
  await desk.closeWindow(win);
  await pause(1000);
}

async function review() {
  await fastForward('feature', waitForSession(SHOP, FEATURE, ['idle'], { timeoutMs: 900_000 }));
  await pause(2200);
  await click(sidebar(SHOP));
  await pause(900);
  const ws = card(FEATURE);
  await click(row(FEATURE, 'Claude Code').getByRole('button', { name: 'Toggle session details' }));
  await pause(3500);
  const win = await openTerminal(ws.getByRole('button', { name: `Open workstream ${FEATURE}`, exact: true }));
  await desk.typeText('git status --short\n', 70);
  await pause(1800);
  await desk.typeText('head -24 src/checkout/coupons.js\n', 55);
  await pause(3500);
  await desk.closeWindow(win);
  await pause(1000);
}

async function continueOldConversation() {
  const r = row('gift-cards', 'Plan gift cards');
  await click(r.getByRole('button', { name: 'Continue' }));
  await fastForward('continue', waitForSession(SHOP, 'gift-cards', ['idle', 'running'], { timeoutMs: 120_000 }));
  await pause(1200);
  const win = await openTerminal(row('gift-cards', 'Plan gift cards').getByRole('button', { name: 'Open' }));
  await pause(2500); // the old plan is still there
  await desk.typeText(FOLLOW_UP, 32);
  desk.key('Return');
  await pause(3500);
  await desk.closeWindow(win);
  await pause(3000);
}

async function main() {
  mkdirSync(outDir, { recursive: true });
  ({ page } = await desk.openBrowser(base, process.env.HOMESHELL_API_TOKEN ?? 'demo-token'));
  await click(sidebar(SHOP));
  await pause(1500);

  // Off camera: the agent that will ask a question in another project.
  await startClaude('payments-api', 'refund-webhooks', QUESTION_PROMPT);
  const asker = await waitForSession('payments-api', 'refund-webhooks', ['running'], { timeoutMs: 60_000 });
  await rename(asker, 'Webhook retries');

  capture = desk.startCapture(join(outDir, 'raw.mkv'));
  try {
    await pause(2500); // the overview

    await newWorktree();
    await newEnclaveSession();
    await briefInTerminal();
    await answerQuestion();
    await review();
    if (process.env.DEMO_SKIP_CONTINUE !== '1') await continueOldConversation();
  } finally {
    await capture.stop(join(outDir, 'timeline.json'));
  }
  for (const s of await allSessions()) console.log(`${s.project}/${s.ws}: ${s.state} ${s.title}`);
  await page.context().close();
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
