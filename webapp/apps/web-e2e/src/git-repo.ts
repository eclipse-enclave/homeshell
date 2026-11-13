import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { type APIRequestContext } from '@playwright/test';
import { tempProjectDir } from './fixtures';
import { addEmptyProject } from './projects';

const execFileAsync = promisify(execFile);

/** Hermetic against the developer's git config, and able to commit without it. */
const env = {
  ...process.env,
  GIT_CONFIG_GLOBAL: '/dev/null',
  GIT_CONFIG_SYSTEM: '/dev/null',
  GIT_AUTHOR_NAME: 'e2e',
  GIT_AUTHOR_EMAIL: 'e2e@example.com',
  GIT_COMMITTER_NAME: 'e2e',
  GIT_COMMITTER_EMAIL: 'e2e@example.com',
};

/** Runs git in `dir` and returns its trimmed output. */
export async function git(dir: string, ...args: string[]): Promise<string> {
  const { stdout } = await execFileAsync('git', args, { cwd: dir, env });
  return stdout.trim();
}

/**
 * A throwaway project directory holding a repository on `main` and a `feature`
 * branch one commit ahead of it, checked out on `feature`, with `main` tagged
 * `v1.0`.
 */
export async function tempGitRepo(prefix: string): Promise<string> {
  const dir = await tempProjectDir(prefix);
  await git(dir, 'init', '-q', '-b', 'main');
  await git(dir, 'commit', '-q', '--allow-empty', '-m', 'initial');
  await git(dir, 'tag', 'v1.0');
  await git(dir, 'checkout', '-q', '-b', 'feature');
  await git(dir, 'commit', '-q', '--allow-empty', '-m', 'feature work');
  return dir;
}

/** A project over a fresh `tempGitRepo`, without its default workstream. */
export async function gitProject(
  request: APIRequestContext,
  prefix: string,
): Promise<{ dir: string; projectId: string }> {
  const dir = await tempGitRepo(prefix);
  return { dir, projectId: await addEmptyProject(request, dir) };
}
