import type { Project } from '@homeshell/model';
import { describe, expect, it } from 'vitest';

import { notificationDestination } from './notification-destination';

// Only the ids matter for where a toast leads.
const projects = [
  {
    id: 'p1',
    workstreams: [
      { id: 'w1', sessions: [{ id: 's1' }, { id: 's2' }] },
      { id: 'w2', sessions: [] },
    ],
  },
] as unknown as Project[];

const target = { projectId: 'p1', workstreamId: 'w1', sessionId: 's2' };

describe('notificationDestination', () => {
  it('leads to the session when it is still there', () => {
    expect(notificationDestination(projects, target)).toEqual({
      projectId: 'p1',
      itemId: 's2',
    });
  });

  it('falls back to the workstream when the session is gone', () => {
    expect(
      notificationDestination(projects, { ...target, sessionId: 'gone' }),
    ).toEqual({ projectId: 'p1', itemId: 'w1' });
  });

  it('falls back to the project when the workstream is gone', () => {
    expect(
      notificationDestination(projects, { ...target, workstreamId: 'gone' }),
    ).toEqual({ projectId: 'p1' });
  });

  it('leads nowhere when the project is gone', () => {
    expect(
      notificationDestination(projects, { ...target, projectId: 'gone' }),
    ).toBeUndefined();
  });
});
