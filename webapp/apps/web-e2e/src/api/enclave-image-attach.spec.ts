import { expect, test } from '@playwright/test';
import { existsSync, readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import {
  buildEnclaveFixture,
  calls,
  cliContainer,
  isolatedDaemon,
  projectAndWorkstream,
  removeEnclaveFixture,
  rowWhere,
  type EnclaveWorkstream,
} from '../enclave-helpers';
import { disposeIsolatedDaemons } from '../isolated-daemon';
import { pollFor } from './workstream-helpers';

const PNG = Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 1, 2, 3]);
const JPEG = Buffer.from([0xff, 0xd8, 0xff, 0xe0, 1, 2, 3]);

/**
 * Attaching an image to an Enclave session, against the fixture `enclave`,
 * which imports with `img import --json` as an Enclave with the image inbox
 * does (enclave#230) and records what reaches the agent's input.
 */
test.describe('Enclave image attach', () => {
  test.skip(process.platform === 'win32', 'Host bridge requires Unix PTYs');
  test.beforeAll(buildEnclaveFixture);
  test.afterEach(disposeIsolatedDaemons);
  test.afterAll(removeEnclaveFixture);

  /** What the agent of container `name` has received on its input so far. */
  const agentInput = (fixture: string, name: string) => {
    const file = join(fixture, `input-${name}`);
    return existsSync(file) ? readFileSync(file, 'utf8') : '';
  };

  const attachImage = async (ws: EnclaveWorkstream, id: string) =>
    (await ws.session(id))?.capabilities?.attachImage === true;

  test('types the imported image’s path into the agent’s input without submitting it', async () => {
    test.setTimeout(60_000);
    const daemon = await isolatedDaemon();
    const ws = await projectAndWorkstream(daemon);
    try {
      const response = await daemon.api.post(ws.base, {
        data: { providerId: 'enclave', optionId: 'codex', config: { enclave: { imageInbox: true } } },
      });
      expect(response.status(), await response.text()).toBe(202);
      const [session] = await pollFor('the started session', async () => {
        const sessions = await ws.sessions();
        return sessions.some((s) => s.state === 'idle') ? sessions : undefined;
      });
      const id = session.id;
      await expect.poll(() => attachImage(ws, id)).toBe(true);

      const attached = await daemon.api.post(`${ws.base}/${id}/image`, {
        headers: { 'Content-Type': 'image/png' },
        data: PNG,
      });
      expect(attached.status(), await attached.text()).toBe(200);
      const { path } = await attached.json();
      expect(path).toMatch(/^\/mnt\/host-images\/.+\.png$/);
      expect(calls(daemon.fixture, 'img')).toEqual([
        ['img', 'import', '--session', id, '--json', '-'],
      ]);

      // The path and one space, and no Enter: the user's next prompt goes with it.
      await expect.poll(() => agentInput(daemon.fixture, id)).toBe(`${path} `);
      const prompted = await daemon.api.post(`${ws.base}/${id}/prompt`, {
        data: { text: 'what is in this image?' },
      });
      expect(prompted.status()).toBe(204);
      await expect
        .poll(async () => (await ws.session(id))?.preview)
        .toContain(`received: ${path} what is in this image?`);
    } finally {
      await ws.remove();
      await daemon.stop();
    }
  });

  test('pastes natively when the agent pastes the image’s format, else types its path', async () => {
    test.setTimeout(60_000);
    const daemon = await isolatedDaemon({ inventoryMs: 200 });
    const ws = await projectAndWorkstream(daemon);
    try {
      writeFileSync(join(daemon.fixture, 'tools'), 'claude\nopencode\n');
      const dir = join(daemon.root, 'project');
      const names = {
        claude: 'enclave-claude-0123456789ab-paste',
        opencode: 'enclave-opencode-0123456789ab-paste',
        codex: 'enclave-codex-0123456789ab-paste',
      };
      for (const [tool, name] of Object.entries(names))
        cliContainer(daemon.fixture, {
          name,
          session: 'paste',
          dir,
          background: true,
          tool,
          options: { image_inbox: true },
        });
      // OpenCode's state comes from hooks, not its screen, so it may stay running.
      for (const name of Object.values(names))
        await pollFor(`${name} able to take an image`, async () => {
          const session = await ws.session(name);
          return ['idle', 'running'].includes(session?.state ?? '') &&
            session?.capabilities?.attachImage
            ? session
            : undefined;
        });
      const attach = async (name: string, contentType: string, data: Buffer) => {
        const response = await daemon.api.post(`${ws.base}/${name}/image`, {
          headers: { 'Content-Type': contentType },
          data,
        });
        expect(response.status(), await response.text()).toBe(200);
        return (await response.json()).path as string;
      };

      // Claude Code pastes PNG and JPEG: Ctrl+V right after the import.
      await attach(names.claude, 'image/png', PNG);
      await expect.poll(() => agentInput(daemon.fixture, names.claude)).toBe('\x16');
      // OpenCode pastes PNG only, so a JPEG is its path; a PNG then is Ctrl+V.
      const jpegPath = await attach(names.opencode, 'image/jpeg', JPEG);
      expect(jpegPath).toMatch(/\.jpg$/);
      await attach(names.opencode, 'image/png', PNG);
      await expect
        .poll(() => agentInput(daemon.fixture, names.opencode))
        .toBe(`${jpegPath} \x16`);
      // Codex pastes nothing natively.
      const codexPath = await attach(names.codex, 'image/png', PNG);
      await expect.poll(() => agentInput(daemon.fixture, names.codex)).toBe(`${codexPath} `);
      // Never a submit.
      for (const name of Object.values(names))
        expect(agentInput(daemon.fixture, name)).not.toMatch(/[\r\n]/);
    } finally {
      await ws.remove();
      await daemon.stop();
    }
  });

  test('declares attachImage when the recorded image inbox is on or unknown', async () => {
    test.setTimeout(60_000);
    const daemon = await isolatedDaemon({ inventoryMs: 200 });
    const ws = await projectAndWorkstream(daemon);
    try {
      const dir = join(daemon.root, 'project');
      const sessions: Record<string, Record<string, unknown> | undefined> = {
        on: { image_inbox: true },
        off: { image_inbox: false },
        // Recorded by an Enclave from before the inbox.
        without: { yolo: true },
        // Not recorded: Enclave could not read the session's provenance.
        unknown: undefined,
      };
      for (const [session, options] of Object.entries(sessions))
        cliContainer(daemon.fixture, {
          name: `enclave-codex-0123456789ab-${session}`,
          session,
          dir,
          background: true,
          ...(options ? { options } : {}),
        });
      for (const session of Object.keys(sessions))
        await rowWhere(ws, `enclave-codex-0123456789ab-${session}`, 'idle');
      const declared = async () =>
        Object.fromEntries(
          await Promise.all(
            Object.keys(sessions).map(async (session) => [
              session,
              await attachImage(ws, `enclave-codex-0123456789ab-${session}`),
            ]),
          ),
        );
      await expect
        .poll(declared)
        .toEqual({ on: true, off: false, without: false, unknown: true });

      // One without the capability is refused before Enclave is asked.
      const refused = await daemon.api.post(`${ws.base}/enclave-codex-0123456789ab-off/image`, {
        headers: { 'Content-Type': 'image/png' },
        data: PNG,
      });
      expect(refused.status()).toBe(501);
      expect(calls(daemon.fixture, 'img')).toEqual([]);
    } finally {
      await ws.remove();
      await daemon.stop();
    }
  });

  test('a failed import is a 500 with Enclave’s error, and types nothing', async () => {
    test.setTimeout(60_000);
    const daemon = await isolatedDaemon({ inventoryMs: 200 });
    const ws = await projectAndWorkstream(daemon);
    try {
      // No options recorded, so HomeShell offers the attach; the container has no inbox.
      const name = 'enclave-codex-0123456789ab-noinbox';
      cliContainer(daemon.fixture, {
        name,
        session: 'noinbox',
        dir: join(daemon.root, 'project'),
        background: true,
      });
      await rowWhere(ws, name, 'idle');
      await expect.poll(() => attachImage(ws, name)).toBe(true);

      const attached = await daemon.api.post(`${ws.base}/${name}/image`, {
        headers: { 'Content-Type': 'image/png' },
        data: PNG,
      });
      expect(attached.status()).toBe(500);
      expect((await attached.json()).error).toBe(
        `session "noinbox" (${name}) has no image inbox; restart it with --image-inbox`,
      );
      expect(calls(daemon.fixture, 'img')).toHaveLength(1);
      expect(agentInput(daemon.fixture, name)).toBe('');
    } finally {
      await ws.remove();
      await daemon.stop();
    }
  });
});
