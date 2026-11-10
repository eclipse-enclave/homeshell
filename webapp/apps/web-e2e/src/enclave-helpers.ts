import {
  expect,
  request as http,
  type APIRequestContext,
} from '@playwright/test';
import { execFile, spawn, type ChildProcess } from 'node:child_process';
import {
  copyFileSync,
  existsSync,
  mkdirSync,
  readFileSync,
  readdirSync,
  rmSync,
  writeFileSync,
} from 'node:fs';
import { join } from 'node:path';
import { promisify } from 'node:util';
import { awaitRealWorkstream, sessionsOf } from './api/workstream-helpers';
import { homeshellBin as binary, scratchDir } from './e2e-env';
import { isolatedHome, runtimeEnv, trackIsolatedHome } from './isolated-daemon';

const exec = promisify(execFile);
/** The fixture `enclave` CLI and terminal, first on the daemon's PATH. */
export const binaries = scratchDir('encl-bin-');

// Public HTTP against a real daemon, bridge and PTY. The external Enclave CLI
// peer makes container lifetime deterministic without requiring Docker in CI.
// It rejects the old monitor/status/exec path. Real Docker compatibility is a
// separate integration check, not something this peer can establish.
export async function isolatedDaemon() {
  const root = isolatedHome('encl');
  const fixture = join(root, 'fixture');
  mkdirSync(fixture);
  writeFileSync(
    join(root, 'settings.json'),
    JSON.stringify({
      schemaVersion: 2,
      settings: { notifications: { style: 'none' } },
      logLevel: 'warn',
    }),
  );
  let daemon: ChildProcess | undefined;
  const dispose = trackIsolatedHome(root, () => daemon);
  let api: APIRequestContext;
  const logs: string[] = [];
  async function start() {
    daemon = spawn(
      binary,
      ['serve', '-demo=false', `-home-dir=${root}`, '-addr=127.0.0.1:0'],
      {
        stdio: ['ignore', 'ignore', 'pipe'],
        env: {
          ...process.env,
          PATH: `${binaries}:/usr/bin:/bin`,
          ...runtimeEnv(root),
          HOMESHELL_ENCLAVE_FIXTURE: fixture,
          TERMINAL: join(binaries, 'fixture-terminal'),
          HOMESHELL_API_TOKEN: 'enclave-bridge-api',
          HOMESHELL_UPDATE_BASE_URL: 'http://127.0.0.1:9/',
        },
      },
    );
    daemon.stderr?.on('data', (data) => logs.push(String(data)));
    await expect
      .poll(() => existsSync(join(root, 'daemon.json')), {
        timeout: 15_000,
        message: logs.join(''),
      })
      .toBe(true);
    const state = JSON.parse(readFileSync(join(root, 'daemon.json'), 'utf8'));
    api = await http.newContext({
      baseURL: `http://${state.address}`,
      extraHTTPHeaders: { Authorization: `Bearer ${state.token}` },
    });
    return api;
  }
  async function stopDaemon() {
    await exec(binary, ['stop', `-home-dir=${root}`], { timeout: 15_000 });
    await expect
      .poll(() => daemon?.exitCode, { timeout: 10_000 })
      .not.toBeNull();
    await api.dispose();
  }
  await start();
  return {
    root,
    fixture,
    get url() {
      return `http://${JSON.parse(readFileSync(join(root, 'daemon.json'), 'utf8')).address}`;
    },
    token: 'enclave-bridge-api',
    get api() {
      return api;
    },
    restart: async () => {
      await stopDaemon();
      await start();
    },
    stop: async () => {
      // The shared teardown: bridges first, then the daemon, then the home.
      try {
        await dispose();
      } finally {
        await api.dispose();
      }
    },
  };
}

// Takes the daemon, not its client: restart() replaces daemon.api.
export async function projectAndWorkstream(daemon: {
  api: APIRequestContext;
  root: string;
}) {
  const path = join(daemon.root, 'project');
  mkdirSync(path);
  const response = await daemon.api.post('/api/projects', { data: { path } });
  expect(response.status()).toBe(201);
  const id = (await response.json()).id as string;
  const created = await daemon.api.post(`/api/projects/${id}/workstreams`, {
    data: {
      providerId: 'core',
      name: 'enclave',
      config: { strategy: 'in-place' },
    },
  });
  expect(created.status()).toBe(201);
  const ws = await awaitRealWorkstream(daemon.api, id);
  const sessions = () => sessionsOf(daemon.api, id, ws.id);
  return {
    id,
    ws,
    base: `/api/projects/${id}/workstreams/${ws.id}/sessions`,
    sessions,
    session: async (sessionId: string) =>
      (await sessions()).find((s) => s.id === sessionId),
  };
}

