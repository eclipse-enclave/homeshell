import { describe, expect, it } from 'vitest';
import {
  countsSummary,
  endedAgo,
  sessionCounts,
  splitSessions,
} from './session-groups';
import type { Session, SessionState } from './types';

const session = (
  id: string,
  state: SessionState,
  endedAt?: string,
): Session => ({
  id,
  title: id,
  agent: 'Claude Code',
  state,
  endedAt,
});

describe('splitSessions', () => {
  it('keeps live in input order and sorts ended newest first, undated last', () => {
    const { live, ended } = splitSessions([
      session('a', 'running'),
      session('old', 'stopped', '2026-11-10T08:00:00Z'),
      session('undated1', 'done'),
      session('b', 'idle'),
      session('new', 'done', '2026-11-10T09:00:00Z'),
      session('undated2', 'stopped'),
    ]);
    expect(live.map((s) => s.id)).toEqual(['a', 'b']);
    expect(ended.map((s) => s.id)).toEqual([
      'new',
      'old',
      'undated1',
      'undated2',
    ]);
  });
});

describe('sessionCounts', () => {
  it('counts by display state in a fixed order: starting as running, ended as ended', () => {
    const counts = sessionCounts(
      [
        session('1', 'unknown'),
        session('2', 'starting'),
        session('3', 'running'),
        session('4', 'waiting'),
        session('5', 'error'),
        session('6', 'stopped'),
        session('7', 'idle'),
        session('8', 'done'),
      ],
      // The unseen-idle overlay: an ended session counts as ended whatever it shows.
      (s) => (s.state === 'idle' ? 'done' : s.state),
    );
    expect(counts).toEqual([
      { kind: 'waiting', count: 1 },
      { kind: 'done', count: 1 },
      { kind: 'error', count: 1 },
      { kind: 'running', count: 2 },
      { kind: 'ended', count: 2 },
      { kind: 'unknown', count: 1 },
    ]);
  });
});

describe('countsSummary', () => {
  it('words the counts', () => {
    expect(
      countsSummary([
        { kind: 'waiting', count: 1 },
        { kind: 'running', count: 1 },
        { kind: 'ended', count: 5 },
      ]),
    ).toBe('1 waiting for input, 1 running, 5 ended');
  });
});

describe('endedAgo', () => {
  const now = new Date('2026-11-10T12:00:00Z');
  const ago = (ms: number) => new Date(now.getTime() - ms).toISOString();
  const MIN = 60_000;
  const H = 60 * MIN;
  it.each([
    [30_000, 'just now'],
    [5 * MIN, '5 min ago'],
    [23 * H, '23 h ago'],
    [24 * H, 'yesterday'],
    [48 * H, '2 days ago'],
  ])('%i ms ago -> %s', (ms, expected) => {
    expect(endedAgo(ago(ms), now)).toBe(expected);
  });
});

describe('a missing or unparseable end time', () => {
  it('reads as unknown: no relative time, sorted with the undated', () => {
    expect(endedAgo(undefined, new Date())).toBeUndefined();
    expect(endedAgo('not a date', new Date())).toBeUndefined();
    const { ended } = splitSessions([
      session('bad', 'stopped', 'not a date'),
      session('dated', 'stopped', '2026-11-10T08:00:00Z'),
    ]);
    expect(ended.map((s) => s.id)).toEqual(['dated', 'bad']);
  });
});
