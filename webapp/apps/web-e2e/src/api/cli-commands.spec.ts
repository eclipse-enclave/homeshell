import { execFile, spawn } from 'node:child_process';
import { join } from 'node:path';
import { promisify } from 'node:util';
import type { APIRequestContext } from '@playwright/test';
import { expect, tempProjectDir, test } from '../fixtures';
import { isPlaceholderSession } from '../placeholders';
import { homeshellBin as binary, repoRoot, scratchDir } from '../e2e-env';
import { withSettingsLock } from '../settings-lock';

/**
 * Black-box coverage of the CLI's **data verbs** (`project`, `workstream`,
 * `session`, `provider`, `settings`, `config`, `events`, `version`) against the
 * live daemon this suite starts.
 *
 * The CLI is a client over the public API and finds the daemon through the
 * discovery state file, so these tests drive the real binary with the daemon's
 * `-home-dir` and never reach inside it — they survive a daemon rewrite exactly
 * as the rest of the api project does. `cli.spec.ts` covers dispatch, usage and
 * `--version`; this covers what the verbs actually do.
 *
 * The binary is exec'd directly rather than through `go run`, which masks the
 * exit code — and the exit codes are half of what is being asserted.
 */
const execFileAsync = promisify(execFile);

// The daemon under test publishes its state file here (see playwright.config.ts),
// so this is how the CLI discovers it.
const homeDirArg = `-home-dir=${join(repoRoot, 'db/e2e')}`;

/** The CLI's exit codes; scripts branch on these, so they are part of its contract. */
const EXIT = { ok: 0, failed: 1, usage: 2, noDaemon: 3 } as const;

interface CliResult {
  code: number;
  stdout: string;
  stderr: string;
}

async function cli(...args: string[]): Promise<CliResult> {
  try {
    const { stdout, stderr } = await execFileAsync(binary, args);
    return { code: 0, stdout, stderr };
  } catch (err) {
    const failure = err as { code?: number; stdout?: string; stderr?: string };
    return {
      code: failure.code ?? 1,
      stdout: failure.stdout ?? '',
      stderr: failure.stderr ?? '',
    };
  }
}

/** Runs a verb that must succeed, surfacing stderr in the failure when it does not. */
async function cliOk(...args: string[]): Promise<CliResult> {
  const result = await cli(...args);
  expect(
    result.code,
    `homeshell ${args.join(' ')} failed: ${result.stderr}`,
  ).toBe(EXIT.ok);
  return result;
}

async function cliJSON<T>(...args: string[]): Promise<T> {
  const { stdout } = await cliOk(...args, '--json');
  return JSON.parse(stdout) as T;
}

interface WorkstreamJSON {
  id: string;
  name: string;
  sessions?: { id: string; state: string }[];
}

/**
 * A project of its own, so concurrent specs never collide over one. Its
 * directory comes from the shared fixture, which removes the project and the
 * directory after the test even when an assertion failed before the test's own
 * `project remove` ran.
 */
async function freshProject(name: string): Promise<string> {
  const dir = await tempProjectDir(`hs-cli-${name}-`);
  const created = await cliJSON<{ id: string }>(
    'project',
    'add',
    dir,
    homeDirArg,
  );
  expect(created.id).toBeTruthy();
  return created.id;
}

/**
 * Polls `workstream list` until the provider has replaced the placeholder the
 * daemon answers `create` with. Creation is asynchronous by design, so the CLI
 * cannot report a finished workstream and neither can a test assume one.
 */
async function awaitWorkstream(
  projectId: string,
  timeoutMs = 20000,
): Promise<WorkstreamJSON> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const listed = await cliJSON<WorkstreamJSON[]>(
      'workstream',
      'list',
      '-project',
      projectId,
      homeDirArg,
    );
    const real = listed.find(
      (workstream) => !workstream.id.startsWith('creating-'),
    );
    if (real) return real;
    await new Promise((done) => setTimeout(done, 50));
  }
  throw new Error(`timed out waiting for a workstream in ${projectId}`);
}

async function awaitSession(
  projectId: string,
  workstreamId: string,
  timeoutMs = 20000,
): Promise<{ id: string; state: string }> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const listed = await cliJSON<{ id: string; state: string }[]>(
      'session',
      'list',
      '-project',
      projectId,
      '-workstream',
      workstreamId,
      homeDirArg,
    );
    const real = listed.find((session) => !isPlaceholderSession(session.id));
    if (real) return real;
    await new Promise((done) => setTimeout(done, 50));
  }
  throw new Error(`timed out waiting for a session in ${workstreamId}`);
}

