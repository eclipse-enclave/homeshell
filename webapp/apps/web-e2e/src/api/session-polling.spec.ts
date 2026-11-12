import {
  expect,
  request as http,
  test,
  type APIRequestContext,
} from '@playwright/test';
import { spawn } from 'node:child_process';
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { homeshellBin } from '../e2e-env';
import {
  disposeIsolatedDaemons,
  isolatedHome,
  runtimeEnv,
  trackIsolatedHome,
} from '../isolated-daemon';
import { awaitRealWorkstream, pollFor, sessionsOf } from './workstream-helpers';

type Daemon = Awaited<ReturnType<typeof startDaemon>>;

// These tests use public HTTP/SSE and a real bridge/PTY, not the demo provider.
// The scripted agent is an external terminal peer: no daemon internals or
// persisted provider records are imported or manufactured by the tests.
async function startDaemon({
  version = 1,
  interval,
}: { version?: number; interval?: number } = {}) {
  const root = isolatedHome('poll');
  writeFileSync(
    join(root, 'settings.json'),
    JSON.stringify({
      schemaVersion: version,
      logLevel: 'warn',
      settings: {
        providers:
          interval === undefined ? {} : { refreshIntervalSec: interval },
        sessions: { previewLines: 7 },
        notifications: { style: 'none' },
      },
    }),
  );
  const daemon = spawn(
    homeshellBin,
    ['serve', '-demo=false', `-home-dir=${root}`, '-addr=127.0.0.1:0'],
    {
      stdio: 'ignore',
      env: {
        ...process.env,
        PATH: `${dirname(homeshellBin)}:/usr/bin:/bin`,
        ...runtimeEnv(root),
        HOMESHELL_API_TOKEN: 'isolated-polling-test',
        HOMESHELL_UPDATE_BASE_URL: 'http://127.0.0.1:9/',
      },
    },
  );
  let api: APIRequestContext | undefined;
  const dispose = trackIsolatedHome(root, () => daemon);
  const stop = async () => {
    try {
      await dispose();
    } finally {
      await api?.dispose();
    }
  };
  try {
    await expect
      .poll(() => existsSync(join(root, 'daemon.json')), { timeout: 15_000 })
      .toBe(true);
    const state = JSON.parse(readFileSync(join(root, 'daemon.json'), 'utf8'));
    const url = `http://${state.address}`;
    api = await http.newContext({
      baseURL: url,
      extraHTTPHeaders: { Authorization: `Bearer ${state.token}` },
    });
    return { api, root, url, token: state.token as string, stop };
  } catch (error) {
    await stop();
    throw error;
  }
}

interface Frame {
  state: string;
  preview: string;
}

// Redraws its screen on each prompt.
const redrawingAgent = `#!/bin/sh
printf '\\033]0;⠋\\007\\033[2J\\033[Hworking output\\nesc to cancel\\n'
while IFS= read -r command; do
  case "$command" in
    work) printf '\\033]0;⠋\\007\\033[2J\\033[Hresumed output\\nesc to cancel\\n' ;;
    pause) printf '\\033]0;\\007\\033[2J\\033[Hbrief pause\\n' ;;
    idle) printf '\\033]0;\\007\\033[2J\\033[Hfinished output\\n' ;;
    wait) printf '\\033]0;Action Required\\007\\033[2J\\033[Hpermission request\\n│ Allow execution\\n' ;;
  esac
done
`;

// Asks for the device attributes before drawing, as agents do at startup, then
// draws a wide emoji and a character placed by column after it.
const probingAgent = `#!/bin/sh
stty raw -echo
printf '\\033[c'
reply=$(head -c 9 | tr '\\033' E)
stty sane
printf '\\033[2J\\033[Hgot %s\\n✨\\033[2;4Hx\\n' "$reply"
exec cat >/dev/null
`;

// Exits on its own with a last word on the first prompt.
const exitingAgent = `#!/bin/sh
printf 'ready\\n'
read -r command
printf '\\033[2J\\033[Hgoodbye: unsaved work lost\\n'
exit 3
`;

// Logs each launch's arguments beside itself, then quits on the first line it
// reads, the way /quit ends an agent on its own.
const quittingAgent = `#!/bin/sh
echo "$*" >> "$0.launches"
printf 'ready\\n'
read -r command
printf '\\033[2J\\033[Hbye\\n'
exit 0
`;

