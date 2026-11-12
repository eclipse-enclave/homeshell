import { expect, test, type APIRequestContext } from '@playwright/test';
import { isPlaceholderSession } from '../placeholders';
import {
  createProject,
  createWorkstream,
  pollFor,
  sessionsOf,
} from './workstream-helpers';

/**
 * Black-box tests for starting a session again with new launch settings, and
 * for reading back what a session was started with — driven through the fake
 * provider's "sandboxed" agent, whose create form carries the nested container
 * group the Enclave provider offers.
 */

const awaitRealSession = (
  request: APIRequestContext,
  projectId: string,
  workstreamId: string,
) =>
  pollFor(
    `a real session in workstream ${workstreamId}`,
    async () =>
      (await sessionsOf(request, projectId, workstreamId)).find(
        (session) => !isPlaceholderSession(session.id),
      ),
    5000,
  );

/**
 * Polls the launch-config endpoint until it reports a config: the async create
 * records it as the session lands, and until then the session reports none.
 */
const awaitLaunchConfig = (request: APIRequestContext, sessionPath: string) =>
  pollFor(
    `the launch config of ${sessionPath}`,
    async () => {
      const res = await request.get(`${sessionPath}/launch-config`);
      if (res.status() !== 200) return undefined;
      const launch = await res.json();
      return launch.config === null ? undefined : launch;
    },
    10_000,
  );

const created = {
  model: 'opus',
  enclave: {
    features: ['devtools', 'node-dev', 'playwright-mcp'],
    dirs: [{ path: '/tmp/fixtures', readonly: true }],
    allowDomains: ['api.example.com'],
    bridgePorts: [5432],
  },
};

/**
 * The schema the fake lists for `optionId` in the workstream of `sessionPath`,
 * which is what the session's launch config should carry.
 */
async function listedSchema(
  request: APIRequestContext,
  sessionPath: string,
  optionId: string,
) {
  const workstreamPath = sessionPath.slice(
    0,
    sessionPath.indexOf('/sessions/'),
  );
  const [group] = await (
    await request.get(`${workstreamPath}/session-schemas`)
  ).json();
  return group.options.find((option: { id: string }) => option.id === optionId)
    .schema;
}

async function sandboxedSession(
  request: APIRequestContext,
  config: object | null = created,
) {
  const projectId = await createProject(request, 'hs-restart-e2e-');
  const wsId = await createWorkstream(request, projectId, 'restart');
  const base = `/api/projects/${projectId}/workstreams/${wsId}/sessions`;
  const res = await request.post(base, {
    data: { providerId: 'fake', optionId: 'sandboxed', config },
  });
  expect(res.status()).toBe(202);
  const session = await awaitRealSession(request, projectId, wsId);
  return `${base}/${session.id}`;
}