test.describe('homeshell data verbs', () => {
  // Serial, against the project's fullyParallel default: each of these tests
  // drives a whole create-workstream/create-session lifecycle through a separate
  // process, and running eight of them at once adds enough concurrent load on the
  // shared demo daemon to destabilize the *other* api specs. One at a time keeps
  // this spec's footprint to a single worker.
  test.describe.configure({ mode: 'serial' });

  test('project list prints a table and --json yields the API payload', async ({
    request,
  }) => {
    const table = await cliOk('project', 'list', homeDirArg);
    for (const column of ['ID', 'NAME', 'STATE', 'WORKSTREAMS', 'PATH']) {
      expect(table.stdout).toContain(column);
    }

    const payload = await cliJSON<{ projects: { id: string }[] }>(
      'project',
      'list',
      homeDirArg,
    );
    expect(Array.isArray(payload.projects)).toBe(true);

    // The same endpoint, reached directly: --json is a pass-through, so the
    // seeded demo project has to appear through both paths.
    const direct = (await (await request.get('/api/projects')).json()) as {
      projects: { id: string }[];
    };
    const seeded = direct.projects.find(
      (project) => project.id === 'proj-homeshell',
    );
    expect(
      seeded,
      'the demo daemon should serve the seeded projects',
    ).toBeTruthy();
    expect(
      payload.projects.some((project) => project.id === 'proj-homeshell'),
    ).toBe(true);
    expect(table.stdout).toContain('proj-homeshell');
  });

  test('a project added by the CLI is visible over the API, and removable again', async ({
    request,
  }) => {
    const dir = await tempProjectDir('hs-cli-roundtrip-');
    const added = await cliJSON<{ id: string; name: string; path: string }>(
      'project',
      'add',
      dir,
      '-name',
      'CLI Round Trip',
      homeDirArg,
    );
    expect(added.name).toBe('CLI Round Trip');
    expect(added.path).toBe(dir);

    const overApi = (await (await request.get('/api/projects')).json()) as {
      projects: { id: string; name: string }[];
    };
    expect(overApi.projects.some((project) => project.id === added.id)).toBe(
      true,
    );

    // Removal by name, which is the CLI's own convenience over the API's IDs.
    const removed = await cliOk(
      'project',
      'remove',
      'CLI Round Trip',
      homeDirArg,
    );
    expect(removed.stdout).toContain('removed project CLI Round Trip');

    const after = (await (await request.get('/api/projects')).json()) as {
      projects: { id: string }[];
    };
    expect(after.projects.some((project) => project.id === added.id)).toBe(
      false,
    );
  });

  test('a workstream and a session can be driven end to end from the CLI', async ({
    request,
  }) => {
    const projectId = await freshProject('lifecycle');

    const creating = await cliOk(
      'workstream',
      'create',
      '-project',
      projectId,
      '-provider',
      'fake',
      '-name',
      'cli-lifecycle',
      '-config',
      '{"branch":"main"}',
      homeDirArg,
    );
    expect(creating.stdout).toContain('creating workstream cli-lifecycle');
    const workstream = await awaitWorkstream(projectId);

    await cliOk(
      'session',
      'new',
      '-project',
      projectId,
      '-workstream',
      workstream.id,
      '-agent',
      'claude',
      homeDirArg,
    );
    const session = await awaitSession(projectId, workstream.id);

    // Stop and start through the CLI; the daemon is the one that reports state,
    // so read it back over the API rather than trusting the CLI's own message.
    expect(
      (await cliOk('session', 'stop', session.id, homeDirArg)).stdout,
    ).toContain('stopped');
    expect(
      await sessionState(request, projectId, workstream.id, session.id),
    ).toBe('stopped');
    expect(
      (await cliOk('session', 'start', session.id, homeDirArg)).stdout,
    ).toContain('started');
    expect(
      await sessionState(request, projectId, workstream.id, session.id),
    ).toBe('running');

    // Restart with new settings; the daemon records what it restarted with.
    expect(
      (
        await cliOk(
          'session',
          'restart',
          '-config',
          '{"model":"haiku"}',
          session.id,
          homeDirArg,
        )
      ).stdout,
    ).toContain('restarting');
    // Read once the restart is over: a restarting session's settings are refused.
    const launchPath = `/api/projects/${projectId}/workstreams/${workstream.id}/sessions/${session.id}/launch-config`;
    await expect
      .poll(async () => (await request.get(launchPath)).status(), {
        timeout: 10_000,
      })
      .toBe(200);
    expect((await (await request.get(launchPath)).json()).config).toEqual({
      model: 'haiku',
    });

    // Without -config the daemon replays what the session was last started with;
    // retried while the restart above is still in flight.
    await expect
      .poll(
        async () =>
          (await cli('session', 'restart', '-fresh', session.id, homeDirArg))
            .code,
        { timeout: 10_000 },
      )
      .toBe(EXIT.ok);
    await expect
      .poll(async () => (await request.get(launchPath)).status(), {
        timeout: 10_000,
      })
      .toBe(200);
    expect((await (await request.get(launchPath)).json()).config).toEqual({
      model: 'haiku',
    });
    // A malformed -config is a usage error.
    expect(
      (
        await cli(
          'session',
          'restart',
          '-config',
          '{nope',
          session.id,
          homeDirArg,
        )
      ).code,
    ).toBe(EXIT.usage);

    // A running session is exactly what the removal guard protects, so force here.
    await cliOk('workstream', 'remove', workstream.id, '--force', homeDirArg);
    expect(
      await cliJSON<WorkstreamJSON[]>(
        'workstream',
        'list',
        '-project',
        projectId,
        homeDirArg,
      ),
    ).toHaveLength(0);

    await cliOk('project', 'remove', projectId, homeDirArg);
  });

  // A chosen mode must reach the daemon's open-with endpoint: the default open
  // ignores any mode, so a CLI posting there would open the default and exit 0
  // whatever -mode said.
  test('workstream open honours -mode and refuses one that is not on offer', async () => {
    const projectId = await freshProject('open-mode');
    await cliOk(
      'workstream',
      'create',
      '-project',
      projectId,
      '-provider',
      'fake',
      '-name',
      'cli-open-mode',
      '-config',
      '{"branch":"main"}',
      homeDirArg,
    );
    const workstream = await awaitWorkstream(projectId);
    // The fake's web-IDE link is declared by a live session, so start one.
    await cliOk(
      'session',
      'new',
      '-project',
      projectId,
      '-workstream',
      workstream.id,
      '-agent',
      'claude',
      homeDirArg,
    );
    await awaitSession(projectId, workstream.id);
    await expect
      .poll(async () => {
        const listed = await cliJSON<
          (WorkstreamJSON & { openModes?: { id: string }[] })[]
        >('workstream', 'list', '-project', projectId, homeDirArg);
        return (listed[0]?.openModes ?? []).map((mode) => mode.id);
      })
      .toContain('link:web-ide');

    const linked = await cliOk(
      'workstream',
      'open',
      workstream.id,
      '--mode',
      'link:web-ide',
      homeDirArg,
    );
    expect(linked.stdout).toContain('in mode link:web-ide');
    expect(linked.stdout).toMatch(/https?:\/\//);

    const refused = await cli(
      'workstream',
      'open',
      workstream.id,
      '--mode',
      'nope',
      homeDirArg,
    );
    expect(refused.code).toBe(EXIT.failed);
    expect(refused.stderr).toContain('modes on offer');
    expect(refused.stderr).toContain('link:web-ide');

    await cliOk('workstream', 'remove', workstream.id, '--force', homeDirArg);
    await cliOk('project', 'remove', projectId, homeDirArg);
  });

  test('removing a workstream with a running session is refused, and says how to override', async () => {
    const projectId = await freshProject('guard');
    await cliOk(
      'workstream',
      'create',
      '-project',
      projectId,
      '-provider',
      'fake',
      '-name',
      'cli-guard',
      '-config',
      '{"branch":"main"}',
      homeDirArg,
    );
    const workstream = await awaitWorkstream(projectId);
    await cliOk(
      'session',
      'new',
      '-project',
      projectId,
      '-workstream',
      workstream.id,
      '-agent',
      'claude',
      homeDirArg,
    );
    const session = await awaitSession(projectId, workstream.id);
    // The fake provider creates sessions idle; stop→start is what makes one busy.
    await cliOk('session', 'stop', session.id, homeDirArg);
    await cliOk('session', 'start', session.id, homeDirArg);

    const refused = await cli(
      'workstream',
      'remove',
      workstream.id,
      homeDirArg,
    );
    expect(refused.code).toBe(EXIT.failed);
    // What has to hold is that the refusal names the session standing in the way
    // and the flag that overrides it — not the daemon's exact sentence, which is
    // its to word (it says "still busy" today, "still running" before #104).
    expect(refused.stderr).toContain('Claude Code');
    expect(refused.stderr).toContain('running');
    expect(refused.stderr).toContain('--force');

    // Refused means nothing was torn down.
    const stillThere = await cliJSON<WorkstreamJSON[]>(
      'workstream',
      'list',
      '-project',
      projectId,
      homeDirArg,
    );
    expect(stillThere.some((candidate) => candidate.id === workstream.id)).toBe(
      true,
    );

    await cliOk('workstream', 'remove', workstream.id, '--force', homeDirArg);
    await cliOk('project', 'remove', projectId, homeDirArg);
  });

  test('exit codes distinguish no daemon, a refusal and a bad invocation', async () => {
    // No daemon: an empty data directory has no state file to discover.
    const elsewhere = scratchDir('nodaemon-');
    const missing = await cli('project', 'list', `-home-dir=${elsewhere}`);
    expect(missing.code).toBe(EXIT.noDaemon);
    expect(missing.stderr).toContain('no daemon is running');
    expect(missing.stderr).toContain('homeshell serve');

    // The daemon answered and refused.
    const refused = await cli(
      'project',
      'add',
      join(elsewhere, 'does-not-exist'),
      homeDirArg,
    );
    expect(refused.code).toBe(EXIT.failed);
    expect(refused.stderr).toContain('path does not exist');

    // The invocation itself is wrong.
    const misspelled = await cli('project', 'frobnicate', homeDirArg);
    expect(misspelled.code).toBe(EXIT.usage);
    expect(misspelled.stderr).toContain('unknown subcommand');

    const missingArgument = await cli('project', 'remove', homeDirArg);
    expect(missingArgument.code).toBe(EXIT.usage);
    expect(missingArgument.stderr).toContain('required');
  });

  test('an ambiguous name is refused instead of guessed', async () => {
    const first = await freshProject('dup-a');
    const second = await freshProject('dup-b');
    for (const projectId of [first, second]) {
      await cliOk(
        'workstream',
        'create',
        '-project',
        projectId,
        '-provider',
        'fake',
        '-name',
        'cli-ambiguous',
        '-config',
        '{"branch":"main"}',
        homeDirArg,
      );
      await awaitWorkstream(projectId);
    }

    const refused = await cli(
      'workstream',
      'remove',
      'cli-ambiguous',
      homeDirArg,
    );
    expect(refused.code).toBe(EXIT.failed);
    expect(refused.stderr).toContain('workstreams are named');

    // Scoping to one project makes the same name unambiguous.
    await cliOk(
      'workstream',
      'remove',
      'cli-ambiguous',
      '-project',
      first,
      homeDirArg,
    );
    await cliOk(
      'workstream',
      'remove',
      'cli-ambiguous',
      '-project',
      second,
      homeDirArg,
    );
    await cliOk('project', 'remove', first, homeDirArg);
    await cliOk('project', 'remove', second, homeDirArg);
  });

  test('a project can be renamed and refreshed from the CLI', async ({
    request,
  }) => {
    const projectId = await freshProject('rename');

    await cliOk('project', 'rename', projectId, 'CLI Renamed', homeDirArg);
    const overApi = (await (await request.get('/api/projects')).json()) as {
      projects: { id: string; name: string }[];
    };
    expect(
      overApi.projects.find((project) => project.id === projectId)?.name,
    ).toBe('CLI Renamed');

    // Re-detection reports what it concluded; a bare temp dir is not a repo.
    const refreshed = await cliOk('project', 'refresh', projectId, homeDirArg);
    expect(refreshed.stdout).toContain('not git-backed');

    await cliOk('project', 'remove', projectId, homeDirArg);
  });

  test('discovery verbs answer what create takes for -provider and -agent', async () => {
    const projectId = await freshProject('discovery');

    // The IDs these print are exactly what the create verbs accept, which is the
    // whole point of them: no guessing at a provider or agent name.
    const providers = await cliJSON<{ id: string }[]>(
      'workstream',
      'providers',
      '-project',
      projectId,
      homeDirArg,
    );
    const fake = providers.find((provider) => provider.id === 'fake');
    expect(fake, 'the demo daemon should offer the fake provider').toBeTruthy();

    await cliOk(
      'workstream',
      'create',
      '-project',
      projectId,
      '-provider',
      fake?.id ?? 'fake',
      '-name',
      'cli-discovery',
      '-config',
      '{"branch":"main"}',
      homeDirArg,
    );
    const workstream = await awaitWorkstream(projectId);

    const agents = await cliJSON<
      { providerId: string; options: { id: string }[] }[]
    >(
      'session',
      'agents',
      '-workstream',
      workstream.id,
      '-project',
      projectId,
      homeDirArg,
    );
    const offered = agents.flatMap((schema) =>
      schema.options.map((option) => option.id),
    );
    expect(offered.length).toBeGreaterThan(0);

    await cliOk(
      'session',
      'new',
      '-project',
      projectId,
      '-workstream',
      workstream.id,
      '-agent',
      offered[0],
      homeDirArg,
    );
    await awaitSession(projectId, workstream.id);

    // provider list is daemon-wide rather than per project.
    const described = await cliJSON<{ id: string }[]>(
      'provider',
      'list',
      homeDirArg,
    );
    expect(described.some((provider) => provider.id === 'fake')).toBe(true);

    await cliOk('workstream', 'remove', workstream.id, '--force', homeDirArg);
    await cliOk('project', 'remove', projectId, homeDirArg);
  });

  test('a session can be renamed, keyed and removed from the CLI', async ({
    request,
  }) => {
    const projectId = await freshProject('session-ops');
    await cliOk(
      'workstream',
      'create',
      '-project',
      projectId,
      '-provider',
      'fake',
      '-name',
      'cli-session-ops',
      '-config',
      '{"branch":"main"}',
      homeDirArg,
    );
    const workstream = await awaitWorkstream(projectId);
    await cliOk(
      'session',
      'new',
      '-project',
      projectId,
      '-workstream',
      workstream.id,
      '-agent',
      'claude',
      homeDirArg,
    );
    const session = await awaitSession(projectId, workstream.id);

    await cliOk('session', 'rename', session.id, 'CLI Named', homeDirArg);
    expect(
      await sessionTitle(request, projectId, workstream.id, session.id),
    ).toBe('CLI Named');

    // Clearing hands the name back to the provider, so the title changes again.
    await cliOk('session', 'rename', session.id, '-clear', homeDirArg);
    expect(
      await sessionTitle(request, projectId, workstream.id, session.id),
    ).not.toBe('CLI Named');

    await cliOk('session', 'key', session.id, 'down', homeDirArg);

    // `delete` is the alias; it removes the session outright, unlike `stop`.
    await cliOk('session', 'delete', session.id, homeDirArg);
    const remaining = await cliJSON<{ id: string }[]>(
      'session',
      'list',
      '-workstream',
      workstream.id,
      '-project',
      projectId,
      homeDirArg,
    );
    expect(remaining.some((entry) => entry.id === session.id)).toBe(false);

    await cliOk('workstream', 'remove', workstream.id, '--force', homeDirArg);
    await cliOk('project', 'remove', projectId, homeDirArg);
  });

  test('settings and the live log level round-trip through the CLI', async ({
    request,
  }) => {
    // A setting the daemon owns: change it here, read it back over the API.
    // Under the settings lock: other tests write the whole document, and one
    // that lands between the CLI's write and this read would undo it.
    await withSettingsLock(async () => {
      const original = (
        await cliOk('settings', 'get', 'projects.confirmRemove', homeDirArg)
      ).stdout.trim();
      const flipped = original === 'true' ? 'false' : 'true';

      try {
        await cliOk(
          'settings',
          'set',
          'projects.confirmRemove',
          flipped,
          homeDirArg,
        );
        const overApi = (await (await request.get('/api/settings')).json()) as {
          projects: { confirmRemove: boolean };
        };
        expect(String(overApi.projects.confirmRemove)).toBe(flipped);
      } finally {
        await cliOk(
          'settings',
          'set',
          'projects.confirmRemove',
          original,
          homeDirArg,
        );
      }
    });

    // A wrong type is caught before the daemon; a wrong value is the daemon's.
    const badType = await cli(
      'settings',
      'set',
      'sessions.previewLines',
      'eight',
      homeDirArg,
    );
    expect(badType.code).toBe(EXIT.usage);
    const badValue = await cli(
      'settings',
      'set',
      'notifications.style',
      'bogus',
      homeDirArg,
    );
    expect(badValue.code).toBe(EXIT.failed);

    // The live log level is runtime config, not the settings document.
    const level = (
      await cliOk('config', 'get', 'logLevel', homeDirArg)
    ).stdout.trim();
    await cliOk('config', 'set', 'logLevel', 'debug', homeDirArg);
    const live = (await (await request.get('/api/config')).json()) as {
      logLevel: string;
    };
    expect(live.logLevel).toBe('debug');
    await cliOk('config', 'set', 'logLevel', level, homeDirArg);

    const badLevel = await cli(
      'config',
      'set',
      'logLevel',
      'shouty',
      homeDirArg,
    );
    expect(badLevel.code).toBe(EXIT.failed);
  });

  test('version reports both the client and the daemon', async () => {
    const text = await cliOk('version', homeDirArg);
    expect(text.stdout).toContain('client');
    expect(text.stdout).toContain('daemon');

    const reported = await cliJSON<{
      client: string;
      daemon: { current: string } | null;
    }>('version', homeDirArg);
    expect(reported.client.length).toBeGreaterThan(0);
    expect(
      reported.daemon,
      'a running daemon should be reported, not null',
    ).not.toBeNull();
    expect(reported.daemon?.current).toBeTruthy();
  });

  test('events relays the SSE feed as newline-delimited JSON and exits 0 on interrupt', async () => {
    const stream = spawn(binary, ['events', homeDirArg], {
      stdio: ['ignore', 'pipe', 'pipe'],
    });
    const lines: string[] = [];
    let buffered = '';
    stream.stdout.setEncoding('utf8');
    stream.stdout.on('data', (chunk: string) => {
      buffered += chunk;
      const parts = buffered.split('\n');
      buffered = parts.pop() ?? '';
      lines.push(...parts.filter((line) => line.length > 0));
    });

    const exited = new Promise<number>((done) => {
      stream.on('close', (code, signal) => done(signal ? -1 : (code ?? -1)));
    });

    // The daemon sends a full-state snapshot on connect, so the first line is
    // already there without waiting for anything to change.
    const deadline = Date.now() + 20000;
    while (lines.length === 0 && Date.now() < deadline) {
      await new Promise((done) => setTimeout(done, 50));
    }
    expect(
      lines.length,
      'the feed should emit the snapshot event on connect',
    ).toBeGreaterThan(0);

    const first = JSON.parse(lines[0]) as {
      event: string;
      data: { projects?: unknown[] };
    };
    expect(first.event).toBe('state');
    expect(Array.isArray(first.data.projects)).toBe(true);
    // No heartbeats unless asked for: they carry no state.
    expect(
      lines.some(
        (line) => (JSON.parse(line) as { event: string }).event === 'heartbeat',
      ),
    ).toBe(false);

    stream.kill('SIGINT');
    expect(await exited).toBe(EXIT.ok);
  });
});

/** Reads a session's title from the API, to check a rename actually landed. */
async function sessionTitle(
  request: APIRequestContext,
  projectId: string,
  workstreamId: string,
  sessionId: string,
): Promise<string | undefined> {
  const state = (await (await request.get('/api/projects')).json()) as {
    projects?: {
      id: string;
      workstreams?: {
        id: string;
        sessions?: { id: string; title: string }[];
      }[];
    }[];
  };
  return state.projects
    ?.find((project) => project.id === projectId)
    ?.workstreams?.find((workstream) => workstream.id === workstreamId)
    ?.sessions?.find((session) => session.id === sessionId)?.title;
}

/** Reads a session's state straight from the API, which is the source of truth. */
async function sessionState(
  request: APIRequestContext,
  projectId: string,
  workstreamId: string,
  sessionId: string,
): Promise<string | undefined> {
  const state = (await (await request.get('/api/projects')).json()) as {
    projects?: {
      id: string;
      workstreams?: {
        id: string;
        sessions?: { id: string; state: string }[];
      }[];
    }[];
  };
  return state.projects
    ?.find((project) => project.id === projectId)
    ?.workstreams?.find((workstream) => workstream.id === workstreamId)
    ?.sessions?.find((session) => session.id === sessionId)?.state;
}
