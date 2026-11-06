import { expect, test, type APIRequestContext } from '@playwright/test';

/**
 * `/api/agent-models` — the model catalog: which models each agent offers in a new
 * session's Model field.
 *
 * A resource of its own rather than a section of `/api/settings`: these are rows
 * with identities rather than fields of a document, each added, corrected and
 * removed on its own.
 *
 * Each row belongs to an agent described under `/api/agents`. Nothing here
 * validates a model against the agent: the agent is the only authority on whether
 * one exists, and a wrong id fails at launch with the agent's own message.
 *
 * The suite shares one daemon, so it only ever adds its own agents and rows and
 * removes them again — the seeded catalog is read but never edited.
 */

interface Entry {
  id: string;
  agent: string;
  model: string;
}

async function list(request: APIRequestContext): Promise<Entry[]> {
  const res = await request.get('/api/agent-models');
  expect(res.status()).toBe(200);
  return (await res.json()).entries as Entry[];
}

const modelsFor = (entries: Entry[], agent: string) =>
  entries.filter((e) => e.agent === agent).map((e) => e.model);

// A unique agent per test, so parallel workers never collide and nobody has to
// edit a seeded row to have something to change.
const unique = (name: string) =>
  `${name}-${Math.random().toString(36).slice(2, 8)}`;

test.describe('the model catalog', () => {
  const agents: string[] = [];

  test.afterEach(async ({ request }) => {
    // Deleting the agent takes its models with it, so one call per agent is the
    // whole cleanup.
    for (const id of agents.splice(0)) {
      await request.delete(`/api/agents/${id}`);
    }
  });

  /** Describes a throwaway agent to hang rows on, removed when the test ends. */
  async function tempAgent(request: APIRequestContext): Promise<string> {
    const id = unique('aider');
    const res = await request.post('/api/agents', {
      data: { id, binary: id, display: id },
    });
    expect(res.status()).toBe(201);
    agents.push(id);
    return id;
  }

  test('the seeded catalog is what each agent offers', async ({ request }) => {
    const entries = await list(request);

    // Most capable first, which is the order the Go slices carried and the order
    // the Model field offers them in.
    expect(modelsFor(entries, 'claude')).toEqual([
      'claude-opus-5',
      'claude-sonnet-5',
      'claude-haiku-4-5',
      'claude-fable-5',
    ]);
    expect(modelsFor(entries, 'codex')).toEqual([
      'gpt-5.6',
      'gpt-5.5',
      'gpt-5.4',
    ]);
    expect(modelsFor(entries, 'gemini')).toEqual([
      'gemini-3-pro-preview',
      'gemini-2.5-flash',
    ]);

    // Agents that discover their own models have no seeded model rows.
    const agentsResponse = await request.get('/api/agents');
    expect(agentsResponse.status()).toBe(200);
    const agentIds = (await agentsResponse.json()).agents.map(
      (agent: { id: string }) => agent.id,
    );
    for (const agent of ['opencode', 'pi', 'mistral-vibe']) {
      expect(agentIds).toContain(agent);
      expect(modelsFor(entries, agent)).toEqual([]);
    }

    // Every row is addressable, which is why these are rows and not a document.
    for (const entry of entries) {
      expect(entry.id).toBeTruthy();
    }
  });

  test('a model can be offered, corrected and withdrawn', async ({
    request,
  }) => {
    const agent = await tempAgent(request);

    const create = await request.post('/api/agent-models', {
      data: { agent, model: 'first-model' },
    });
    expect(create.status()).toBe(201);
    const entry = (await create.json()) as Entry;
    expect(entry.agent).toBe(agent);

    // A second model lands after the first: the menu reads in the order it was built.
    const second = await request.post('/api/agent-models', {
      data: { agent, model: 'second-model' },
    });
    expect(second.status()).toBe(201);
    expect(modelsFor(await list(request), agent)).toEqual([
      'first-model',
      'second-model',
    ]);

    // Correcting an id must not move it: a typo fix is not a reordering.
    const patch = await request.patch(`/api/agent-models/${entry.id}`, {
      data: { model: 'first-model-fixed' },
    });
    expect(patch.status()).toBe(200);
    // The response is the stored row, so it still names the agent.
    expect(await patch.json()).toEqual({
      id: entry.id,
      agent,
      model: 'first-model-fixed',
    });
    expect(modelsFor(await list(request), agent)).toEqual([
      'first-model-fixed',
      'second-model',
    ]);

    expect(
      (await request.delete(`/api/agent-models/${entry.id}`)).status(),
    ).toBe(204);
    expect(modelsFor(await list(request), agent)).toEqual(['second-model']);
    // Naming a row that is gone is worth hearing about.
    expect(
      (await request.delete(`/api/agent-models/${entry.id}`)).status(),
    ).toBe(404);
  });

  test('a row for an agent with no description is refused', async ({
    request,
  }) => {
    // It would belong to no menu, the same reason a launch setting for one is
    // refused (`POST /api/agent-settings`).
    const create = await request.post('/api/agent-models', {
      data: { agent: unique('not-described'), model: 'whatever/it-wants' },
    });
    expect(create.status()).toBe(400);
  });

  test('deleting an agent takes its models with it', async ({ request }) => {
    const agent = await tempAgent(request);
    const create = await request.post('/api/agent-models', {
      data: { agent, model: 'the-model' },
    });
    expect(create.status()).toBe(201);
    const entry = (await create.json()) as Entry;

    expect((await request.delete(`/api/agents/${agent}`)).status()).toBe(204);
    agents.splice(agents.indexOf(agent), 1);

    expect(modelsFor(await list(request), agent)).toEqual([]);
    expect(
      (
        await request.patch(`/api/agent-models/${entry.id}`, {
          data: { model: 'x' },
        })
      ).status(),
    ).toBe(404);
  });

  test('a row that could not be offered is refused', async ({ request }) => {
    const agent = await tempAgent(request);
    const create = await request.post('/api/agent-models', {
      data: { agent, model: 'the-model' },
    });
    expect(create.status()).toBe(201);

    // A row with no agent belongs to no menu; one with no model puts an empty
    // choice in it.
    expect(
      (await request.post('/api/agent-models', { data: { agent } })).status(),
    ).toBe(400);
    expect(
      (
        await request.post('/api/agent-models', { data: { model: 'orphan' } })
      ).status(),
    ).toBe(400);
    // Offering the same model twice would put it in the menu twice.
    expect(
      (
        await request.post('/api/agent-models', {
          data: { agent, model: 'the-model' },
        })
      ).status(),
    ).toBe(409);
    expect(
      (
        await request.patch('/api/agent-models/nope', { data: { model: 'x' } })
      ).status(),
    ).toBe(404);
  });
});
