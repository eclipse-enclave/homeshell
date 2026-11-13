// Seeds the demo daemon before recording: settings, the projects and worktrees of layout.json,
// and real Claude Code sessions, some finished or ended, some still at work when it returns.
//
// Usage: node scripts/demo-video/seed.mjs <base-url> <projects-dir>
// Env: DEMO_TERMINAL, the gnome-terminal line Open runs (see run.sh).
import { join } from 'node:path';
import {
  addWorktree,
  allSessions,
  api,
  layout,
  project,
  rename,
  sessionAction,
  setBase,
  startClaude,
  waitForSession,
} from './api.mjs';

const [base, projectsDir] = process.argv.slice(2);
setBase(base);

async function settings() {
  const s = await api('GET', '/settings');
  s.notifications = { ...s.notifications, style: 'in-app', sound: 'none' };
  s.workstreams = { ...s.workstreams, location: projectsDir };
  s.terminal = { emulator: 'custom', command: process.env.DEMO_TERMINAL };
  await api('PUT', '/settings', s);
}

/** Starts a session titled `title` and returns it once it runs. */
async function started(projectName, wsName, title, prompt) {
  await startClaude(projectName, wsName, prompt);
  const s = await waitForSession(projectName, wsName, ['running'], { timeoutMs: 60_000 });
  await rename(s, title);
  return s;
}

/** Runs `prompt` to the end in a fresh session; with `end`, stops it so it is listed as ended. */
async function finished(projectName, wsName, title, prompt, { end = false } = {}) {
  const { id } = await started(projectName, wsName, title, prompt);
  const s = await waitForSession(projectName, wsName, ['idle', 'done'], { pick: (x) => x.id === id, timeoutMs: 600_000 });
  if (end) {
    await sessionAction(s, 'stop');
    await waitForSession(projectName, wsName, ['stopped', 'done'], { pick: (x) => x.id === s.id, timeoutMs: 60_000 });
  }
  console.log(`seeded ${projectName}/${wsName}: ${s.title}`);
}

async function main() {
  await settings();
  for (const name of Object.keys(layout.projects))
    if (!(await project(name))) await api('POST', '/projects', { path: join(projectsDir, name) });
  for (const [name, p] of Object.entries(layout.projects)) for (const w of p.worktrees) await addWorktree(name, w);

  // Past work, in parallel: conversations that finished, some of them ended.
  await Promise.all([
    finished(
      'acme-webshop',
      'gift-cards',
      'Plan gift cards',
      'Let us plan gift cards for the checkout, without changing files yet. Read src/checkout/cart.js and ' +
        'propose in five short bullets how a gift card should reduce the cart total, and what to test.',
      { end: true },
    ),
    finished(
      'acme-webshop',
      'tax-rounding',
      'Test tax rounding',
      'Check how src/checkout/tax.js rounds tax and add a test for 3 mugs at 0.35 euro each. Run npm test.',
    ),
    finished(
      'mobile-app',
      'main',
      'Document the layout',
      'Read the code and write a short "Project layout" section into README.md.',
    ),
    finished(
      'developer-docs',
      'main',
      'Changelog for v1.5',
      'Add a v1.5 entry to CHANGELOG.md: refunds accept an Idempotency-Key header. One line.',
      { end: true },
    ),
  ]);

  // At work when the recording starts.
  await started(
    'payments-api',
    'idempotency',
    'Idempotent refunds',
    'Make POST /refunds idempotent: honour an Idempotency-Key header so a retried request returns the first ' +
      'refund instead of booking a second one. Keep keys in memory for 24 hours. Add table-driven tests in ' +
      'internal/refunds and run go test ./... until it passes.',
  );
  await started(
    'mobile-app',
    'dark-mode',
    'Dark mode',
    'Add dark mode: give src/theme/tokens.js a dark palette next to the light one, chosen by the system ' +
      'colour scheme, and make SettingsScreen use tokens instead of its hard-coded colours. Explain each ' +
      'change briefly as you go.',
  );
  for (const s of await allSessions()) console.log(`${s.project}/${s.ws}: ${s.state} ${s.title}`);
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
