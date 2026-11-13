import type {
  DeleteWorkstreamOptions,
  NewSessionData,
  NewWorkstreamData,
  Refusal,
  RestartSessionData,
  WorkstreamDraft,
} from '@homeshell/components';
import type {
  BusySession,
  LaunchConfig,
  OpenResult,
  Provider,
  Session,
  SessionProviderSchemas,
  Workstream,
} from '@homeshell/model';
import { REASON_SESSIONS_RUNNING } from '@homeshell/model';
import { describeDaemonError, type DaemonErrorBody } from './field-errors';
import { openExternal } from './open-external';

/** The daemon requests a project view makes. */

export async function renameProject(
  id: string,
  name: string,
): Promise<string | null> {
  try {
    const res = await fetch(`/api/projects/${encodeURIComponent(id)}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name }),
    });
    if (!res.ok) {
      const body = await res.json().catch(() => null);
      return (body as { error?: string } | null)?.error ?? 'Rename failed.';
    }
    return null;
  } catch {
    return 'Could not reach the daemon.';
  }
}

export async function removeProject(id: string): Promise<string | null> {
  try {
    const res = await fetch(`/api/projects/${encodeURIComponent(id)}`, {
      method: 'DELETE',
    });
    if (!res.ok) {
      const body = await res.json().catch(() => null);
      return (body as { error?: string } | null)?.error ?? 'Delete failed.';
    }
    return null;
  } catch {
    return 'Could not reach the daemon.';
  }
}

export async function createWorkstream(
  projectId: string,
  data: NewWorkstreamData,
): Promise<Workstream | string | Refusal> {
  return postWorkstream(projectId, 'workstreams', data);
}

/**
 * Gives the project the workstream a new project comes with, in the project
 * folder itself; the daemon says what that is.
 */
export async function addDefaultWorkstream(
  projectId: string,
): Promise<Workstream | string> {
  const result = await postWorkstream(projectId, 'default-workstream');
  return refusalMessage(result);
}

/**
 * Gives the project a workstream in the untracked git worktree at `path`; the
 * daemon refuses a path that is not one (any more).
 */
export async function addUntrackedWorktree(
  projectId: string,
  path: string,
): Promise<Workstream | string> {
  const result = await postWorkstream(projectId, 'worktree-workstreams', {
    path,
  });
  return refusalMessage(result);
}

/** A refusal as the message it carries, which is all these callers show. */
export function refusalMessage<T extends object>(
  result: T | string | Refusal,
): T | string {
  return typeof result !== 'string' && 'message' in result
    ? result.message
    : result;
}

/** POSTs `body`, if any, to a project's workstream-creating `path`. */
async function postWorkstream(
  projectId: string,
  path: string,
  body?: unknown,
): Promise<Workstream | string | Refusal> {
  try {
    const res = await fetch(
      `/api/projects/${encodeURIComponent(projectId)}/${path}`,
      body === undefined
        ? { method: 'POST' }
        : {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(body),
          },
    );
    if (!res.ok) return await refusalFrom(res, 'Create failed.');
    return (await res.json()) as Workstream;
  } catch {
    return 'Could not reach the daemon.';
  }
}

/**
 * What a create of `data` would derive for what it leaves blank, or null when
 * the daemon does not answer.
 */
export async function resolveWorkstreamDraft(
  projectId: string,
  data: NewWorkstreamData,
): Promise<WorkstreamDraft | null> {
  try {
    const res = await fetch(
      `/api/projects/${encodeURIComponent(projectId)}/workstream-draft`,
      {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(data),
      },
    );
    return res.ok ? ((await res.json()) as WorkstreamDraft) : null;
  } catch {
    return null;
  }
}

/**
 * Outcome of a delete attempt. `busy` means the daemon refused because sessions
 * are still busy: not a failure to report, but a decision to put back to the
 * user, who can confirm and have it re-issued with force.
 */
type DeleteOutcome =
  | { status: 'ok' }
  | { status: 'error'; message: string }
  | { status: 'busy'; sessions: BusySession[] };

export async function deleteWorkstream(
  projectId: string,
  workstreamId: string,
  opts?: DeleteWorkstreamOptions,
  force = false,
): Promise<DeleteOutcome> {
  const body = opts || force ? { ...opts, force } : undefined;
  try {
    const res = await fetch(
      `/api/projects/${encodeURIComponent(projectId)}/workstreams/${encodeURIComponent(workstreamId)}`,
      {
        method: 'DELETE',
        headers: body ? { 'Content-Type': 'application/json' } : undefined,
        body: body ? JSON.stringify(body) : undefined,
      },
    );
    if (!res.ok) {
      const payload = (await res.json().catch(() => null)) as {
        error?: string;
        reason?: string;
        sessions?: BusySession[];
      } | null;
      if (res.status === 409 && payload?.reason === REASON_SESSIONS_RUNNING) {
        return { status: 'busy', sessions: payload.sessions ?? [] };
      }
      return { status: 'error', message: payload?.error ?? 'Delete failed.' };
    }
    return { status: 'ok' };
  } catch {
    return { status: 'error', message: 'Could not reach the daemon.' };
  }
}

export async function renameWorkstream(
  projectId: string,
  workstreamId: string,
  name: string,
): Promise<string | null> {
  try {
    const res = await fetch(
      `/api/projects/${encodeURIComponent(projectId)}/workstreams/${encodeURIComponent(workstreamId)}`,
      {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name }),
      },
    );
    if (!res.ok) {
      const body = (await res
        .json()
        .catch(() => null)) as DaemonErrorBody | null;
      return body?.fields?.['/name'] ?? body?.error ?? 'Rename failed.';
    }
    return null;
  } catch {
    return 'Could not reach the daemon.';
  }
}

export async function openSession(
  projectId: string,
  workstreamId: string,
  sessionId: string,
): Promise<string | null> {
  try {
    const res = await fetch(
      `/api/projects/${encodeURIComponent(projectId)}/workstreams/${encodeURIComponent(workstreamId)}/sessions/${encodeURIComponent(sessionId)}/open`,
      {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ mode: 'terminal' }),
      },
    );
    if (!res.ok) {
      const body = await res.json().catch(() => null);
      return (body as { error?: string } | null)?.error ?? 'Open failed.';
    }
    return null;
  } catch {
    return 'Could not reach the daemon.';
  }
}

export async function fetchWorkstreamProviders(
  projectId: string,
): Promise<Provider[]> {
  try {
    const res = await fetch(
      `/api/projects/${encodeURIComponent(projectId)}/workstream-providers`,
    );
    if (!res.ok) return [];
    const body = await res.json();
    return Array.isArray(body) ? (body as Provider[]) : [];
  } catch {
    return [];
  }
}

export async function fetchProviders(): Promise<Provider[]> {
  try {
    const res = await fetch('/api/providers');
    if (!res.ok) return [];
    const body = await res.json();
    return Array.isArray(body) ? (body as Provider[]) : [];
  } catch {
    return [];
  }
}

const workstreamBase = (projectId: string, workstreamId: string) =>
  `/api/projects/${encodeURIComponent(projectId)}/workstreams/${encodeURIComponent(workstreamId)}`;

const sessionsBase = (projectId: string, workstreamId: string) =>
  `${workstreamBase(projectId, workstreamId)}/sessions`;

/**
 * The daemon's {error, fields?} refusal as describeDaemonError words it, with
 * the reasons it gives by the pointer of the field each blames.
 */
async function refusalFrom(res: Response, fallback: string): Promise<Refusal> {
  const body = (await res.json().catch(() => null)) as DaemonErrorBody | null;
  return {
    message: describeDaemonError(body, fallback),
    fields: body?.fields ?? {},
  };
}

async function errorFrom(res: Response, fallback: string): Promise<string> {
  return (await refusalFrom(res, fallback)).message;
}

/**
 * Asks the daemon to open the workstream: `POST …/open` without a mode (the
 * default terminal hand-off), `POST …/open-with` with one. A result carrying a
 * URL is finished here by opening a browser tab.
 */
export async function openWorkstream(
  projectId: string,
  workstreamId: string,
  mode?: string,
  tab?: Window | null,
): Promise<string | null> {
  try {
    const base = workstreamBase(projectId, workstreamId);
    const res = mode
      ? await fetch(`${base}/open-with`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ mode }),
        })
      : await fetch(`${base}/open`, { method: 'POST' });
    if (!res.ok) {
      tab?.close();
      return await errorFrom(res, 'Open failed.');
    }
    const result = (await res.json().catch(() => null)) as OpenResult | null;
    const url = result?.data?.url;
    if (typeof url !== 'string' || !url) {
      tab?.close();
      return null;
    }
    // The tab was opened in the click handler, before this await, so the browser
    // still attributes it to the user's gesture. Without one — the desktop
    // shell's webview never hands us a window — openExternal asks the daemon to
    // give the URL to the host's browser instead. See open-external.ts.
    if (tab) {
      tab.location.href = url;
      return null;
    }
    return await openExternal(url);
  } catch {
    tab?.close();
    return 'Could not reach the daemon.';
  }
}

/**
 * Clears a workstream's status message on the daemon, which is what makes it a
 * dismissal rather than a hide: the clear broadcasts, and this browser drops the
 * message on the state push it triggers like any other change.
 */
export async function dismissWorkstreamStatus(
  projectId: string,
  workstreamId: string,
): Promise<string | null> {
  try {
    const res = await fetch(
      `${workstreamBase(projectId, workstreamId)}/status`,
      { method: 'DELETE' },
    );
    return res.ok
      ? null
      : await errorFrom(res, 'Could not dismiss the message.');
  } catch {
    return 'Could not reach the daemon.';
  }
}

/**
 * Asks the daemon to open this workstream's note. The daemon owns the whole
 * thing — where the file lives, creating it on first use, and which application
 * the desktop opens it with — so the UI only fires the request and shows what
 * came back. Both refusals are 409s the daemon has worded for the user: a
 * workstream with nothing on disk, and a host with no desktop opener installed.
 */
export async function openWorkstreamNote(
  projectId: string,
  workstreamId: string,
): Promise<string | null> {
  try {
    const res = await fetch(`${workstreamBase(projectId, workstreamId)}/note`, {
      method: 'POST',
    });
    return res.ok ? null : await errorFrom(res, 'Could not open the note.');
  } catch {
    return 'Could not reach the daemon.';
  }
}

/**
 * What the daemon answers a GET of `url` with. Throws what to tell the user
 * when it cannot be reached or fails: its reason, else `fallback`.
 */
async function getJSON(url: string, fallback: string): Promise<unknown> {
  let res: Response;
  try {
    res = await fetch(url);
  } catch {
    throw new Error('Could not reach the daemon.');
  }
  if (!res.ok) throw new Error(await errorFrom(res, fallback));
  try {
    return await res.json();
  } catch {
    throw new Error(fallback);
  }
}

/**
 * The session types the workstream offers, by provider. Throws when the
 * daemon cannot be reached or fails otherwise.
 */
export async function fetchSessionSchemas(
  projectId: string,
  workstreamId: string,
): Promise<SessionProviderSchemas[]> {
  const body = await getJSON(
    `${workstreamBase(projectId, workstreamId)}/session-schemas`,
    'Could not load the session types.',
  );
  return Array.isArray(body) ? (body as SessionProviderSchemas[]) : [];
}

export async function createSession(
  projectId: string,
  workstreamId: string,
  data: NewSessionData,
): Promise<Session | string | Refusal> {
  try {
    const res = await fetch(sessionsBase(projectId, workstreamId), {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(data),
    });
    if (!res.ok) return await refusalFrom(res, 'Create failed.');
    return (await res.json()) as Session;
  } catch {
    return 'Could not reach the daemon.';
  }
}

/**
 * What the session was last started with, and the session option a restart
 * starts it as, with its schema. Throws when the daemon cannot be reached, or
 * refuses the session or fails otherwise, with the reason it gives.
 */
export async function fetchLaunchConfig(
  projectId: string,
  workstreamId: string,
  sessionId: string,
): Promise<LaunchConfig> {
  return (await getJSON(
    `${sessionsBase(projectId, workstreamId)}/${encodeURIComponent(sessionId)}/launch-config`,
    'Could not load the session settings.',
  )) as LaunchConfig;
}

/**
 * Restarts a session with `data.config`. The restart itself runs after the
 * daemon answers, and shows on the session row as any start does; a refusal
 * (a config the schema does not allow, a provider that cannot restart) comes
 * back with the reasons it gives by field.
 */
export async function restartSession(
  projectId: string,
  workstreamId: string,
  sessionId: string,
  data: RestartSessionData,
): Promise<Session | string | Refusal> {
  try {
    const res = await fetch(
      `${sessionsBase(projectId, workstreamId)}/${encodeURIComponent(sessionId)}/restart`,
      {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(data),
      },
    );
    if (!res.ok) return await refusalFrom(res, 'Restart failed.');
    return (await res.json().catch(() => ({}))) as Session;
  } catch {
    return 'Could not reach the daemon.';
  }
}

export async function sessionAction(
  projectId: string,
  workstreamId: string,
  sessionId: string,
  suffix: string,
  method: string,
): Promise<string | null> {
  try {
    const res = await fetch(
      `${sessionsBase(projectId, workstreamId)}/${encodeURIComponent(sessionId)}${suffix}`,
      { method },
    );
    return res.ok ? null : await errorFrom(res, 'Action failed.');
  } catch {
    return 'Could not reach the daemon.';
  }
}

/**
 * Renames a session, or — for an empty title — asks the daemon to drop the custom
 * name so the provider's own one shows again. Two requests rather than one
 * because they are two operations: the daemon rejects an empty rename precisely so
 * a client cannot clear a name by accident, and clearing has its own subresource.
 */
export async function renameSession(
  projectId: string,
  workstreamId: string,
  sessionId: string,
  title: string,
): Promise<string | null> {
  const url = `${sessionsBase(projectId, workstreamId)}/${encodeURIComponent(sessionId)}`;
  try {
    const res = title
      ? await fetch(url, {
          method: 'PATCH',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ title }),
        })
      : await fetch(`${url}/title`, { method: 'DELETE' });
    return res.ok ? null : await errorFrom(res, 'Rename failed.');
  } catch {
    return 'Could not reach the daemon.';
  }
}

export async function promptSession(
  projectId: string,
  workstreamId: string,
  sessionId: string,
  text: string,
): Promise<string | null> {
  try {
    const res = await fetch(
      `${sessionsBase(projectId, workstreamId)}/${encodeURIComponent(sessionId)}/prompt`,
      {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ text }),
      },
    );
    return res.ok ? null : await errorFrom(res, 'Send failed.');
  } catch {
    return 'Could not reach the daemon.';
  }
}

export async function sendSessionKey(
  projectId: string,
  workstreamId: string,
  sessionId: string,
  key: string,
): Promise<string | null> {
  try {
    const res = await fetch(
      `${sessionsBase(projectId, workstreamId)}/${encodeURIComponent(sessionId)}/key`,
      {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ key }),
      },
    );
    return res.ok ? null : await errorFrom(res, 'Key send failed.');
  } catch {
    return 'Could not reach the daemon.';
  }
}
