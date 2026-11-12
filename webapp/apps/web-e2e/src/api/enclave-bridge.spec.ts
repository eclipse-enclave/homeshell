import { expect, test } from '@playwright/test';
import { existsSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import {
  attachPid,
  attaches,
  buildEnclaveFixture,
  calls,
  createEnclaveSession,
  type EnclaveWorkstream,
  fixtureFiles,
  isolatedDaemon,
  observeSession,
  projectAndWorkstream,
  removeEnclaveFixture,
} from '../enclave-helpers';
import { disposeIsolatedDaemons } from '../isolated-daemon';
import { pollFor } from './workstream-helpers';

test.describe('Enclave host bridge', () => {
  test.skip(process.platform === 'win32', 'Host bridge requires Unix PTYs');
  test.beforeAll(buildEnclaveFixture);
  test.afterEach(disposeIsolatedDaemons);
  test.afterAll(removeEnclaveFixture);

  test('keeps the conversation through bridge/daemon loss and cleans up input, stop, start and exit', async () => {
    test.setTimeout(90_000);
    const daemon = await isolatedDaemon();
    let removeProject: (() => Promise<void>) | undefined;
    try {
      const ws = await projectAndWorkstream(daemon);
      removeProject = ws.remove;
      await createEnclaveSession(daemon.api, ws.base);
      const session = await pollFor('idle Enclave session', async () =>
        (await ws.sessions()).find(
          (s) => s.provider === 'enclave' && s.state === 'idle',
        ),
      );
      const path = `${ws.base}/${session.id}`;
      const current = () => ws.session(session.id);
      const countRuns = () => calls(daemon.fixture, 'run', 'continue').length;
      expect(countRuns()).toBe(1);

      // The terminal launcher is an external recorder; no real desktop window.
      if (process.platform === 'linux') {
        const opened = await daemon.api.post(`${path}/open`, {
          data: { mode: 'terminal' },
        });
        expect(opened.status()).toBe(200);
        const recorded = join(daemon.fixture, 'terminal.json');
        await expect.poll(() => existsSync(recorded)).toBe(true);
        const terminal = JSON.parse(readFileSync(recorded, 'utf8')) as string[];
        expect(terminal.join(' ')).toMatch(
          /['"]?bridge['"]?\s+['"]?attach['"]?\s+['"]?--sock/,
        );
      }

      const key = await daemon.api.post(`${path}/key`, {
        data: { key: 'up' },
      });
      expect(key.status()).toBe(204);
      await expect
        .poll(async () => (await current())?.preview)
        .toContain('key:up');

      const attachPids = () => attaches(daemon.fixture).map(Number);
      // Codex reports idle only through its title, which a reattached bridge never
      // repaints: confident idle proves the remembered title was used.
      const confidentlyIdle = async () => {
        const listed = await current();
        return (
          listed?.state === 'idle' && listed.observationData?.confident === true
        );
      };
      process.kill(attachPids()[0], 'SIGKILL');
      await expect.poll(() => attachPids().length).toBe(2);
      await expect.poll(confidentlyIdle).toBe(true);
      expect(countRuns()).toBe(1);

      await daemon.restart();
      // Restored sessions load as idle, so only live detection evidence proves a capture.
      await expect.poll(confidentlyIdle).toBe(true);
      expect(countRuns()).toBe(1);

      // Shutdown may reap a bridge; recovery may attach again, but must not
      // recreate the container or conversation (countRuns above).
      const prompt = await daemon.api.post(`${path}/prompt`, {
        data: { text: 'one conversation' },
      });
      expect(prompt.status()).toBe(204);
      await expect
        .poll(async () => (await current())?.preview)
        .toContain('received: one conversation');

      expect((await daemon.api.post(`${path}/stop`)).status()).toBe(204);
      await expect.poll(async () => (await current())?.state).toBe('stopped');
      expect(fixtureFiles(daemon.fixture, 'gateway-')).toHaveLength(0);

      expect((await daemon.api.post(`${path}/start`)).status()).toBe(204);
      await expect.poll(countRuns).toBe(2);
      expect(calls(daemon.fixture, 'continue')).toHaveLength(1);
      await expect.poll(async () => (await current())?.state).toBe('idle');

      const exit = await daemon.api.post(`${path}/prompt`, {
        data: { text: 'exit' },
      });
      expect(exit.status()).toBe(204);
      await expect
        .poll(async () => (await current())?.state, { timeout: 15_000 })
        .toBe('done');
      expect(
        fixtureFiles(daemon.fixture, 'container-', 'gateway-'),
      ).toHaveLength(0);

      // A session that ended on its own continues like a stopped one.
      expect((await daemon.api.post(`${path}/start`)).status()).toBe(204);
      await expect.poll(countRuns).toBe(3);
      expect(calls(daemon.fixture, 'continue')).toHaveLength(2);
      await expect.poll(async () => (await current())?.state).toBe('idle');

      expect((await daemon.api.delete(path)).status()).toBe(204);
      await expect.poll(current).toBeUndefined();
      const forbidden = calls(daemon.fixture).filter(
        (argv) =>
          argv[0] === 'status' ||
          argv[0] === 'exec' ||
          argv.includes('--session-monitor'),
      );
      expect(forbidden).toEqual([]);
    } finally {
      await removeProject?.();
      await daemon.stop();
    }
  });

  test('never polls container inventory between inventory checks', async () => {
    test.setTimeout(45_000);
    // Checks are a minute apart, so none falls in the window below.
    const daemon = await isolatedDaemon({ inventoryMs: 60_000 });
    let removeProject: (() => Promise<void>) | undefined;
    try {
      const ws = await projectAndWorkstream(daemon);
      removeProject = ws.remove;
      await createEnclaveSession(daemon.api, ws.base);
      const managed = await pollFor('owned session', async () =>
        (await ws.sessions()).find(
          (s) => s.provider === 'enclave' && s.state === 'idle',
        ),
      );

      const inventoryCalls = () =>
        calls(daemon.fixture, 'ps', 'status', 'exec');
      const before = inventoryCalls();
      // Cover the former five-second background-discovery cadence.
      await new Promise((resolve) => setTimeout(resolve, 6_100));
      expect(inventoryCalls()).toEqual(before);
      const listed = await ws.sessions();
      expect(listed.map((s) => s.id)).toEqual([managed.id]);

      const removed = await daemon.api.delete(`${ws.base}/${managed.id}`);
      expect(removed.status()).toBe(204);
      expect(await ws.sessions()).toEqual([]);
      // Removing a running session leaves neither its container nor its gateway.
      const leftovers = fixtureFiles(daemon.fixture, 'container-', 'gateway-');
      expect(leftovers.filter((name) => name.includes(managed.id))).toEqual([]);
    } finally {
      await removeProject?.();
      await daemon.stop();
    }
  });

  test('a stalled container check does not block another session and reports uncertain liveness', async () => {
    test.setTimeout(45_000);
    const daemon = await isolatedDaemon();
    const marker = (name: string) => join(daemon.fixture, name);
    let removeProject: (() => Promise<void>) | undefined;
    try {
      const ws = await projectAndWorkstream(daemon);
      removeProject = ws.remove;
      await createIdleSessions(ws, daemon.api, 2);
      const [lost, healthy] = await ws.sessions();

      writeFileSync(marker('block-inventory'), '');
      process.kill(attachPid(daemon.fixture, lost.id), 'SIGKILL');
      await expect
        .poll(() => existsSync(marker('inventory-blocked')))
        .toBe(true);

      // A real wrapped socket error must reject Open without launching a
      // terminal, even while watcher recovery is still checking the container.
      rmSync(marker('terminal.json'), { force: true });
      const opened = await daemon.api.post(`${ws.base}/${lost.id}/open`, {
        data: { mode: 'terminal' },
        timeout: 2_000,
      });
      expect(opened.status()).toBe(500);
      expect(await opened.text()).toContain('cannot open enclave terminal');
      expect(existsSync(marker('terminal.json'))).toBe(false);

      const healthyPath = `${ws.base}/${healthy.id}`;
      const prompt = await daemon.api.post(`${healthyPath}/prompt`, {
        data: { text: 'still responsive' },
        timeout: 2_000,
      });
      expect(prompt.status()).toBe(204);
      const key = await daemon.api.post(`${healthyPath}/key`, {
        data: { key: 'up' },
        timeout: 2_000,
      });
      expect(key.status()).toBe(204);
      const stop = await daemon.api.post(`${healthyPath}/stop`, {
        timeout: 2_000,
      });
      expect(stop.status()).toBe(204);

      writeFileSync(marker('refuse-inventory'), '');
      rmSync(marker('block-inventory'));
      const uncertain = await pollFor('uncertain liveness', async () => {
        const listed = await ws.session(lost.id);
        const readError = listed?.observationData?.readError;
        return listed?.state === 'unknown' &&
          readError?.includes('Docker unavailable')
          ? listed
          : undefined;
      });
      expect(uncertain.error).toBeUndefined();

      const inventoryCount = () => calls(daemon.fixture, 'ps').length;
      const failedAttemptCount = inventoryCount();
      // Hot polling would query about four times here; the 2 s backoff allows at
      // most one retry, wherever the window starts after the failed attempt.
      await new Promise((resolve) => setTimeout(resolve, 1_900));
      expect(inventoryCount() - failedAttemptCount).toBeLessThanOrEqual(1);

      rmSync(marker('refuse-inventory'));
      await pollFor(
        'recovery after Docker returns',
        async () => {
          const listed = await ws.session(lost.id);
          return listed?.state === 'idle' ? listed : undefined;
        },
        10_000,
      );
      expect(calls(daemon.fixture, 'run', 'continue')).toHaveLength(2);
    } finally {
      rmSync(marker('block-inventory'), { force: true });
      rmSync(marker('refuse-inventory'), { force: true });
      await removeProject?.();
      await daemon.stop();
    }
  });

  for (const stalled of ['inventory', 'cleanup'] as const) {
    test(`SSE state updates continue while another session's ${stalled} is stalled`, async () => {
      test.setTimeout(45_000);
      const daemon = await isolatedDaemon();
      let removeProject: (() => Promise<void>) | undefined;
      let stream: Awaited<ReturnType<typeof observeSession>> | undefined;
      let block: string | undefined;
      try {
        const ws = await projectAndWorkstream(daemon);
        removeProject = ws.remove;
        await createIdleSessions(ws, daemon.api, 2);
        const [lost, healthy] = await ws.sessions();
        stream = await observeSession(daemon, healthy.id);

        const [blockName, blockedName] =
          stalled === 'inventory'
            ? ['block-inventory', 'inventory-blocked']
            : [`block-stop-${lost.id}`, `stop-blocked-${lost.id}`];
        block = join(daemon.fixture, blockName);
        writeFileSync(block, '');
        if (stalled === 'inventory') {
          process.kill(attachPid(daemon.fixture, lost.id), 'SIGKILL');
        } else {
          const exit = await daemon.api.post(`${ws.base}/${lost.id}/prompt`, {
            data: { text: 'exit' },
          });
          expect(exit.status()).toBe(204);
        }
        const blocked = join(daemon.fixture, blockedName);
        await expect.poll(() => existsSync(blocked)).toBe(true);

        for (const [text, state] of [
          ['working', 'running'],
          ['ready', 'idle'],
        ]) {
          const prompt = await daemon.api.post(
            `${ws.base}/${healthy.id}/prompt`,
            { data: { text }, timeout: 2_000 },
          );
          expect(prompt.status()).toBe(204);
          const frames = stream.frames;
          const published = () =>
            frames.some((f) => f.state === state && f.preview?.includes(text));
          // Well below the peer's five-second recovery deadline: publishing
          // only after recovery times out is not independent polling.
          await expect
            .poll(published, { timeout: 2_000, intervals: [100] })
            .toBe(true);
          // Do not release the blocked peer to make the assertion pass.
          expect(existsSync(block)).toBe(true);
        }
      } finally {
        await stream?.close();
        if (block) rmSync(block, { force: true });
        await removeProject?.();
        await daemon.stop();
      }
    });
  }

  test('start answers before a slow continue finishes and refuses a restart meanwhile', async () => {
    test.setTimeout(45_000);
    const daemon = await isolatedDaemon();
    const blockContinue = join(daemon.fixture, 'block-continue');
    let removeProject: (() => Promise<void>) | undefined;
    try {
      const ws = await projectAndWorkstream(daemon);
      removeProject = ws.remove;
      await createEnclaveSession(daemon.api, ws.base);
      const session = await pollFor('idle Enclave session', async () =>
        (await ws.sessions()).find(
          (s) => s.provider === 'enclave' && s.state === 'idle',
        ),
      );
      const path = `${ws.base}/${session.id}`;
      const stateOf = async () => (await ws.session(session.id))?.state;
      expect((await daemon.api.post(`${path}/stop`)).status()).toBe(204);
      await expect.poll(stateOf).toBe('stopped');

      writeFileSync(blockContinue, '');
      // The continue may build an image for minutes; the request must not wait on it.
      const start = await daemon.api.post(`${path}/start`, { timeout: 5_000 });
      expect(start.status()).toBe(204);
      const continueBlocked = join(daemon.fixture, 'continue-blocked');
      await expect.poll(() => existsSync(continueBlocked)).toBe(true);
      await expect.poll(stateOf).toBe('starting');
      expect((await daemon.api.post(`${path}/restart`)).status()).toBe(409);

      rmSync(blockContinue);
      await expect.poll(stateOf, { timeout: 15_000 }).toBe('idle');
      expect(calls(daemon.fixture, 'run', 'continue')).toHaveLength(2);
    } finally {
      rmSync(blockContinue, { force: true });
      await removeProject?.();
      await daemon.stop();
    }
  });

  test('reports refused starts with stderr and leaves no container or gateway', async () => {
    test.setTimeout(45_000);
    const daemon = await isolatedDaemon();
    let removeProject: (() => Promise<void>) | undefined;
    try {
      writeFileSync(join(daemon.fixture, 'refuse-start'), '');
      const ws = await projectAndWorkstream(daemon);
      removeProject = ws.remove;
      await createEnclaveSession(daemon.api, ws.base);
      const session = await pollFor('failed start', async () =>
        (await ws.sessions()).find((s) => s.state === 'error'),
      );
      expect(session.error).toContain(
        'refusing to mount sensitive host paths: fixture refusal',
      );
      expect(attaches(daemon.fixture)).toEqual([]);
      expect(
        fixtureFiles(daemon.fixture, 'container-', 'gateway-'),
      ).toHaveLength(0);
    } finally {
      await removeProject?.();
      await daemon.stop();
    }
  });

  test('fails a session whose container dies at startup and keeps the container for its logs', async () => {
    test.setTimeout(45_000);
    const daemon = await isolatedDaemon();
    let removeProject: (() => Promise<void>) | undefined;
    try {
      writeFileSync(join(daemon.fixture, 'crash-on-start'), '');
      const ws = await projectAndWorkstream(daemon);
      removeProject = ws.remove;
      await createEnclaveSession(daemon.api, ws.base);
      const session = await pollFor('failed startup', async () =>
        (await ws.sessions()).find((s) => s.state === 'error'),
      );
      expect(session.error).toContain(`docker logs ${session.id}`);
      const containers = () => fixtureFiles(daemon.fixture, 'container-');
      expect(containers()).toHaveLength(1);
      expect(calls(daemon.fixture, 'stop')).toEqual([]);

      const removed = await daemon.api.delete(`${ws.base}/${session.id}`);
      expect(removed.status()).toBe(204);
      expect(containers()).toHaveLength(0);
    } finally {
      await removeProject?.();
      await daemon.stop();
    }
  });
});

async function createIdleSessions(
  ws: EnclaveWorkstream,
  api: Awaited<ReturnType<typeof isolatedDaemon>>['api'],
  count: number,
) {
  for (let created = 1; created <= count; created++) {
    await createEnclaveSession(api, ws.base);
    await pollFor('idle sessions', async () => {
      const sessions = await ws.sessions();
      return sessions.length === created &&
        sessions.every((s) => s.state === 'idle')
        ? sessions
        : undefined;
    });
  }
}
