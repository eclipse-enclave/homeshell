import { expect, test, type APIRequestContext } from '@playwright/test';
import { writeFileSync } from 'node:fs';
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

/**
 * The Enclave Container settings' "Image inbox", against the fixture `enclave`,
 * which resolves `image_inbox` as an Enclave with the inbox does (enclave#230).
 */
test.describe('Enclave image inbox setting', () => {
  test.skip(process.platform === 'win32', 'Host bridge requires Unix PTYs');
  test.beforeAll(buildEnclaveFixture);
  test.afterEach(disposeIsolatedDaemons);
  test.afterAll(removeEnclaveFixture);

  // The codex option's Container group, as the create form gets it.
  async function containerGroup(api: APIRequestContext, ws: EnclaveWorkstream) {
    const groups = await (
      await api.get(`/api/projects/${ws.id}/workstreams/${ws.ws.id}/session-schemas`)
    ).json();
    const group = groups.find((g: { providerId: string }) => g.providerId === 'enclave');
    const option = group.options.find((o: { id: string }) => o.id === 'codex');
    return option.schema.properties.enclave;
  }

  async function startSession(
    api: APIRequestContext,
    ws: EnclaveWorkstream,
    config: Record<string, unknown>,
  ) {
    const response = await api.post(ws.base, {
      data: { providerId: 'enclave', optionId: 'codex', config },
    });
    expect(response.status(), await response.text()).toBe(202);
    const [session] = await pollFor('the started session', async () => {
      const sessions = await ws.sessions();
      return sessions.some((s) => s.state === 'idle') ? sessions : undefined;
    });
    return session.id;
  }

  const launchConfig = async (api: APIRequestContext, path: string) =>
    (await (await api.get(`${path}/launch-config`)).json()).config;

  test('is offered with Enclave’s configured value, and not by an Enclave without it', async () => {
    test.setTimeout(60_000);
    const daemon = await isolatedDaemon();
    writeFileSync(join(daemon.fixture, 'image-inbox-on'), '');
    const ws = await projectAndWorkstream(daemon);
    try {
      const offered = await containerGroup(daemon.api, ws);
      expect(offered.properties.imageInbox).toMatchObject({
        type: 'boolean',
        title: 'Image inbox',
        default: true,
        'x-source': 'Enclave global config',
      });
      expect(offered.properties.imageInbox.description).toContain(
        'every session of this project',
      );
      expect(offered.properties.imageInbox.description).toContain('Docker or Podman');

      writeFileSync(join(daemon.fixture, 'no-image-inbox'), '');
      // The contract check's verdict is kept per binary, so a new daemon asks afresh.
      await daemon.restart();
      expect((await containerGroup(daemon.api, ws)).properties).not.toHaveProperty(
        'imageInbox',
      );
    } finally {
      await ws.remove();
      await daemon.stop();
    }
  });

  test('is passed on start and replayed on restart only when it differs from the config', async () => {
    test.setTimeout(60_000);
    const daemon = await isolatedDaemon();
    const ws = await projectAndWorkstream(daemon);
    try {
      const on = { enclave: { imageInbox: true } };
      const id = await startSession(daemon.api, ws, on);
      const path = `${ws.base}/${id}`;
      expect(calls(daemon.fixture, 'run').at(-1)).toContain('--image-inbox');
      expect(await launchConfig(daemon.api, path)).toEqual(on);

      let restarted = await daemon.api.post(`${path}/restart`, {
        data: { config: on, fresh: false },
      });
      expect(restarted.status()).toBe(202);
      await expect
        .poll(() => calls(daemon.fixture, 'continue').at(-1) ?? [])
        .toContain('--image-inbox');
      await rowWhere(ws, id, 'idle');

      // At Enclave's configured value no flag is passed, and nothing is recorded.
      restarted = await daemon.api.post(`${path}/restart`, {
        data: { config: { enclave: { imageInbox: false } }, fresh: false },
      });
      expect(restarted.status()).toBe(202);
      await expect.poll(() => calls(daemon.fixture, 'continue').length).toBe(2);
      const last = calls(daemon.fixture, 'continue').at(-1) ?? [];
      expect(last.filter((arg) => arg.startsWith('--image-inbox'))).toEqual([]);
      await rowWhere(ws, id, 'idle');
      expect(await launchConfig(daemon.api, path)).toEqual({ enclave: {} });

      // Against a config that turns it on, a session can turn it off.
      writeFileSync(join(daemon.fixture, 'image-inbox-on'), '');
      restarted = await daemon.api.post(`${path}/restart`, {
        data: { config: { enclave: { imageInbox: false } }, fresh: false },
      });
      expect(restarted.status()).toBe(202);
      await expect.poll(() => calls(daemon.fixture, 'continue').length).toBe(3);
      expect(calls(daemon.fixture, 'continue').at(-1)).toContain('--image-inbox=false');
    } finally {
      await ws.remove();
      await daemon.stop();
    }
  });

  test('an adopted CLI session started with it shows it in its settings', async () => {
    test.setTimeout(60_000);
    const daemon = await isolatedDaemon({ inventoryMs: 200 });
    const ws = await projectAndWorkstream(daemon);
    try {
      const name = 'enclave-codex-0123456789ab-inbox';
      cliContainer(daemon.fixture, {
        name,
        session: 'inbox',
        dir: join(daemon.root, 'project'),
        background: true,
        options: { image_inbox: true },
      });
      await rowWhere(ws, name, 'idle');
      expect(await launchConfig(daemon.api, `${ws.base}/${name}`)).toEqual({
        enclave: { imageInbox: true },
      });
    } finally {
      await ws.remove();
      await daemon.stop();
    }
  });

  test('a running session shows it as outdated once a restart would change it', async () => {
    test.setTimeout(60_000);
    const daemon = await isolatedDaemon({ inventoryMs: 200 });
    const ws = await projectAndWorkstream(daemon);
    try {
      const id = await startSession(daemon.api, ws, {});
      expect((await ws.session(id))?.outdatedSettings).toBeUndefined();

      writeFileSync(join(daemon.fixture, 'image-inbox-on'), '');
      await expect
        .poll(async () => (await ws.session(id))?.outdatedSettings, {
          timeout: 15_000,
        })
        .toEqual(['image_inbox']);
    } finally {
      await ws.remove();
      await daemon.stop();
    }
  });
});
