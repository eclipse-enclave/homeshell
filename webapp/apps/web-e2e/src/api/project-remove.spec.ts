import type { APIRequestContext } from '@playwright/test';
import { expect, test, tempProjectDir } from '../fixtures';
import { isPlaceholderSession } from '../placeholders';
import {
  addProject,
  createProject,
  createWorkstream,
  pollFor,
  sessionsOf,
  type ListedSession,
} from './workstream-helpers';

/**
 * Black-box tests for removing a project: its sessions are left running, and
 * found again when the project is added back at the same path, but what is in
 * flight for them is cancelled. The fake, like Enclave, keeps a project's
 * sessions by directory, and brings a restarted session back running if its
 * restart runs to the end.
 */

/** Every session of a project, across its workstreams, as listed right now. */
const sessionsInProject = async (
  request: APIRequestContext,
  projectId: string,
): Promise<ListedSession[]> => {
  const state = await (await request.get('/api/projects')).json();
  const project = state.projects?.find(
    (candidate: { id: string }) => candidate.id === projectId,
  );
  return (project?.workstreams ?? []).flatMap(
    (workstream: { sessions?: ListedSession[] }) => workstream.sessions ?? [],
  );
};

test.describe('project removal API', () => {
  test('removing a project cancels a restart in flight and keeps the other sessions', async ({
    request,
  }) => {
    const path = await tempProjectDir('hs-project-remove-e2e-');
    const projectId = await addProject(request, path);
    const wsId = await createWorkstream(request, projectId, 'remove');
    const base = `/api/projects/${projectId}/workstreams/${wsId}/sessions`;
    const realSessions = async () =>
      (await sessionsOf(request, projectId, wsId)).filter(
        (session) => !isPlaceholderSession(session.id),
      );
    for (const count of [1, 2]) {
      const res = await request.post(base, {
        data: { providerId: 'fake', optionId: 'claude' },
      });
      expect(res.status()).toBe(202);
      await pollFor(`${count} real sessions`, async () =>
        (await realSessions()).length === count ? true : undefined,
      );
    }
    const [restarted, kept] = await realSessions();

    // A session in another project, restarted only once the project is removed,
    // tells when the cancelled restart would have ended: a restart takes as long
    // as the fake's startup.
    const witnessProject = await createProject(
      request,
      'hs-project-remove-e2e-',
    );
    const witnessWs = await createWorkstream(
      request,
      witnessProject,
      'witness',
    );
    const witnessBase = `/api/projects/${witnessProject}/workstreams/${witnessWs}/sessions`;
    expect(
      (
        await request.post(witnessBase, {
          data: { providerId: 'fake', optionId: 'claude' },
        })
      ).status(),
    ).toBe(202);
    const witness = await pollFor('the witness session', async () =>
      (await sessionsOf(request, witnessProject, witnessWs)).find(
        (session) => !isPlaceholderSession(session.id),
      ),
    );

    expect(
      (
        await request.post(`${base}/${restarted.id}/restart`, {
          data: { fresh: true },
        })
      ).status(),
    ).toBe(202);
    expect((await request.delete(`/api/projects/${projectId}`)).status()).toBe(
      204,
    );
    expect(
      (await request.post(`${witnessBase}/${witness.id}/restart`)).status(),
    ).toBe(202);
    await pollFor('the witness restart to end', async () =>
      (await sessionsOf(request, witnessProject, witnessWs)).find(
        (session) => session.id === witness.id && session.state === 'running',
      ),
    );

    const again = await addProject(request, path);
    const found = await pollFor(
      'the sessions left running to be found again',
      async () => {
        const sessions = await sessionsInProject(request, again);
        return sessions.length > 0 ? sessions : undefined;
      },
    );
    expect(found.map((session) => session.id)).toEqual([kept.id]);
  });
});
