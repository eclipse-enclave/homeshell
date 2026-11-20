import type { Project } from '@homeshell/model';
import type { NotificationTarget } from './use-notifications';

/** Where to go: a project, and the card or row in it to focus. */
export interface Destination {
  projectId: string;
  itemId?: string;
  /** Expand the session row, to show its output and input. */
  expand?: boolean;
}

/**
 * Where clicking a toast of `kind` leads. The notification may be older than
 * the state: a session gone since falls back to its workstream, a workstream
 * gone to its project, and a project gone leads nowhere. A session waiting for
 * input is expanded, so it can be answered there.
 */
export function notificationDestination(
  projects: readonly Project[],
  target: NotificationTarget,
  kind?: string,
): Destination | undefined {
  const project = projects.find((p) => p.id === target.projectId);
  if (!project) return undefined;
  const workstream = project.workstreams.find(
    (w) => w.id === target.workstreamId,
  );
  if (!workstream) return { projectId: project.id };
  const session = workstream.sessions.find((s) => s.id === target.sessionId);
  if (!session) return { projectId: project.id, itemId: workstream.id };
  return kind === 'waiting'
    ? { projectId: project.id, itemId: session.id, expand: true }
    : { projectId: project.id, itemId: session.id };
}
