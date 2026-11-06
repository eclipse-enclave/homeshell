import { expect, test } from '../fixtures';
import { isPlaceholderSession, isStartingSession } from '../placeholders';
import {
  createProject,
  createWorkstream,
  type ListedSession,
  pollFor,
  sessionsOf,
} from './workstream-helpers';

/**
 * The starting-session placeholder: while a provider brings a session up, the
 * daemon projects a stand-in carrying the agent's name, the `starting` state and
 * the tail of the provider's startup output (for Enclave, the container build
 * log). It is what turns a multi-minute wait into something the UI can show.
 *
 * Driven black-box here, over the projection every client reads. The e2e daemon
 * runs the fake provider with HOMESHELL_FAKE_SESSION_STARTUP_MS set
 * (playwright.config.ts), which is what gives these assertions a window to look
 * through — a real create is fast only because the fake is.
 */

test('a session being created is projected as a starting placeholder', async ({
  request,
}) => {
  const projectId = await createProject(request, 'hs-startup-e2e-');
  const workstreamId = await createWorkstream(request, projectId, 'startup');

  const res = await request.post(
    `/api/projects/${projectId}/workstreams/${workstreamId}/sessions`,
    { data: { providerId: 'fake', optionId: 'claude', config: {} } },
  );
  expect(res.status()).toBe(202);

  const placeholder = await pollFor('the starting placeholder', async () =>
    (await sessionsOf(request, projectId, workstreamId)).find((s) => isStartingSession(s.id)),
  );

  // It names what is coming up, which is the point: "Claude Code" is the fake's
  // label for the `claude` option.
  expect(placeholder.agent).toBe('Claude Code');
  expect(placeholder.state).toBe('starting');
  expect(placeholder.provider).toBe('fake');
});

test('the create answers with the placeholder it projects', async ({
  request,
}) => {
  const projectId = await createProject(request, 'hs-startup-e2e-');
  const workstreamId = await createWorkstream(request, projectId, 'startup');

  const res = await request.post(
    `/api/projects/${projectId}/workstreams/${workstreamId}/sessions`,
    { data: { providerId: 'fake', optionId: 'claude', config: {} } },
  );
  expect(res.status()).toBe(202);
  const created = (await res.json()) as ListedSession;
  expect(isStartingSession(created.id)).toBe(true);
  expect(created.state).toBe('starting');

  const projected = await pollFor('the created placeholder', async () =>
    (await sessionsOf(request, projectId, workstreamId)).find((s) => s.id === created.id),
  );
  expect(projected.agent).toBe(created.agent);
});

test('the startup log tail rides on the placeholder while it builds', async ({
  request,
}) => {
  const projectId = await createProject(request, 'hs-startup-e2e-');
  const workstreamId = await createWorkstream(request, projectId, 'startup');

  await request.post(
    `/api/projects/${projectId}/workstreams/${workstreamId}/sessions`,
    { data: { providerId: 'fake', optionId: 'claude', config: {} } },
  );

  const preview = await pollFor('the startup output', async () => {
    const starting = (await sessionsOf(request, projectId, workstreamId)).find((s) =>
      isStartingSession(s.id),
    );
    return starting?.preview ? starting.preview : undefined;
  });

  // The fake streams buildx-shaped output; any of it proves the sink is wired
  // from the provider through to the projection.
  expect(preview).toContain('#');
  // Retained as *scrollback*, not as what fits on screen: the UI shows ten lines
  // and the user scrolls back through the rest, so the daemon has to keep more
  // than ten. Still bounded (provider.startupTailLines) rather than unbounded.
  const finalPreview = await pollFor('the whole build log', async () => {
    const starting = (await sessionsOf(request, projectId, workstreamId)).find((s) =>
      isStartingSession(s.id),
    );
    const lines = starting?.preview?.split('\n').length ?? 0;
    return lines > 10 ? starting?.preview : undefined;
  });
  expect(finalPreview?.split('\n').length ?? 0).toBeGreaterThan(10);
  // The oldest line survives, which is what makes scrolling up worth anything.
  expect(finalPreview).toContain('#1 [internal] load build definition');
});