// Logs each launch like quittingAgent, but is killed outright on the first line,
// the way an OOM kill or crash ends an agent the user did not quit.
const killedAgent = `#!/bin/sh
echo "$*" >> "$0.launches"
printf 'ready\\n'
read -r command
kill -9 $$
`;

const live = (state: string) => state !== 'starting' && state !== 'stopped';

// Starts a local session of a scripted agent, waits until it is in a state
// ready accepts, and records every SSE frame of that session.
async function startScriptedSession(
  daemon: Daemon,
  script = redrawingAgent,
  ready: (state: string) => boolean = (state) => state === 'running',
  agentFields: Record<string, unknown> = {},
) {
  const projectDir = join(daemon.root, 'project');
  mkdirSync(projectDir);
  const agent = join(daemon.root, 'scripted-agent');
  writeFileSync(agent, script, { mode: 0o755 });
  const agentResponse = await daemon.api.post('/api/agents', {
    data: {
      id: 'polling-agent',
      binary: agent,
      display: 'Polling agent',
      ruleSetId: 'gemini',
      ...agentFields,
    },
  });
  expect(agentResponse.status()).toBe(201);
  const project = await daemon.api.post('/api/projects', {
    data: { path: projectDir },
  });
  expect(project.status()).toBe(201);
  const projectId = (await project.json()).id as string;
  const abort = new AbortController();
  let eventsDone: Promise<void> | undefined;
  const close = async () => {
    abort.abort();
    await eventsDone;
    await daemon.api.delete(`/api/projects/${projectId}`);
  };
  try {
    const created = await daemon.api.post(
      `/api/projects/${projectId}/workstreams`,
      {
        data: {
          providerId: 'core',
          name: 'polling',
          config: { strategy: 'in-place' },
        },
      },
    );
    expect(created.status()).toBe(201);
    const ws = await awaitRealWorkstream(daemon.api, projectId);
    const base = `/api/projects/${projectId}/workstreams/${ws.id}/sessions`;
    expect(
      (
        await daemon.api.post(base, {
          data: { providerId: 'local', optionId: 'polling-agent', config: {} },
        })
      ).status(),
    ).toBe(202);
    const session = await pollFor('ready local session', async () =>
      (await sessionsOf(daemon.api, projectId, ws.id)).find(
        (s) => s.provider === 'local' && ready(s.state),
      ),
    );
    const frames: Frame[] = [];
    const response = await fetch(`${daemon.url}/api/events`, {
      headers: { Authorization: `Bearer ${daemon.token}` },
      signal: abort.signal,
    });
    expect(response.status).toBe(200);
    eventsDone = (async () => {
      const decoder = new TextDecoder();
      let buffer = '';
      const body = response.body;
      if (!body) throw new Error('SSE response has no body');
      for await (const bytes of body) {
        buffer += decoder.decode(bytes, { stream: true });
        let end: number;
        while ((end = buffer.indexOf('\n\n')) >= 0) {
          const frame = buffer.slice(0, end);
          buffer = buffer.slice(end + 2);
          const data = frame
            .split('\n')
            .find((line) => line.startsWith('data: '));
          if (!data) continue;
          const state = JSON.parse(data.slice(6));
          type Listed = { id: string; state: string; preview?: string };
          const current = (state.projects ?? [])
            .flatMap(
              (p: { workstreams: { sessions?: Listed[] }[] }) => p.workstreams,
            )
            .flatMap((w: { sessions?: Listed[] }) => w.sessions ?? [])
            .find((s: Listed) => s.id === session.id);
          if (current)
            frames.push({
              state: current.state,
              preview: current.preview ?? '',
            });
        }
      }
    })().catch((error) => {
      if (!abort.signal.aborted) throw error;
    });
    const prompt = async (text: string) =>
      expect(
        (
          await daemon.api.post(`${base}/${session.id}/prompt`, {
            data: { text },
          })
        ).status(),
      ).toBe(204);
    const listed = async () =>
      (await sessionsOf(daemon.api, projectId, ws.id)).find(
        (s) => s.id === session.id,
      );
    const frame = (
      what: string,
      match: (f: Frame) => boolean,
      timeoutMs: number,
    ) => pollFor(what, async () => frames.find(match), timeoutMs);
    const start = async () =>
      expect(
        (await daemon.api.post(`${base}/${session.id}/start`)).status(),
      ).toBe(204);
    return { script: agent, frames, prompt, listed, frame, start, close };
  } catch (error) {
    await close();
    throw error;
  }
}

