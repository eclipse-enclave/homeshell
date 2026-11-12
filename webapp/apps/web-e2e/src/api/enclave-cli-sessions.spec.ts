import { expect, test } from '@playwright/test';
import {
  existsSync,
  mkdirSync,
  readFileSync,
  realpathSync,
  rmSync,
} from 'node:fs';
import { join } from 'node:path';
import {
  afterChecks,
  attaches,
  buildEnclaveFixture,
  calls,
  cliContainer,
  isolatedDaemon,
  projectAndWorkstream,
  removeEnclaveFixture,
  rowWhere,
} from '../enclave-helpers';
import { disposeIsolatedDaemons } from '../isolated-daemon';
import { git } from '../git-repo';
import { addEmptyProject } from '../projects';
import {
  addUntrackedWorktree,
  pollFor,
  sessionsOf,
  untrackedOf,
} from './workstream-helpers';

const background = 'enclave-codex-0123456789ab-bg';
const foreground = 'enclave-codex-0123456789ab';

test.describe('Enclave sessions started with the CLI', () => {
  test.skip(process.platform === 'win32', 'Host bridge requires Unix PTYs');
  test.beforeAll(buildEnclaveFixture);
  test.afterEach(disposeIsolatedDaemons);
  test.afterAll(removeEnclaveFixture);

  // A daemon with a project, and a container started "in a terminal" in it.
  async function setUp() {
    const daemon = await isolatedDaemon({ inventoryMs: 200 });
    const ws = await projectAndWorkstream(daemon);
    const dir = join(daemon.root, 'project');
    const row = (id: string) => ws.session(id);
    return { daemon, ws, dir, row };
  }

  test('adopts a background CLI session and attaches it once, across a daemon restart', async () => {
    test.setTimeout(60_000);
    const { daemon, ws, dir } = await setUp();
    try {
      cliContainer(daemon.fixture, {
        name: background,
        session: 'bg',
        dir,
        background: true,
      });
      const adopted = await rowWhere(ws, background, 'idle');
      expect(adopted.provider).toBe('enclave');
      expect(adopted.title).toBe('Codex');
      expect(attaches(daemon.fixture)).toHaveLength(1);

      await daemon.restart();
      await rowWhere(ws, background, 'idle');
      // Several inventory checks after the restart it is still the one attach.
      await afterChecks(daemon.fixture);
      expect(attaches(daemon.fixture)).toHaveLength(1);
    } finally {
      await ws.remove();
      await daemon.stop();
    }
  });

  test('lists a foreground CLI session with its limits', async () => {
    test.setTimeout(60_000);
    const { daemon, ws, dir } = await setUp();
    try {
      cliContainer(daemon.fixture, {
        name: foreground,
        session: '',
        dir,
        background: false,
      });
      const listed = await rowWhere(ws, foreground, 'running');
      await afterChecks(daemon.fixture);
      expect(listed.capabilities).toEqual({
        live: false,
        takeOver: true,
        stopWarning:
          'It runs in a terminal; stopping it ends the session there too.',
      });
      expect(attaches(daemon.fixture)).toEqual([]);

      const prompt = await daemon.api.post(`${ws.base}/${foreground}/prompt`, {
        data: { text: 'hello' },
      });
      expect(prompt.status()).toBe(501);

      const opened = await daemon.api.post(`${ws.base}/${foreground}/open`, {
        data: { mode: 'terminal' },
      });
      expect(opened.status()).toBe(200);
      const recorded = join(daemon.fixture, 'terminal.json');
      await expect.poll(() => existsSync(recorded)).toBe(true);
      const terminal = JSON.parse(readFileSync(recorded, 'utf8')) as string[];
      expect(terminal.join(' ')).toContain('exec');
      expect(terminal.join(' ')).toContain(foreground);
    } finally {
      await ws.remove();
      await daemon.stop();
    }
  });

  test('a foreground session that ends becomes an ended row that continues in HomeShell', async () => {
    test.setTimeout(60_000);
    const { daemon, ws, dir, row } = await setUp();
    try {
      cliContainer(daemon.fixture, {
        name: foreground,
        session: '',
        dir,
        background: false,
        options: { allow_domains: ['y.org'] },
      });
      await rowWhere(ws, foreground, 'running');
      rmSync(join(daemon.fixture, `container-${foreground}.json`));
      const ended = await rowWhere(ws, foreground, 'done');
      expect(ended.capabilities).toBeUndefined();

      const started = await daemon.api.post(`${ws.base}/${foreground}/start`);
      expect(started.ok()).toBe(true);
      const continued = await pollFor('the continued session', async () =>
        (await ws.sessions()).find(
          (s) => s.id !== foreground && s.state === 'idle',
        ),
      );
      expect(continued.id).toMatch(/-hs-/);
      expect(continued.restartable).toBe(true);
      expect(continued.capabilities).toBeUndefined();
      expect(await row(foreground)).toBeUndefined();

      const last = calls(daemon.fixture, 'continue').at(-1) ?? [];
      expect(last).toContain('--background');
      expect(last[last.indexOf('--name') + 1]).toMatch(/^hs-/);

      const config = await daemon.api.get(
        `${ws.base}/${continued.id}/launch-config`,
      );
      expect((await config.json()).config).toEqual({
        enclave: { allowDomains: ['y.org'] },
      });
    } finally {
      await ws.remove();
      await daemon.stop();
    }
  });

  test("skips another daemon's and IDE containers", async () => {
    test.setTimeout(60_000);
    const { daemon, ws, dir } = await setUp();
    try {
      cliContainer(daemon.fixture, {
        name: 'enclave-codex-0123456789ab-hs-other',
        session: 'hs-other',
        dir,
        background: true,
      });
      cliContainer(daemon.fixture, {
        name: 'enclave-theia-0123456789ab',
        session: '',
        dir,
        background: true,
        tool: 'theia',
      });
      // A third one, adopted, shows the checks have run.
      cliContainer(daemon.fixture, {
        name: background,
        session: 'bg',
        dir,
        background: true,
      });
      await rowWhere(ws, background, 'idle');
      await afterChecks(daemon.fixture);
      expect((await ws.sessions()).map((s) => s.id)).toEqual([background]);
      expect(attaches(daemon.fixture)).toHaveLength(1);
    } finally {
      await ws.remove();
      await daemon.stop();
    }
  });

  test('a CLI session in an untracked worktree is counted on its suggestion and shows once it is added', async () => {
    test.setTimeout(60_000);
    const daemon = await isolatedDaemon({ inventoryMs: 200 });
    try {
      const repo = join(daemon.root, 'repo');
      mkdirSync(repo);
      await git(repo, 'init', '-q', '-b', 'main');
      await git(repo, 'commit', '-q', '--allow-empty', '-m', 'initial');
      const worktree = join(daemon.root, 'wt');
      await git(repo, 'worktree', 'add', '-q', '-b', 'wt', worktree);
      const projectId = await addEmptyProject(daemon.api, repo);
      const path = realpathSync(worktree);
      cliContainer(daemon.fixture, {
        name: background,
        session: 'bg',
        dir: path,
        projectDir: realpathSync(repo),
        background: true,
      });

      await pollFor('the session to be counted', async () =>
        (await untrackedOf(daemon.api, projectId)).find(
          (w) => w.path === path && w.sessionCount === 1,
        ),
      );
      const added = await addUntrackedWorktree(daemon.api, projectId, path);
      expect(added.status(), await added.text()).toBe(201);
      const workstreamId = (await added.json()).id;
      const shown = await pollFor('the session to show', async () =>
        (await sessionsOf(daemon.api, projectId, workstreamId)).find(
          (s) => s.id === background,
        ),
      );
      expect(shown.provider).toBe('enclave');
      expect(attaches(daemon.fixture)).toHaveLength(1);

      const base = `/api/projects/${projectId}/workstreams/${workstreamId}/sessions`;
      await daemon.api.delete(`${base}/${background}`);
      await daemon.api.delete(`/api/projects/${projectId}`);
    } finally {
      await daemon.stop();
    }
  });
});
