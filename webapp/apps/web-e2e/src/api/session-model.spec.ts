import { mkdtemp } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { expect, test, type APIRequestContext } from '@playwright/test';
import { isPlaceholderSession } from '../placeholders';

async function createProject(request: APIRequestContext): Promise<string> {
  const dir = await mkdtemp(join(tmpdir(), 'hs-model-e2e-'));
  const res = await request.post('/api/projects', { data: { path: dir } });
  expect(res.status()).toBe(201);
  return (await res.json()).id as string;
}

/** Creates a workstream via the fake provider and returns the real one's ID. */
async function createWorkstream(
  request: APIRequestContext,
  projectId: string,
  timeoutMs = 5000,
): Promise<string> {
  const res = await request.post(`/api/projects/${projectId}/workstreams`, {
    data: { providerId: 'fake', name: 'models', config: { branch: 'main' } },
  });
  expect(res.status()).toBe(201);
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const state = await (await request.get('/api/projects')).json();
    const real = state.projects
      ?.find((p: { id: string }) => p.id === projectId)
      ?.workstreams?.find(
        (w: { id: string }) => !String(w.id).startsWith('creating-'),
      );
    if (real) return real.id as string;
    await new Promise((r) => setTimeout(r, 50));
  }
  throw new Error(`timed out waiting for a workstream in ${projectId}`);
}

/** Polls until the session behind a create request is no longer a placeholder. */
async function awaitRealSession(
  request: APIRequestContext,
  projectId: string,
  workstreamId: string,
  timeoutMs = 5000,
): Promise<{ id: string; agent: string }> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const state = await (await request.get('/api/projects')).json();
    const real = state.projects
      ?.find((p: { id: string }) => p.id === projectId)
      ?.workstreams?.find((w: { id: string }) => w.id === workstreamId)
      ?.sessions?.find((s: { id: string }) => !isPlaceholderSession(s.id));
    if (real) return { id: real.id as string, agent: real.agent as string };
    await new Promise((r) => setTimeout(r, 50));
  }
  throw new Error(`timed out waiting for a session in ${workstreamId}`);
}

type ModelProp = { enum?: string[]; default?: string; title?: string };

/** The model knob an agent option advertises, or undefined if it has none. */
async function modelKnob(
  request: APIRequestContext,
  projectId: string,
  workstreamId: string,
  optionId: string,
): Promise<ModelProp | undefined> {
  const res = await request.get(
    `/api/projects/${projectId}/workstreams/${workstreamId}/session-schemas`,
  );
  expect(res.status()).toBe(200);
  const groups = await res.json();
  const option = groups
    .flatMap((g: { options: { id: string; schema?: unknown }[] }) => g.options)
    .find((o: { id: string }) => o.id === optionId);
  const schema = option?.schema as
    | { properties?: Record<string, ModelProp> }
    | undefined;
  return schema?.properties?.model;
}

/**
 * Black-box tests for choosing an agent's model when creating a session: the
 * daemon advertises the models on offer as a session-option knob, accepts one of
 * them, accepts none (the agent keeps its own), and refuses anything else by
 * name. Driven through the fake provider, whose two options carry the two shapes
 * real providers use — a model with a default, and an optional one without.
 */
test.describe('session model API', () => {
  test('advertises the models an agent offers, with and without a default', async ({
    request,
  }) => {
    const projectId = await createProject(request);
    const wsId = await createWorkstream(request, projectId);

    // A default means the choice is already made: a client shows it selected.
    const withDefault = await modelKnob(request, projectId, wsId, 'claude');
    expect(withDefault?.enum).toEqual(['opus', 'sonnet', 'haiku']);
    expect(withDefault?.default).toBe('sonnet');

    // No default means "unset" is a real answer, and the client has to offer it —
    // this is the shape a container session uses, where the image already has a
    // model and the daemon should not overrule it.
    const optional = await modelKnob(request, projectId, wsId, 'codex');
    expect(optional?.enum).toEqual(['o3', 'o4-mini']);
    expect(optional?.default).toBeUndefined();
  });

  test('creates a session on the chosen model', async ({ request }) => {
    const projectId = await createProject(request);
    const wsId = await createWorkstream(request, projectId);

    const res = await request.post(
      `/api/projects/${projectId}/workstreams/${wsId}/sessions`,
      { data: { providerId: 'fake', optionId: 'codex', config: { model: 'o4-mini' } } },
    );
    expect(res.status()).toBe(202);
    expect((await awaitRealSession(request, projectId, wsId)).agent).toBe(
      'Codex',
    );
  });

  test('creates a session with no model chosen at all', async ({ request }) => {
    const projectId = await createProject(request);
    const wsId = await createWorkstream(request, projectId);

    const res = await request.post(
      `/api/projects/${projectId}/workstreams/${wsId}/sessions`,
      { data: { providerId: 'fake', optionId: 'codex', config: {} } },
    );
    expect(res.status()).toBe(202);
    expect((await awaitRealSession(request, projectId, wsId)).agent).toBe(
      'Codex',
    );
  });

  test('refuses a model the agent does not offer, naming the field', async ({
    request,
  }) => {
    const projectId = await createProject(request);
    const wsId = await createWorkstream(request, projectId);

    const res = await request.post(
      `/api/projects/${projectId}/workstreams/${wsId}/sessions`,
      {
        data: {
          providerId: 'fake',
          optionId: 'codex',
          config: { model: 'claude-opus-5' },
        },
      },
    );
    // Refused on the request, so the create form can point at the field rather
    // than a session row turning red a moment later.
    expect(res.status()).toBe(400);
    expect(Object.keys((await res.json()).fields ?? {}).join()).toContain(
      'model',
    );
  });
});