test.describe('fast session polling', () => {
  test.skip(
    process.platform === 'win32',
    'Local sessions require the Unix bridge',
  );

  test.afterEach(disposeIsolatedDaemons);

  test('migrates old defaults once and preserves custom and newly chosen intervals', async () => {
    test.setTimeout(60_000);
    for (const [version, saved, expected] of [
      [1, 5, 0.5],
      [1, 0.25, 0.25],
      [2, 5, 5],
    ]) {
      const daemon = await startDaemon({ version, interval: saved });
      try {
        const settings = await (await daemon.api.get('/api/settings')).json();
        expect(settings.providers.refreshIntervalSec).toBe(expected);
        expect(settings.sessions.previewLines).toBe(7);
        const readDisk = () =>
          JSON.parse(readFileSync(join(daemon.root, 'settings.json'), 'utf8'));
        // Migrated in memory; the next save writes the new schema.
        expect(readDisk().schemaVersion).toBe(version);
        settings.providers.refreshIntervalSec = 0.3;
        const put = await daemon.api.put('/api/settings', { data: settings });
        expect(put.status()).toBe(200);
        const disk = readDisk();
        expect(disk.schemaVersion).toBe(2);
        expect(disk.logLevel).toBe('warn');
        expect(
          (await (await daemon.api.get('/api/settings')).json()).providers
            .refreshIntervalSec,
        ).toBe(0.3);
      } finally {
        await daemon.stop();
      }
    }
  });

  test('publishes live states at the half-second default', async () => {
    test.setTimeout(60_000);
    const daemon = await startDaemon();
    try {
      const settings = await (await daemon.api.get('/api/settings')).json();
      expect(settings.providers.refreshIntervalSec).toBe(0.5);
      const agent = await startScriptedSession(daemon);
      try {
        await agent.prompt('wait');
        await agent.frame(
          'waiting',
          (f) =>
            f.state === 'waiting' && f.preview.includes('permission request'),
          2500,
        );
        const waiting = await agent.listed();
        expect(waiting?.observationData).toMatchObject({
          confident: true,
          visibleBlocker: true,
        });
        expect(waiting).not.toHaveProperty('providerData');
        await agent.prompt('work');
        await agent.frame(
          'running',
          (f) => f.state === 'running' && f.preview.includes('resumed output'),
          2500,
        );
        // Two ticks: the first idle capture is held, the second confirms it.
        await agent.prompt('idle');
        await agent.frame(
          'confirmed idle',
          (f) => f.state === 'idle' && f.preview.includes('finished output'),
          2500,
        );
      } finally {
        await agent.close();
      }
    } finally {
      await daemon.stop();
    }
  });

  test('holds a one-tick working-to-idle pause and publishes a sustained idle', async () => {
    test.setTimeout(60_000);
    // The hold confirms on the next tick, so 1 s ticks (the slowest that hold) give 1 s to end the pause.
    const interval = 1;
    const budget = interval * 2500;
    const daemon = await startDaemon({ version: 2, interval });
    try {
      const agent = await startScriptedSession(daemon);
      try {
        await agent.prompt('pause');
        await agent.frame(
          'brief pause',
          (f) => f.preview.includes('brief pause'),
          budget,
        );
        await agent.prompt('work');
        await agent.frame(
          'resumed output',
          (f) => f.preview.includes('resumed output'),
          budget,
        );
        expect(agent.frames.map((f) => f.state)).not.toContain('idle');

        await agent.prompt('idle');
        await agent.frame(
          'confirmed idle',
          (f) => f.state === 'idle',
          budget * 2,
        );
        const finished = agent.frames.filter((f) =>
          f.preview.includes('finished output'),
        );
        // The first idle capture publishes the new preview but keeps running.
        expect(finished[0].state).toBe('running');
        expect(finished.at(-1)?.state).toBe('idle');
        expect((await agent.listed())?.state).toBe('idle');
      } finally {
        await agent.close();
      }
    } finally {
      await daemon.stop();
    }
  });

  test('publishes idle on the first idle capture at a slow interval', async () => {
    test.setTimeout(60_000);
    // Above 1 s a hold would delay idle by a whole extra tick, so there is none.
    const interval = 2;
    const daemon = await startDaemon({ version: 2, interval });
    try {
      const agent = await startScriptedSession(daemon);
      try {
        await agent.prompt('idle');
        await agent.frame(
          'idle',
          (f) => f.state === 'idle',
          interval * 2500,
        );
        const finished = agent.frames.filter((f) =>
          f.preview.includes('finished output'),
        );
        expect(finished[0].state).toBe('idle');
      } finally {
        await agent.close();
      }
    } finally {
      await daemon.stop();
    }
  });

  test('answers an agent probing the terminal and draws wide characters two cells wide', async () => {
    test.setTimeout(60_000);
    const daemon = await startDaemon();
    try {
      const agent = await startScriptedSession(daemon, probingAgent, live);
      try {
        // No terminal is attached, so only the bridge can have answered.
        const drawn = await agent.frame(
          'reply drawn',
          (f) => f.preview.includes('got E[?62;22c'),
          2500,
        );
        expect(drawn.preview).toContain('✨ x');
      } finally {
        await agent.close();
      }
    } finally {
      await daemon.stop();
    }
  });

  test('stops a resumable session whose agent quit instead of relaunching it', async () => {
    test.setTimeout(60_000);
    const daemon = await startDaemon();
    try {
      const agent = await startScriptedSession(daemon, quittingAgent, live, {
        sessionIdFlag: '--session-id',
        resumeFlag: '--resume',
      });
      const launches = () =>
        readFileSync(`${agent.script}.launches`, 'utf8')
          .split('\n')
          .filter(Boolean);
      try {
        await agent.frame(
          'agent ready',
          (f) => f.preview.includes('ready'),
          2500,
        );
        await agent.prompt('/quit');
        await agent.frame('stopped', (f) => f.state === 'stopped', 2500);
        // A relaunch would have come on the next half-second tick.
        await new Promise((resolve) => setTimeout(resolve, 2000));
        expect((await agent.listed())?.state).toBe('stopped');
        expect(launches()).toHaveLength(1);

        const before = agent.frames.length;
        await agent.start();
        await pollFor(
          'resumed',
          async () =>
            agent.frames
              .slice(before)
              .find(
                (f) => f.state !== 'stopped' && f.preview.includes('ready'),
              ),
          5000,
        );
        expect(launches()).toHaveLength(2);
        expect(launches()[1]).toContain('--resume');
      } finally {
        await agent.close();
      }
    } finally {
      await daemon.stop();
    }
  });

  test('relaunches a resumable session whose agent was killed', async () => {
    test.setTimeout(60_000);
    const daemon = await startDaemon();
    try {
      const agent = await startScriptedSession(daemon, killedAgent, live, {
        sessionIdFlag: '--session-id',
        resumeFlag: '--resume',
      });
      const launches = () =>
        readFileSync(`${agent.script}.launches`, 'utf8')
          .split('\n')
          .filter(Boolean);
      try {
        await agent.frame(
          'agent ready',
          (f) => f.preview.includes('ready'),
          2500,
        );
        await agent.prompt('crash');
        await pollFor(
          'relaunched',
          async () => (launches().length === 2 ? true : undefined),
          5000,
        );
        expect((await agent.listed())?.state).not.toBe('stopped');
      } finally {
        await agent.close();
      }
    } finally {
      await daemon.stop();
    }
  });

  test('shows the last screen of an agent that exited on its own', async () => {
    test.setTimeout(60_000);
    const daemon = await startDaemon();
    try {
      const agent = await startScriptedSession(daemon, exitingAgent, live);
      try {
        await agent.frame(
          'agent ready',
          (f) => f.preview.includes('ready'),
          2500,
        );
        await agent.prompt('quit');
        const ended = await agent.frame(
          'stopped',
          (f) => f.state === 'stopped',
          2500,
        );
        expect(ended.preview).toContain('goodbye: unsaved work lost');
      } finally {
        await agent.close();
      }
    } finally {
      await daemon.stop();
    }
  });
});