test('the placeholder is replaced by the real session, not added to', async ({
  request,
}) => {
  const projectId = await createProject(request, 'hs-startup-e2e-');
  const workstreamId = await createWorkstream(request, projectId, 'startup');

  await request.post(
    `/api/projects/${projectId}/workstreams/${workstreamId}/sessions`,
    { data: { providerId: 'fake', optionId: 'claude', config: {} } },
  );

  const real = await pollFor('the real session', async () =>
    (await sessionsOf(request, projectId, workstreamId)).find(
      (s) => !isPlaceholderSession(s.id),
    ),
  );
  expect(real.state).not.toBe('starting');

  // And nothing is left over: exactly one session, the real one.
  const sessions = await sessionsOf(request, projectId, workstreamId);
  expect(sessions.filter((s) => isStartingSession(s.id))).toHaveLength(0);
  expect(sessions).toHaveLength(1);
});

// A starting session is busy (domain.SessionState.Busy), so the removal guard
// must refuse its workstream while the build is in flight — the container the
// provider is about to return would otherwise outlive the workstream.
test('a workstream is guarded while one of its sessions is starting', async ({
  request,
}) => {
  const projectId = await createProject(request, 'hs-startup-e2e-');
  const workstreamId = await createWorkstream(request, projectId, 'startup');

  await request.post(
    `/api/projects/${projectId}/workstreams/${workstreamId}/sessions`,
    { data: { providerId: 'fake', optionId: 'claude', config: {} } },
  );
  await pollFor('the starting placeholder', async () =>
    (await sessionsOf(request, projectId, workstreamId)).find((s) => isStartingSession(s.id)),
  );

  const res = await request.delete(
    `/api/projects/${projectId}/workstreams/${workstreamId}`,
  );
  expect(res.status()).toBe(409);
  const body = await res.json();
  expect(body.reason).toBe('sessionsRunning');
  expect(body.sessions?.[0]?.state).toBe('starting');
});

/**
 * Force-removing a workstream mid-create must dispose of the build rather than
 * let it finish into a store that has no record to put it in. Observable from
 * outside as: the workstream is gone and *stays* gone — no session reappears
 * under it, and no adopted workstream shows up for the directory it was using.
 */
test('force-removing a workstream mid-create leaves nothing behind', async ({
  request,
}) => {
  const projectId = await createProject(request, 'hs-startup-e2e-');
  const workstreamId = await createWorkstream(request, projectId, 'startup');

  const created = await request.post(
    `/api/projects/${projectId}/workstreams/${workstreamId}/sessions`,
    { data: { providerId: 'fake', optionId: 'claude', config: {} } },
  );
  expect(created.status()).toBe(202);
  await pollFor('the starting placeholder', async () =>
    (await sessionsOf(request, projectId, workstreamId)).find((s) => isStartingSession(s.id)),
  );

  const removed = await request.delete(
    `/api/projects/${projectId}/workstreams/${workstreamId}?force=true`,
  );
  expect(removed.ok()).toBe(true);

  // Give the create every chance to finish and try to land.
  await new Promise((r) => setTimeout(r, 1500));

  const state = await (await request.get('/api/projects')).json();
  const project = state.projects?.find((p: { id: string }) => p.id === projectId);
  const workstreams = (project?.workstreams ?? []) as {
    id: string;
    sessions?: ListedSession[];
  }[];
  expect(workstreams.some((w) => w.id === workstreamId)).toBe(false);
  // And nothing was adopted in its place: a session that outlived its workstream
  // shows up as a discovered (`disc-`) workstream on the next reconcile pass,
  // which is the orphan this is really watching for.
  expect(workstreams.some((w) => w.id.startsWith('disc-'))).toBe(false);
});
