import type { Project, Session, SessionState } from '@homeshell/model';
import { act, renderHook } from '@testing-library/react';

import { useSessionAttention } from './use-session-attention';

const session = (id: string, state: SessionState): Session => ({
  id,
  title: id,
  agent: 'Claude Code',
  state,
});

const projectWith = (...sessions: Session[]): Project => ({
  id: 'p',
  name: 'p',
  path: '/p',
  gitBacked: false,
  state: 'idle',
  runtimeState: 'running',
  effectiveState: 'idle',
  workstreams: [
    {
      id: 'w',
      name: 'w',
      provider: 'local',
      strategyId: 'in-place',
      strategy: 'in-place',
      state: 'idle',
      runtimeState: 'running',
      effectiveState: 'idle',
      sessions,
    },
  ],
});

describe('useSessionAttention', () => {
  it('shows a freshly created idle session as idle, not done', () => {
    const { result } = renderHook(
      ({ project }) => useSessionAttention(project),
      {
        initialProps: { project: projectWith(session('s1', 'idle')) },
      },
    );
    expect(result.current.displayState(session('s1', 'idle'))).toBe('idle');
  });

  it('shows a session that finished (running → idle) as done until seen', () => {
    const { result, rerender } = renderHook(
      ({ project }) => useSessionAttention(project),
      { initialProps: { project: projectWith(session('s1', 'running')) } },
    );
    // Finishes: running → idle, unseen → done.
    act(() => rerender({ project: projectWith(session('s1', 'idle')) }));
    expect(result.current.displayState(session('s1', 'idle'))).toBe('done');

    // The user opens it (marks seen) → idle.
    act(() => result.current.markSeen('s1'));
    expect(result.current.displayState(session('s1', 'idle'))).toBe('idle');
  });

  it('resets to done when the agent runs again and finishes again', () => {
    const { result, rerender } = renderHook(
      ({ project }) => useSessionAttention(project),
      { initialProps: { project: projectWith(session('s1', 'running')) } },
    );
    act(() => rerender({ project: projectWith(session('s1', 'idle')) }));
    act(() => result.current.markSeen('s1'));
    expect(result.current.displayState(session('s1', 'idle'))).toBe('idle');

    // New activity, then finishes again → done again.
    act(() => rerender({ project: projectWith(session('s1', 'running')) }));
    act(() => rerender({ project: projectWith(session('s1', 'idle')) }));
    expect(result.current.displayState(session('s1', 'idle'))).toBe('done');
  });

  it('never overlays done on non-idle states', () => {
    const { result } = renderHook(
      ({ project }) => useSessionAttention(project),
      {
        initialProps: { project: projectWith(session('s1', 'stopped')) },
      },
    );
    expect(result.current.displayState(session('s1', 'stopped'))).toBe(
      'stopped',
    );
    expect(result.current.displayState(session('s1', 'waiting'))).toBe(
      'waiting',
    );
  });
});
