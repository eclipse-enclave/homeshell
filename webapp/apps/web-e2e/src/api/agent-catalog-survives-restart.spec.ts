import { expect, test } from '@playwright/test';

import { isolatedDaemon, type IsolatedDaemon } from '../isolated-daemon';

/**
 * The agent catalog is the user's: an agent described, a setting added to it
 * and a model offered for it are all served again after a restart, and a
 * deleted default stays deleted. Black-box, through the public API, a SIGKILL
 * and `homeshell stop`.
 */

interface Setting {
  id: string;
  agent: string;
  field: string;
}

interface Agent {
  id: string;
  binary: string;
  display: string;
  settings: Setting[];
}

interface Model {
  id: string;
  agent: string;
  model: string;
}

async function agents(daemon: IsolatedDaemon): Promise<Agent[]> {
  const res = await daemon.api('/api/agents');
  expect(res.status).toBe(200);
  return ((await res.json()) as { agents: Agent[] }).agents;
}

async function models(daemon: IsolatedDaemon): Promise<Model[]> {
  const res = await daemon.api('/api/agent-models');
  expect(res.status).toBe(200);
  return ((await res.json()) as { entries: Model[] }).entries;
}

test.describe('the agent catalog survives a restart', () => {
  let daemon: IsolatedDaemon;

  test.beforeAll(() => {
    daemon = isolatedDaemon('cat');
  });

  test.afterAll(() => daemon.dispose());

  test('a custom agent, its setting and its model come back; a deleted default stays gone', async () => {
    test.setTimeout(90_000);
    await daemon.start();

    // A fresh install starts from the defaults.
    const before = await agents(daemon);
    expect(before.map((a) => a.id)).toContain('claude');

    const createdAgent = await daemon.api('/api/agents', {
      method: 'POST',
      body: JSON.stringify({ id: 'aider', binary: 'aider', display: 'Aider' }),
    });
    expect(createdAgent.status).toBe(201);

    const createdSetting = await daemon.api('/api/agent-settings', {
      method: 'POST',
      body: JSON.stringify({
        agent: 'aider',
        field: 'model',
        role: 'model',
        title: 'Model',
        delivery: 'flag',
        deliveryArg: '--model',
      }),
    });
    expect(createdSetting.status).toBe(201);
    const setting = (await createdSetting.json()) as Setting;

    const createdModel = await daemon.api('/api/agent-models', {
      method: 'POST',
      body: JSON.stringify({ agent: 'aider', model: 'aider-4' }),
    });
    expect(createdModel.status).toBe(201);
    const model = (await createdModel.json()) as Model;

    const deleted = await daemon.api('/api/agents/codex', { method: 'DELETE' });
    expect(deleted.status).toBe(204);

    // Killed rather than stopped: every edit has to be written before the API answers.
    await daemon.kill();
    await daemon.start();

    const after = await agents(daemon);
    const aider = after.find((a) => a.id === 'aider');
    expect(aider).toBeDefined();
    expect(aider?.display).toBe('Aider');
    expect(aider?.settings.map((s) => s.id)).toEqual([setting.id]);
    expect(after.map((a) => a.id)).not.toContain('codex');
    // The custom agent landed after every default and stays there.
    expect(after[after.length - 1].id).toBe('aider');

    const entries = await models(daemon);
    expect(entries).toContainEqual(model);
    expect(entries.filter((e) => e.agent === 'codex')).toHaveLength(0);

    await daemon.stop();
  });
});
