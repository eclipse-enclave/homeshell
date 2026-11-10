import { expect, test } from '@playwright/test';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { homeshellBin as binary } from '../e2e-env';

/**
 * Black-box tests for the homeshell CLI's non-serving verbs (dispatch, usage,
 * --version). The daemon-serving `serve` path is covered by daemon.spec.ts,
 * which runs against the daemon that playwright.config.ts starts with `serve`.
 *
 * The run's daemon build is exec'd directly rather than via `go run`, because
 * `go run` masks the program's exit code (it exits 1 for any non-zero child),
 * and these tests assert the exact codes.
 */
const execFileAsync = promisify(execFile);

interface CliResult {
  code: number;
  stdout: string;
  stderr: string;
}

async function runCli(args: string[]): Promise<CliResult> {
  try {
    const { stdout, stderr } = await execFileAsync(binary, args);
    return { code: 0, stdout, stderr };
  } catch (err) {
    const e = err as { code?: number; stdout?: string; stderr?: string };
    return { code: e.code ?? 1, stdout: e.stdout ?? '', stderr: e.stderr ?? '' };
  }
}

test.describe('homeshell CLI', () => {
  test('bare invocation prints usage and exits 0', async () => {
    const { code, stdout } = await runCli([]);
    expect(code).toBe(0);
    expect(stdout).toContain('Usage');
    expect(stdout).toContain('serve');
  });

  test('--version prints a version and exits 0', async () => {
    const { code, stdout } = await runCli(['--version']);
    expect(code).toBe(0);
    expect(stdout.trim().length).toBeGreaterThan(0);
  });

  test('an unknown command errors on stderr and exits 2', async () => {
    const { code, stderr } = await runCli(['bogus']);
    expect(code).toBe(2);
    expect(stderr).toContain('unknown command');
  });
});
