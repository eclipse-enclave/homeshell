import { describe, expect, it } from 'vitest';
import { displayPath } from './paths';

describe('displayPath', () => {
  it.each([
    ['/p/app-wt', '/p/app', '../app-wt'],
    ['/p/app-wt/', '/p/app/', '../app-wt'],
    ['/p/app/.worktrees/x', '/p/app', '/p/app/.worktrees/x'],
    ['/srv/x', '/p/app', '/srv/x'],
  ])('%s next to %s -> %s', (dir, project, expected) => {
    expect(displayPath(dir, project)).toBe(expected);
  });
});
