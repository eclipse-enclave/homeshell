import { expect, test } from '../fixtures';
import {
  createProject,
  createSession,
  createWorkstream,
  pollFor,
  sessionsOf,
} from './workstream-helpers';

/**
 * The daemon stamps `endedAt` on a session when it sees it become terminal
 * (done, stopped, error), keeps the row and the stamp while it stays ended, and
 * clears the stamp when the session runs again. It marks the session `ended`
 * while it is, which is what clients list and count it by.
 */

const TERMINAL = ['done', 'stopped', 'error'];

test('a stopped session keeps its row and end time until it starts again', async ({
  request,
}) => {
  const projectId = await createProject(request, 'hs-ended-at-');
  const workstreamId = await createWorkstream(request, projectId, 'ended-at');
  const sessions = `/api/projects/${projectId}/workstreams/${workstreamId}/sessions`;
  const session = await createSession(request, projectId, workstreamId);
  const listed = async () =>
    (await sessionsOf(request, projectId, workstreamId)).find(
      (candidate) => candidate.id === session.id,
    );

  expect((await request.post(`${sessions}/${session.id}/stop`)).ok()).toBe(
    true,
  );
  const stopped = await pollFor('a stopped session with endedAt', async () => {
    const current = await listed();
    return current?.state === 'stopped' && current.endedAt
      ? current
      : undefined;
  });
  expect(Number.isNaN(Date.parse(stopped.endedAt ?? ''))).toBe(false);

  // Three reconcile cycles: the row stays and the stamp is carried, not renewed.
  await new Promise((resolve) => setTimeout(resolve, 3000));
  expect(await listed()).toMatchObject({
    state: 'stopped',
    ended: true,
    endedAt: stopped.endedAt,
  });

  expect((await request.post(`${sessions}/${session.id}/start`)).ok()).toBe(
    true,
  );
  const live = await pollFor('a live session again', async () => {
    const current = await listed();
    return current && !TERMINAL.includes(current.state) ? current : undefined;
  });
  expect(live.endedAt).toBeUndefined();
  expect(live.ended).toBe(false);
});