test.describe('session restart API', () => {
  test('the session-schemas carry the nested container group', async ({
    request,
  }) => {
    const projectId = await createProject(request, 'hs-restart-e2e-');
    const wsId = await createWorkstream(request, projectId, 'restart');
    const res = await request.get(
      `/api/projects/${projectId}/workstreams/${wsId}/session-schemas`,
    );
    const [group] = await res.json();
    const option = group.options.find(
      (candidate: { id: string }) => candidate.id === 'sandboxed',
    );
    const container = option.schema.properties.enclave;
    expect(container.type).toBe('object');
    expect(Object.keys(container.properties)).toContain('allowDomains');
    expect(container.properties.dirs['x-inherited'].values).not.toHaveLength(
      0,
    );
  });

  test('the providers report which can restart a session', async ({
    request,
  }) => {
    const providers = await (await request.get('/api/providers')).json();
    const fake = providers.find((p: { id: string }) => p.id === 'fake');
    expect(fake.capabilities.sessionRestart).toBe(true);
  });

  test('a created session reports what it was started with', async ({
    request,
  }) => {
    const sessionPath = await sandboxedSession(request);
    expect(await awaitLaunchConfig(request, sessionPath)).toEqual({
      providerId: 'fake',
      optionId: 'sandboxed',
      config: created,
      schema: await listedSchema(request, sessionPath, 'sandboxed'),
    });
  });

  test('a restart records the new settings and keeps the session', async ({
    request,
  }) => {
    const sessionPath = await sandboxedSession(request);
    await awaitLaunchConfig(request, sessionPath);
    const changed = {
      model: 'sonnet',
      enclave: { ...created.enclave, hostConfig: true, ports: ['8080:80'] },
    };

    const res = await request.post(`${sessionPath}/restart`, {
      data: { config: changed, fresh: false },
    });
    expect(res.status()).toBe(202);
    const session = await res.json();
    expect(sessionPath.endsWith(`/${session.id}`)).toBe(true);

    // Read once the restart is over: a restarting session's settings are refused.
    expect((await awaitLaunchConfig(request, sessionPath)).config).toEqual(
      changed,
    );
  });

  test('only what differs from the defaults is recorded', async ({
    request,
  }) => {
    const full = {
      model: 'sonnet',
      enclave: {
        features: ['node-dev', 'devtools'],
        dirs: [{ path: '/tmp/fixtures', readonly: true }],
        ports: [],
        bridgePorts: [],
        allowDomains: ['api.example.com'],
        hostConfig: false,
      },
    };
    const sessionPath = await sandboxedSession(request, full);
    expect((await awaitLaunchConfig(request, sessionPath)).config).toEqual({
      model: 'sonnet',
      enclave: {
        dirs: [{ path: '/tmp/fixtures', readonly: true }],
        allowDomains: ['api.example.com'],
      },
    });

    const res = await request.post(`${sessionPath}/restart`, {
      data: {
        config: { ...full, enclave: { ...full.enclave, hostConfig: true } },
      },
    });
    expect(res.status()).toBe(202);
    expect((await awaitLaunchConfig(request, sessionPath)).config).toEqual({
      model: 'sonnet',
      enclave: {
        dirs: [{ path: '/tmp/fixtures', readonly: true }],
        allowDomains: ['api.example.com'],
        hostConfig: true,
      },
    });
  });

  test('a session created with a null config restarts without one', async ({
    request,
  }) => {
    const sessionPath = await sandboxedSession(request, null);
    const launch = await (
      await request.get(`${sessionPath}/launch-config`)
    ).json();
    expect(launch).toEqual({
      providerId: 'fake',
      optionId: 'sandboxed',
      config: null,
      schema: await listedSchema(request, sessionPath, 'sandboxed'),
    });

    const res = await request.post(`${sessionPath}/restart`, { data: {} });
    expect(res.status()).toBe(202);
  });

  test('a restart without a config replays the recorded settings', async ({
    request,
  }) => {
    const sessionPath = await sandboxedSession(request);
    await awaitLaunchConfig(request, sessionPath);

    for (const data of [{ fresh: true }, { config: null }]) {
      await pollFor(
        `a restart with ${JSON.stringify(data)} to be accepted`,
        async () =>
          (await request.post(`${sessionPath}/restart`, { data })).status() ===
            202 || undefined,
        10_000,
      );
      expect((await awaitLaunchConfig(request, sessionPath)).config).toEqual(
        created,
      );
    }
  });

  test('a session HomeShell did not start reports the option it restarts as', async ({
    request,
  }) => {
    // A demo session the fake reports by discovery: adopted, never created
    // through the API, so nothing recorded what it was started with.
    const sessionPath =
      '/api/projects/proj-homeshell/workstreams/ws-hs-docs/sessions/sess-hs-docs';
    const res = await request.get(`${sessionPath}/launch-config`);
    expect(res.status()).toBe(200);
    expect(await res.json()).toEqual({
      providerId: 'fake',
      optionId: 'codex',
      config: null,
      schema: await listedSchema(request, sessionPath, 'codex'),
    });
  });

  test('a restart that does not fit the schema is refused per field', async ({
    request,
  }) => {
    const sessionPath = await sandboxedSession(request);
    await awaitLaunchConfig(request, sessionPath);

    const res = await request.post(`${sessionPath}/restart`, {
      data: { config: { enclave: { bridgePorts: [70000] } } },
    });
    expect(res.status()).toBe(400);
    const body = await res.json();
    expect(Object.keys(body.fields)).toContain('/enclave/bridgePorts/0');
    // Refused before anything changed.
    expect(
      (await (await request.get(`${sessionPath}/launch-config`)).json()).config,
    ).toEqual(created);
  });

  test('settings Enclave would refuse are refused before the restart', async ({
    request,
  }) => {
    const sessionPath = await sandboxedSession(request);
    await awaitLaunchConfig(request, sessionPath);

    const res = await request.post(`${sessionPath}/restart`, {
      data: {
        config: {
          enclave: { ports: ['300O'], allowDomains: ['https://example.com'] },
        },
      },
    });
    expect(res.status()).toBe(400);
    expect(Object.keys((await res.json()).fields)).toEqual(
      expect.arrayContaining(['/enclave/ports/0', '/enclave/allowDomains/0']),
    );
  });

  test('a session already restarting refuses a second restart', async ({
    request,
  }) => {
    const sessionPath = await sandboxedSession(request);
    await awaitLaunchConfig(request, sessionPath);

    const first = await request.post(`${sessionPath}/restart`, {
      data: { config: created },
    });
    expect(first.status()).toBe(202);
    const restarting = await first.json();
    expect(restarting.state).toBe('starting');
    // Starting, but no placeholder: stopping it is not deleting it.
    expect(restarting.placeholder).toBeUndefined();
    // Nor do its settings open for one, as the UI offers none (restartable).
    const settings = await request.get(`${sessionPath}/launch-config`);
    expect(settings.status()).toBe(409);
    const second = await request.post(`${sessionPath}/restart`, {
      data: { config: created },
    });
    expect(second.status()).toBe(409);

    // Once the first is done, the session restarts again.
    await pollFor(
      'the session to accept a restart again',
      async () => {
        const res = await request.post(`${sessionPath}/restart`, {
          data: { config: created },
        });
        return res.status() === 202 || undefined;
      },
      10_000,
    );
  });

  test('removing a restarting session removes it for good', async ({
    request,
  }) => {
    // The fake brings a restarted session back under its ID even if it was
    // removed meanwhile; only the removal cancelling the restart stops that. A
    // second session, restarted only once the first is removed, tells when the
    // first's restart would have ended: a restart takes as long as the fake's
    // startup, so the second's ends after the first's would have.
    const projectId = await createProject(request, 'hs-restart-e2e-');
    const wsId = await createWorkstream(request, projectId, 'restart');
    const base = `/api/projects/${projectId}/workstreams/${wsId}/sessions`;
    const realSessions = async () =>
      (await sessionsOf(request, projectId, wsId)).filter(
        (session) => !isPlaceholderSession(session.id),
      );
    for (const count of [1, 2]) {
      const res = await request.post(base, {
        data: { providerId: 'fake', optionId: 'sandboxed', config: created },
      });
      expect(res.status()).toBe(202);
      await pollFor(`${count} real sessions`, async () =>
        (await realSessions()).length === count ? true : undefined,
      );
    }
    const [removed, witness] = await realSessions();

    const restart = async (sessionId: string) => {
      const res = await request.post(`${base}/${sessionId}/restart`, {
        data: { config: created, fresh: true },
      });
      expect(res.status()).toBe(202);
    };
    await restart(removed.id);
    expect((await request.delete(`${base}/${removed.id}`)).status()).toBe(204);
    await restart(witness.id);

    // Created idle, the witness is running only once its restart has ended.
    expect(witness.state).toBe('idle');
    await pollFor('the other restart to end', async () =>
      (await realSessions()).find(
        (session) => session.id === witness.id && session.state === 'running',
      ),
    );
    expect((await realSessions()).map((session) => session.id)).not.toContain(
      removed.id,
    );
  });

  test('a session sharing its directory with another of its type restarts only fresh', async ({
    request,
  }) => {
    const projectId = await createProject(request, 'hs-restart-e2e-');
    const wsId = await createWorkstream(request, projectId, 'restart');
    const base = `/api/projects/${projectId}/workstreams/${wsId}/sessions`;
    const create = () =>
      request.post(base, {
        data: { providerId: 'fake', optionId: 'sandboxed', config: created },
      });
    expect((await create()).status()).toBe(202);
    const session = await awaitRealSession(request, projectId, wsId);
    expect(session.restartable).toBe(true);
    const sessionPath = `${base}/${session.id}`;
    expect(
      (await awaitLaunchConfig(request, sessionPath)).continueRefusal,
    ).toBeUndefined();

    expect((await create()).status()).toBe(202);
    const { continueRefusal } = await (
      await request.get(`${sessionPath}/launch-config`)
    ).json();
    expect(continueRefusal).toMatch(
      /^cannot continue the session's conversation: another Sandboxed demo session works in /,
    );

    const refused = await request.post(`${sessionPath}/restart`, {
      data: { fresh: false },
    });
    expect(refused.status()).toBe(409);
    expect((await refused.json()).error).toBe(continueRefusal);
    const fresh = await request.post(`${sessionPath}/restart`, {
      data: { fresh: true },
    });
    expect(fresh.status()).toBe(202);
  });

  test('a bare POST restarts with the recorded settings', async ({
    request,
  }) => {
    const sessionPath = await sandboxedSession(request);
    await awaitLaunchConfig(request, sessionPath);
    const res = await request.post(`${sessionPath}/restart`);
    expect(res.status()).toBe(202);
    expect((await awaitLaunchConfig(request, sessionPath)).config).toEqual(
      created,
    );
  });

  test('a session created with the retired playwrightMcp restarts bare', async ({
    request,
  }) => {
    const retired = {
      model: 'opus',
      enclave: { allowDomains: ['api.example.com'], playwrightMcp: true },
    };
    const sessionPath = await sandboxedSession(request, retired);
    expect((await awaitLaunchConfig(request, sessionPath)).config).toEqual(
      retired,
    );
    const res = await request.post(`${sessionPath}/restart`);
    expect(res.status()).toBe(202);
    expect((await awaitLaunchConfig(request, sessionPath)).config).toEqual(
      retired,
    );
  });

  test('a config that names no option is refused', async ({ request }) => {
    const projectId = await createProject(request, 'hs-restart-e2e-');
    const wsId = await createWorkstream(request, projectId, 'restart');
    const res = await request.post(
      `/api/projects/${projectId}/workstreams/${wsId}/sessions`,
      { data: { providerId: 'fake', config: created } },
    );
    expect(res.status()).toBe(400);
  });

  test('a failed create is not restartable, and says why when asked', async ({
    request,
  }) => {
    const projectId = await createProject(request, 'hs-restart-e2e-');
    const wsId = await createWorkstream(request, projectId, 'restart');
    const base = `/api/projects/${projectId}/workstreams/${wsId}/sessions`;
    const res = await request.post(base, {
      data: { providerId: 'fake', optionId: 'no-such-agent', config: {} },
    });
    expect(res.status()).toBe(202);
    const failed = await pollFor(
      'the failed create',
      async () =>
        (await sessionsOf(request, projectId, wsId)).find(
          (session) => session.state === 'error',
        ),
      5000,
    );
    expect(failed.restartable).toBe(false);
    const path = `${base}/${failed.id}`;

    const launch = await request.get(`${path}/launch-config`);
    expect(launch.status()).toBe(409);
    expect((await launch.json()).error).toMatch(/never started/);
    const restart = await request.post(`${path}/restart`, {
      data: { config: {} },
    });
    expect(restart.status()).toBe(409);
    expect((await restart.json()).error).toMatch(/never started/);
  });

  test('an unknown session is a 404 for both', async ({ request }) => {
    const projectId = await createProject(request, 'hs-restart-e2e-');
    const wsId = await createWorkstream(request, projectId, 'restart');
    const gone = `/api/projects/${projectId}/workstreams/${wsId}/sessions/sess-nope`;
    expect((await request.get(`${gone}/launch-config`)).status()).toBe(404);
    expect(
      (await request.post(`${gone}/restart`, { data: { config: {} } })).status(),
    ).toBe(404);
  });
});
