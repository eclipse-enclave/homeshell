import { expect, test, type APIRequestContext } from '@playwright/test';

/**
 * `/api/agents` and `/api/agent-settings` — the agent descriptions: which agents
 * HomeShell can launch, and which settings a new session of each one offers.
 *
 * These used to be Go inside the Local provider — a slice of agents, a JSON-Schema
 * constant and an argv builder per agent — so a flag a CLI added, or a wording fix,
 * was a HomeShell release. They are rows now, and both session providers
 * *generate* every create form and every launch command from them.
 *
 * That is what this suite is black-box evidence for: a row goes in over HTTP and
 * comes back out shaping what a client is offered, with no restart in between.
 *
 * The suite shares one daemon with every other worker, so it only ever adds its
 * own agents and removes them again. The seeded rows are read but never edited:
 * another worker is reading them at the same time. For the same reason nothing here
 * asserts on the *whole* listing — the throwaway agents of the tests running next to
 * this one are in it too.
 *
 * What the rows then *generate* — the create form each provider offers and the argv it
 * launches — is asserted in Go (internal/agentspec, internal/persist/agentspecs)
 * rather than here: the e2e daemon runs `-demo=true`, which registers only the fake
 * provider, so neither of the two providers that read these rows is reachable over
 * this API.
 */

interface Setting {
  id: string;
  agent: string;
  field: string;
  role: string;
  type: string;
  title: string;
  description: string;
  format: string;
  choices: string[] | null;
  default: string;
  delivery: string;
  deliveryArg: string;
  deliveryKey: string;
}

interface Agent {
  id: string;
  binary: string;
  ruleSetId?: string;
  enclaveTool?: string;
  display: string;
  sessionIdFlag: string;
  resumeFlag: string;
  transcript: boolean;
  listModels: string[] | null;
  settings: Setting[];
}

async function listAgents(request: APIRequestContext): Promise<Agent[]> {
  const res = await request.get('/api/agents');
  expect(res.status()).toBe(200);
  return (await res.json()).agents as Agent[];
}

async function findAgent(
  request: APIRequestContext,
  id: string,
): Promise<Agent | undefined> {
  return (await listAgents(request)).find((a) => a.id === id);
}

// A unique agent id per test, so parallel workers sharing the daemon never
// collide on one — and never have to touch a seeded row to have something to edit.
const unique = (name: string) =>
  `${name}-${Math.random().toString(36).slice(2, 8)}`;

/** Adds a throwaway agent and removes it (and its settings) when the test ends. */
async function tempAgent(
  request: APIRequestContext,
  data: Partial<Agent> & { id: string },
): Promise<Agent> {
  const res = await request.post('/api/agents', {
    data: { binary: data.id, display: data.id, ...data },
  });
  expect(res.status()).toBe(201);
  return (await res.json()) as Agent;
}

