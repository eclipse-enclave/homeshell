import { mkdtempSync, readdirSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { afterAll, afterEach, describe, expect, it, vi } from 'vitest';

/**
 * The socket-path budget of isolated daemon homes: a bridge socket sits two
 * levels below the home, and a unix socket path has at most 107 bytes.
 */

const scratch = mkdtempSync(join(tmpdir(), 'hs-iso-'));

afterEach(() => {
  vi.unstubAllEnvs();
  vi.resetModules();
});

afterAll(() => rmSync(scratch, { recursive: true, force: true }));

async function load(env: Record<string, string>) {
  for (const [name, value] of Object.entries(env)) vi.stubEnv(name, value);
  vi.resetModules();
  return {
    env: await import('./e2e-env'),
    daemon: await import('./isolated-daemon'),
  };
}

describe('worstSocketPath', () => {
  it('is the runtime dir, the namespace dir and a local session socket', async () => {
    const { daemon } = await load({});
    const path = daemon.worstSocketPath('/r');
    expect(path).toMatch(/^\/r\/homeshell-[0-9a-f]{12}\/hs-[0-9a-f-]{36}\.sock$/);
  });
});

describe('checkSocketBudget', () => {
  it.each([
    [daemonDirOf(107 - 68), true],
    [daemonDirOf(108 - 68), false],
  ])('a runtime dir of %#: fits = %s', async (dir, fits) => {
    const { daemon } = await load({});
    const check = () => daemon.checkSocketBudget(dir);
    if (fits) expect(check).not.toThrow();
    else expect(check).toThrow(/108 bytes.*107.*HOMESHELL_E2E_ROOT/s);
  });
});

describe('isolatedHome', () => {
  it('leaves room for a TMPDIR as long as /var/tmp with the default root', async () => {
    const { env, daemon } = await load({ TMPDIR: '/var/tmp', HOMESHELL_E2E_ROOT: '' });
    expect(env.e2eRoot).toMatch(/^\/var\/tmp\/hs-e2e-[0-9a-f]{6}$/);
    // The longest home isolatedHome makes: a 4-char prefix and mkdtemp's 6 chars.
    const home = join(env.scratchRoot, 'abcd-XXXXXX');
    expect(() => daemon.checkSocketBudget(home)).not.toThrow();
  });

  it('makes a home that is its own runtime dir', async () => {
    const { daemon } = await load({ HOMESHELL_E2E_ROOT: join(scratch, 'ok') });
    const home = daemon.isolatedHome('abcd');
    expect(daemon.runtimeEnv(home)).toEqual({ XDG_RUNTIME_DIR: home });
  });

  it('refuses a prefix longer than 4 chars', async () => {
    const { daemon } = await load({ HOMESHELL_E2E_ROOT: join(scratch, 'long-prefix') });
    expect(() => daemon.isolatedHome('abcde')).toThrow(/at most 4/);
  });

  it('fails fast and leaves nothing behind when the sockets would not fit', async () => {
    const root = join(scratch, 'x'.repeat(60));
    const { env, daemon } = await load({ HOMESHELL_E2E_ROOT: root });
    expect(() => daemon.isolatedHome('abcd')).toThrow(/bytes.*107/s);
    expect(readdirSync(env.scratchRoot)).toEqual([]);
  });
});

/** An absolute dir path of exactly `length` bytes. */
function daemonDirOf(length: number): string {
  return `/${'d'.repeat(length - 1)}`;
}
