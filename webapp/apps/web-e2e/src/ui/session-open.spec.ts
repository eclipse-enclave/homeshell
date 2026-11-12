import { createServer } from 'node:http';
import { expect, test } from '../fixtures';
import { pollFor, sessionsOf } from '../api/workstream-helpers';
import { loadApp } from './keyboard-helpers';
import { selectProject, workstreamWithSessions } from './workstream-helpers';

// Keep a deterministic snapshot on a real, open SSE connection. Ending a mocked
// SSE response would mark the app offline and disable the controls under test.
for (const state of ['unknown', 'starting', 'stopped'] as const) {
  test(`a ${state} session ${state === 'unknown' ? 'can' : 'cannot'} be opened`, async ({
    page,
    request,
  }) => {
    const created = await workstreamWithSessions(
      request,
      'hs-ui-open',
      'open-session',
    );
    const session = await pollFor('the live session', async () => {
      const [session] = await sessionsOf(
        request,
        created.projectId,
        created.workstreamId,
      );
      return session && !session.id.startsWith('starting-')
        ? session
        : undefined;
    });
    const response = await request.get('/api/projects');
    expect(response.status()).toBe(200);
    const snapshot = await response.json();
    const project = snapshot.projects.find(
      (project: { id: string }) => project.id === created.projectId,
    );
    const workstream = project.workstreams.find(
      (workstream: { id: string }) => workstream.id === created.workstreamId,
    );
    workstream.sessions = [
      {
        ...workstream.sessions[0],
        state,
        preview: 'Unclassified terminal output',
      },
    ];

    const server = createServer((_request, response) => {
      response.writeHead(200, {
        'content-type': 'text/event-stream',
        'access-control-allow-origin': '*',
      });
      response.write(`event: state\ndata: ${JSON.stringify(snapshot)}\n\n`);
    });
    await new Promise<void>((resolve) =>
      server.listen(0, '127.0.0.1', resolve),
    );
    const address = server.address();
    if (!address || typeof address === 'string')
      throw new Error('SSE server has no TCP address');
    try {
      await page.route('**/api/events**', (route) =>
        route.continue({ url: `http://127.0.0.1:${address.port}/events` }),
      );
      const openPath = `/api/projects/${created.projectId}/workstreams/${created.workstreamId}/sessions/${session.id}/open`;
      let opens = 0;
      await page.route(`**${openPath}`, async (route) => {
        expect(route.request().method()).toBe('POST');
        expect(route.request().postDataJSON()).toEqual({ mode: 'terminal' });
        opens++;
        await route.fulfill({ json: {} });
      });
      await loadApp(page);
      await selectProject(page, created.projectPrefix);
      const card = page.getByRole('region', {
        name: `Workstream ${created.workstreamName}`,
      });
      const row = card.getByRole('listitem');
      await expect(row.getByTestId('session-state')).toHaveText(
        state === 'unknown'
          ? 'Unknown'
          : state === 'starting'
            ? 'Starting'
            : 'Stopped',
      );
      const open = row.getByRole('button', { name: 'Open', exact: true });
      if (state === 'unknown') {
        await expect(row.getByTestId('session-preview')).toContainText(
          'Unclassified terminal output',
        );
        await expect(open).toBeEnabled();
        await open.click();
        await expect.poll(() => opens).toBe(1);
      } else {
        // An ended session offers Continue in Open's place.
        if (state === 'stopped') await expect(open).toHaveCount(0);
        else await expect(open).toBeDisabled();
        expect(opens).toBe(0);
      }
    } finally {
      server.closeAllConnections();
      await new Promise<void>((resolve, reject) =>
        server.close((error) => (error ? reject(error) : resolve())),
      );
    }
  });
}