test.describe('the agent descriptions', () => {
  const added: string[] = [];

  test.afterEach(async ({ request }) => {
    // Deleting the agent takes its settings with it, so one call per agent is the
    // whole cleanup.
    for (const id of added.splice(0)) {
      await request.delete(`/api/agents/${id}`);
    }
  });

  test('detection and Enclave bindings are editable independently of the binary', async ({
    request,
  }) => {
    const id = unique('custom-claude');
    added.push(id);
    const agent = await tempAgent(request, {
      id,
      binary: '/opt/my-claude-wrapper',
      display: 'Custom Claude',
      ruleSetId: 'claude',
      enclaveTool: `${id}-profile`,
    });
    expect(agent.ruleSetId).toBe('claude');
    expect(agent.enclaveTool).toBe(`${id}-profile`);

    const rename = await request.patch(`/api/agents/${id}`, {
      data: { binary: '/opt/renamed-wrapper' },
    });
    expect(rename.status()).toBe(200);
    expect(await findAgent(request, id)).toMatchObject({
      binary: '/opt/renamed-wrapper',
      ruleSetId: 'claude',
      enclaveTool: `${id}-profile`,
    });

    const remap = await request.patch(`/api/agents/${id}`, {
      data: { ruleSetId: 'codex', enclaveTool: `${id}-other` },
    });
    expect(remap.status()).toBe(200);
    expect(await findAgent(request, id)).toMatchObject({
      ruleSetId: 'codex',
      enclaveTool: `${id}-other`,
    });

    const conflict = await request.patch(`/api/agents/${id}`, {
      data: { enclaveTool: 'claude' },
    });
    expect(conflict.status()).toBe(409);
    expect((await findAgent(request, id))?.enclaveTool).toBe(`${id}-other`);

    const reset = await request.patch(`/api/agents/${id}`, {
      data: { ruleSetId: '', enclaveTool: '' },
    });
    expect(reset.status()).toBe(200);
    const resetAgent = await findAgent(request, id);
    expect(resetAgent?.ruleSetId).toBeUndefined();
    expect(resetAgent?.enclaveTool).toBeUndefined();
  });

  test('creating an agent with a taken Enclave tool names the tool, not the new ID', async ({
    request,
  }) => {
    const id = unique('custom-claude');
    added.push(id);
    const conflict = await request.post('/api/agents', {
      data: {
        id,
        binary: '/opt/my-claude-wrapper',
        display: 'Custom Claude',
        enclaveTool: 'claude',
      },
    });
    expect(conflict.status()).toBe(409);
    expect(await conflict.json()).toMatchObject({
      error: 'that agent or setting already exists: Enclave tool "claude"',
    });
    expect(await findAgent(request, id)).toBeUndefined();
  });

  test('the seeded rows describe the agents that ship', async ({ request }) => {
    const agents = await listAgents(request);
    const seeded = [
      'claude',
      'codex',
      'gemini',
      'opencode',
      'pi',
      'mistral-vibe',
      'amp',
      'agy',
      'cline',
      'copilot',
      'cursor',
      'devin',
      'droid',
      'grok',
      'kiro',
      'letta',
      'maki',
      'muse',
      'qodercli',
      'qwen',
    ];

    // Every seeded agent is described, in the order the seed put them in. Filtered
    // rather than compared whole: a worker running next to this one has its own
    // throwaway agents in the same listing.
    expect(agents.map((a) => a.id).filter((id) => seeded.includes(id))).toEqual(
      seeded,
    );

    // Claude is the one agent whose sessions can be resumed by an id the daemon
    // generates, and the one that writes a transcript (how a resume tells whether
    // there is a conversation to resume). Both are rows now rather than Go booleans.
    const claude = agents.find((a) => a.id === 'claude');
    expect(claude?.resumeFlag).toBe('--resume');
    expect(claude?.sessionIdFlag).toBe('--session-id');
    expect(claude?.transcript).toBe(true);
    // Its form is the model and its two launch knobs, then the initial prompt last
    // — plus the title, which delivers without ever being a field.
    expect(claude?.settings.map((s) => s.field)).toEqual([
      'model',
      'effort',
      'permissionMode',
      'title',
      'prompt',
    ]);
    // And that is the rule, not Claude's own layout: the initial prompt is the last
    // row of every seeded form that has one. It is the only multi-line field these
    // forms carry, so anywhere else puts a tall, usually empty box between the
    // selects.
    const withPrompt = agents.filter(
      (a) =>
        seeded.includes(a.id) && a.settings.some((s) => s.role === 'prompt'),
    );
    expect(withPrompt.map((a) => a.id)).toEqual([
      'claude',
      'codex',
      'gemini',
      'pi',
      'mistral-vibe',
    ]);
    for (const agent of withPrompt) {
      expect(`${agent.id}: ${agent.settings.at(-1)?.role}`).toBe(
        `${agent.id}: prompt`,
      );
    }

    const model = claude?.settings.find((s) => s.field === 'model');
    expect(model?.role).toBe('model');
    expect(model?.delivery).toBe('flag');
    expect(model?.deliveryArg).toBe('--model');

    expect(agents.find((a) => a.id === 'claude')?.listModels).toBeFalsy();

    // Codex's reasoning effort is the one seeded setting the CLI takes as a config
    // override rather than as a flag of its own.
    const codexEffort = agents
      .find((a) => a.id === 'codex')
      ?.settings.find((s) => s.field === 'reasoningEffort');
    expect(codexEffort?.delivery).toBe('assign');
    expect(codexEffort?.deliveryArg).toBe('-c');
    expect(codexEffort?.deliveryKey).toBe('model_reasoning_effort');
  });

  test('an agent can be described, edited and removed', async ({ request }) => {
    const id = unique('aider');
    added.push(id);

    const created = await tempAgent(request, {
      id,
      binary: id,
      display: 'Aider',
      listModels: ['--models'],
    });
    expect(created.display).toBe('Aider');
    expect(created.listModels).toEqual(['--models']);
    // A new agent has no settings yet, and says so with an array rather than null.
    expect(created.settings).toEqual([]);
    // It lands after everything that was already described, so the selector reads in
    // the order it was built. Compared against the seeded rows rather than against the
    // last entry: another worker may have added its own agent after this one.
    const order = (await listAgents(request)).map((a) => a.id);
    expect(order.indexOf(id)).toBeGreaterThan(order.indexOf('qwen'));

    // A PATCH keeps what it does not mention: fixing a display name must not
    // silently declare the agent unresumable.
    const patch = await request.patch(`/api/agents/${id}`, {
      data: { display: 'Aider Chat', resumeFlag: '--continue' },
    });
    expect(patch.status()).toBe(200);
    let stored = await findAgent(request, id);
    expect(stored?.display).toBe('Aider Chat');
    expect(stored?.resumeFlag).toBe('--continue');
    expect(stored?.binary).toBe(id);
    expect(stored?.listModels).toEqual(['--models']);

    expect((await request.delete(`/api/agents/${id}`)).status()).toBe(204);
    stored = await findAgent(request, id);
    expect(stored).toBeUndefined();
    // Naming a row that is gone is worth hearing about.
    expect((await request.delete(`/api/agents/${id}`)).status()).toBe(404);
    added.splice(added.indexOf(id), 1);
  });

  test('a launch setting can be added, edited and removed', async ({
    request,
  }) => {
    const id = unique('aider');
    added.push(id);
    await tempAgent(request, { id });

    const create = await request.post('/api/agent-settings', {
      data: {
        agent: id,
        field: 'effort',
        title: 'Reasoning effort',
        choices: ['low', 'high'],
        delivery: 'flag',
        deliveryArg: '--effort',
      },
    });
    expect(create.status()).toBe(201);
    const setting = (await create.json()) as Setting;
    expect(setting.id).toBeTruthy();
    // A body that says nothing about the type gets a string field, not one the
    // daemon would refuse.
    expect(setting.type).toBe('string');

    // It is immediately part of that agent's form, at the end of it.
    expect(
      (await findAgent(request, id))?.settings.map((s) => s.field),
    ).toEqual(['effort']);

    const patch = await request.patch(`/api/agent-settings/${setting.id}`, {
      data: { title: 'Thinking budget', agent: 'claude' },
    });
    expect(patch.status()).toBe(200);
    const edited = (await patch.json()) as Setting;
    expect(edited.title).toBe('Thinking budget');
    // A knob may not change agents: that would be a different knob.
    expect(edited.agent).toBe(id);
    // …and the delivery the PATCH did not mention is kept.
    expect(edited.deliveryArg).toBe('--effort');
    expect(edited.choices).toEqual(['low', 'high']);

    expect(
      (await request.delete(`/api/agent-settings/${setting.id}`)).status(),
    ).toBe(204);
    expect((await findAgent(request, id))?.settings).toEqual([]);
    expect(
      (await request.delete(`/api/agent-settings/${setting.id}`)).status(),
    ).toBe(404);
  });

  test('deleting an agent takes its settings with it', async ({ request }) => {
    const id = unique('aider');
    added.push(id);
    await tempAgent(request, { id });

    const create = await request.post('/api/agent-settings', {
      data: {
        agent: id,
        field: 'model',
        role: 'model',
        title: 'Model',
        delivery: 'flag',
        deliveryArg: '--model',
      },
    });
    expect(create.status()).toBe(201);
    const settingId = ((await create.json()) as Setting).id;

    expect((await request.delete(`/api/agents/${id}`)).status()).toBe(204);
    added.splice(added.indexOf(id), 1);

    // The knobs of an agent nothing can launch have nowhere to appear.
    expect(
      (
        await request.patch(`/api/agent-settings/${settingId}`, {
          data: { title: 'X' },
        })
      ).status(),
    ).toBe(404);
  });

  test('a row the daemon could not act on is refused', async ({ request }) => {
    const id = unique('aider');
    added.push(id);
    await tempAgent(request, { id });

    // An agent needs something to run and a name to be picked out by.
    expect(
      (
        await request.post('/api/agents', { data: { display: 'No id' } })
      ).status(),
    ).toBe(400);
    // A taken id is a conflict: the request is well-formed and the state refuses it.
    expect(
      (
        await request.post('/api/agents', {
          data: { id, binary: 'x', display: 'X' },
        })
      ).status(),
    ).toBe(409);
    // Blanking a field the daemon needs is the client's fault, per field.
    expect(
      (
        await request.patch(`/api/agents/${id}`, { data: { binary: '  ' } })
      ).status(),
    ).toBe(400);
    expect(
      (
        await request.patch('/api/agents/nope', { data: { display: 'X' } })
      ).status(),
    ).toBe(404);

    for (const data of [
      // A delivery nothing could act on would be a field the form shows and the
      // launch drops.
      { agent: id, field: 'x', title: 'X', delivery: 'telepathy' },
      { agent: id, field: 'x', title: 'X', delivery: 'flag' },
      {
        agent: id,
        field: 'x',
        title: 'X',
        type: 'number',
        delivery: 'positional',
      },
      {
        agent: id,
        field: 'x',
        title: 'X',
        delivery: 'assign',
        deliveryArg: '-c',
      },
      { agent: id, title: 'No field', delivery: 'positional' },
      // A knob on an agent nothing describes would never be read.
      { agent: `${id}-ghost`, field: 'x', title: 'X', delivery: 'positional' },
    ]) {
      const res = await request.post('/api/agent-settings', { data });
      expect(res.status(), `refusing ${JSON.stringify(data)}`).toBe(400);
    }

    // Two rows on one field would put that field in the form twice.
    expect(
      (
        await request.post('/api/agent-settings', {
          data: {
            agent: id,
            field: 'model',
            title: 'Model',
            delivery: 'flag',
            deliveryArg: '--model',
          },
        })
      ).status(),
    ).toBe(201);
    expect(
      (
        await request.post('/api/agent-settings', {
          data: {
            agent: id,
            field: 'model',
            title: 'Again',
            delivery: 'flag',
            deliveryArg: '--model',
          },
        })
      ).status(),
    ).toBe(409);
  });
});
