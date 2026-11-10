import { createHash } from 'node:crypto';
import { mkdirSync, mkdtempSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';

/**
 * Where this checkout's e2e run keeps its servers and files. Everything is
 * derived from a hash of the checkout path, so two checkouts (worktrees) can
 * run the suite at once without sharing a port, a lock or a binary. Each value
 * can be overridden through the env var next to it.
 *
 * Pure apart from reading env: playwright.config.ts imports it at top level,
 * which Playwright evaluates again in every worker.
 */

export const repoRoot = resolve(__dirname, '../../../..');
export const daemonDir = join(repoRoot, 'daemon');

const hash = createHash('sha256').update(repoRoot).digest();

/**
 * The per-checkout temp root. Kept short: bridge sockets of isolated daemons
 * live a few levels below it and a unix socket path has ~107 bytes.
 */
export const e2eRoot =
  process.env.HOMESHELL_E2E_ROOT ||
  join(tmpdir(), `hs-e2e-${hash.toString('hex').slice(0, 8)}`);

/** The one daemon build every server and helper of a run uses. */
export const homeshellBin =
  process.env.HOMESHELL_E2E_BIN || join(e2eRoot, 'bin', 'homeshell');

/**
 * Ports 10000-29999, a daemon/web pair per checkout. Below Linux's ephemeral
 * range (32768+), where the isolated daemons' `127.0.0.1:0` binds land.
 */
const portBase = 10_000 + (hash.readUInt32BE(0) % 10_000) * 2;
export const daemonUrl =
  process.env.HOMESHELL_DAEMON_URL || `http://127.0.0.1:${portBase}`;
export const webUrl =
  process.env.HOMESHELL_WEB_URL || `http://127.0.0.1:${portBase + 1}`;

/** The cross-worker settings lock (settings-lock.ts). */
export const settingsLockDir = join(e2eRoot, 'settings.lock');

/** Isolated daemon homes, fixtures and other per-spec scratch dirs. */
export const scratchRoot = join(e2eRoot, 'h');

/**
 * Bridge sockets of the shared daemon: its XDG_RUNTIME_DIR, so its bridges
 * stay inside the root where globalTeardown finds them.
 */
export const sharedRuntimeDir = join(e2eRoot, 'run');

/**
 * A fresh scratch dir under the run's root, for an isolated daemon's home or a
 * spec's fixture files. Keep `prefix` short (see `e2eRoot`).
 */
export function scratchDir(prefix: string): string {
  mkdirSync(scratchRoot, { recursive: true });
  return mkdtempSync(join(scratchRoot, prefix));
}