// Answers 202 before the container is up; poll for the session's state.
export async function createEnclaveSession(
  api: APIRequestContext,
  base: string,
) {
  const response = await api.post(base, {
    data: { providerId: 'enclave', optionId: 'codex', config: {} },
  });
  expect(response.status()).toBe(202);
}

export function calls(fixture: string, ...commands: string[]): string[][] {
  const all = readFileSync(join(fixture, 'calls'), 'utf8')
    .trim()
    .split('\n')
    .filter(Boolean)
    .map((line) => JSON.parse(line) as string[]);
  return commands.length
    ? all.filter((argv) => commands.includes(argv[0]))
    : all;
}

// Marker files the fixture CLI keeps per live container and gateway.
export function fixtureFiles(fixture: string, ...prefixes: string[]) {
  return readdirSync(fixture).filter((name) =>
    prefixes.some((prefix) => name.startsWith(prefix)),
  );
}

export function attachPid(fixture: string, sessionId: string) {
  return Number(readFileSync(join(fixture, `attach-pid-${sessionId}`), 'utf8'));
}

function nodeExecutable(name: string, script: string) {
  // Use Playwright's own Node binary, even with the daemon's restricted PATH.
  // Shell quoting also supports installation paths containing spaces or quotes.
  const quote = (value: string) => `'${value.replaceAll("'", "'\\''")}'`;
  writeFileSync(
    join(binaries, name),
    `#!/bin/sh\nexec ${quote(process.execPath)} ${quote(script)} "$@"\n`,
    { mode: 0o755 },
  );
}

export function buildEnclaveFixture() {
  // Another spec file's removeEnclaveFixture may have run in this worker.
  mkdirSync(binaries, { recursive: true });
  const cliScript = join(binaries, 'enclave-cli.cjs');
  copyFileSync(join(__dirname, 'api/enclave-cli.fixture.cjs'), cliScript);
  nodeExecutable('enclave', cliScript);
  const terminalScript = join(binaries, 'fixture-terminal.cjs');
  writeFileSync(
    terminalScript,
    `const fs = require('node:fs');
const path = require('node:path');
fs.writeFileSync(path.join(process.env.HOMESHELL_ENCLAVE_FIXTURE, 'terminal.json'), JSON.stringify(process.argv.slice(2)));
`,
  );
  nodeExecutable('fixture-terminal', terminalScript);
}

export function removeEnclaveFixture() {
  rmSync(binaries, { recursive: true, force: true });
}

// Capture public SSE snapshots, including preview changes, so responsiveness
// assertions cannot pass merely because an unrelated HTTP handler is available.
export async function observeSession(
  daemon: Awaited<ReturnType<typeof isolatedDaemon>>,
  id: string,
) {
  const abort = new AbortController();
  const frames: { state: string; preview?: string }[] = [];
  const response = await fetch(`${daemon.url}/api/events`, {
    headers: { Authorization: `Bearer ${daemon.token}` },
    signal: abort.signal,
  });
  expect(response.status).toBe(200);
  const done = (async () => {
    const decoder = new TextDecoder();
    let buffer = '';
    if (!response.body) throw new Error('SSE response has no body');
    for await (const bytes of response.body) {
      buffer += decoder.decode(bytes, { stream: true });
      let end: number;
      while ((end = buffer.indexOf('\n\n')) >= 0) {
        const frame = buffer.slice(0, end);
        buffer = buffer.slice(end + 2);
        if (!frame.startsWith('event: state\n')) continue;
        const data = frame
          .split('\n')
          .find((line) => line.startsWith('data: '));
        if (!data) continue;
        type Listed = { id: string; state: string; preview?: string };
        const snapshot = JSON.parse(data.slice(6)) as {
          projects: { workstreams: { sessions: Listed[] }[] }[];
        };
        const current = snapshot.projects
          .flatMap((p) => p.workstreams)
          .flatMap((w) => w.sessions)
          .find((s) => s.id === id);
        if (current) frames.push(current);
      }
    }
  })().catch((error) => {
    if (!abort.signal.aborted) throw error;
  });
  return {
    frames,
    close: async () => {
      abort.abort();
      await done;
    },
  };
}
