// The demo daemon's API, for seeding and for waiting on agents while recording.
import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
export const layout = JSON.parse(readFileSync(join(here, 'layout.json'), 'utf8'));
const TOKEN = process.env.HOMESHELL_API_TOKEN ?? 'demo-token';
let base = process.env.DEMO_URL ?? 'http://127.0.0.1:8431';

export const setBase = (url) => {
  base = url;
};
export const pause = (ms) => new Promise((r) => setTimeout(r, ms));

export async function api(method, path, body) {
  const res = await fetch(`${base}/api${path}`, {
    method,
    headers: { Authorization: `Bearer ${TOKEN}`, 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) throw new Error(`${method} ${path}: ${res.status} ${await res.text()}`);
  return res.status === 204 ? null : res.json().catch(() => null);
}

export const projects = async () => (await api('GET', '/projects')).projects;
export const project = async (name) => (await projects()).find((p) => p.name === name);
export const wsPath = (p, ws) => `/projects/${p.id}/workstreams/${ws.id}`;

export async function workstream(projectName, wsName) {
  const p = await project(projectName);
  const ws = p?.workstreams.find((w) => w.name === wsName);
  if (!ws) throw new Error(`no workstream ${projectName}/${wsName}`);
  return { p, ws };
}

/** Every session with its project and workstream names. */
export async function allSessions() {
  const out = [];
  for (const p of await projects())
    for (const w of p.workstreams) for (const s of w.sessions ?? []) out.push({ ...s, project: p.name, ws: w.name });
  return out;
}

/** Polls until `poll()` returns something truthy; throws `what` after `timeoutMs`. */
export async function until(what, poll, timeoutMs = 120_000, everyMs = 500) {
  const end = Date.now() + timeoutMs;
  for (;;) {
    const v = await poll();
    if (v) return v;
    if (Date.now() > end) throw new Error(`timed out after ${timeoutMs / 1000}s: ${what}`);
    await pause(everyMs);
  }
}

/** The session of `projectName/wsName` matching `pick` (default: the newest), once it is in `states`. */
export async function waitForSession(projectName, wsName, states, { timeoutMs = 300_000, pick } = {}) {
  let last;
  try {
    return await until(
      `${projectName}/${wsName} session in ${states}`,
      async () => {
        const list = (await allSessions()).filter((s) => s.project === projectName && s.ws === wsName && !s.placeholder);
        last = pick ? list.find(pick) : list.at(-1);
        return last && states.includes(last.state) ? last : null;
      },
      timeoutMs,
    );
  } catch (err) {
    err.message += `\nlast state: ${last?.state}\nlast preview:\n${last?.preview ?? '(none)'}`;
    throw err;
  }
}

/** Creates a worktree workstream named `name` and waits until it exists. */
export async function addWorktree(projectName, name) {
  const p = await project(projectName);
  if (!p.workstreams.some((w) => w.name === name))
    await api('POST', `/projects/${p.id}/workstreams`, { providerId: 'core', name, config: { strategy: 'worktree' } });
  return until(`worktree ${projectName}/${name}`, async () => {
    const ws = (await project(projectName)).workstreams.find((w) => w.name === name);
    if (ws?.status?.kind === 'error') throw new Error(`creating ${name}: ${ws.status.message}`);
    return ws && !ws.placeholder && ws.state !== 'creating' && !ws.id.startsWith('creating-') ? ws : null;
  });
}

/** Starts a Local System Claude Code session with `prompt` in `projectName/wsName`. */
export async function startClaude(projectName, wsName, prompt, config = {}) {
  const { p, ws } = await workstream(projectName, wsName);
  const config_ = { model: 'claude-sonnet-5', permissionMode: 'auto', prompt, ...config };
  await api('POST', `${wsPath(p, ws)}/sessions`, { providerId: 'local', optionId: 'claude', config: config_ });
}

export async function sessionAction(s, action, body) {
  const { p, ws } = await workstream(s.project, s.ws);
  return api('POST', `${wsPath(p, ws)}/sessions/${encodeURIComponent(s.id)}/${action}`, body);
}

/** Renames `s` in HomeShell, as a user does from the row's menu. */
export async function rename(s, title) {
  const { p, ws } = await workstream(s.project, s.ws);
  await api('PATCH', `${wsPath(p, ws)}/sessions/${encodeURIComponent(s.id)}`, { title });
}
