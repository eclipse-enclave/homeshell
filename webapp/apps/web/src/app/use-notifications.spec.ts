import type { NotificationMessage } from '@homeshell/model';
import { act, renderHook } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { useNotifications } from './use-notifications';

const playSound = vi.hoisted(() => vi.fn());
vi.mock('./notification-sound', () => ({
  playNotificationSound: playSound,
}));

function message(overrides: Partial<NotificationMessage> = {}): NotificationMessage {
  return {
    id: 'n1',
    kind: 'waiting',
    title: 'Implement is waiting for input',
    body: 'alpha · feature/x',
    sound: 'chime',
    projectId: 'p1',
    projectName: 'alpha',
    workstreamId: 'w1',
    sessionId: 's1',
    sessionTitle: 'Implement',
    agent: 'Claude Code',
    ...overrides,
  };
}

describe('useNotifications', () => {
  beforeEach(() => {
    playSound.mockClear();
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.clearAllTimers();
    vi.useRealTimers();
  });

  it('shows a toast and plays the sound the daemon chose', () => {
    const { result } = renderHook(() => useNotifications());
    act(() => result.current.present(message()));

    expect(result.current.toasts).toHaveLength(1);
    expect(result.current.toasts[0].title).toBe(
      'Implement is waiting for input',
    );
    expect(playSound).toHaveBeenCalledWith('chime');
  });

  it('auto-dismisses a toast after its lifetime', () => {
    const { result } = renderHook(() => useNotifications());
    act(() => result.current.present(message()));
    expect(result.current.toasts).toHaveLength(1);

    act(() => vi.advanceTimersByTime(6000));
    expect(result.current.toasts).toHaveLength(0);
  });

  it('dismisses a toast on demand', () => {
    const { result } = renderHook(() => useNotifications());
    act(() => result.current.present(message()));
    act(() => result.current.dismiss('n1'));
    expect(result.current.toasts).toHaveLength(0);
  });

  it('caps the visible stack', () => {
    const { result } = renderHook(() => useNotifications());
    act(() => {
      for (let i = 0; i < 5; i++) {
        result.current.present(message({ id: `n${i}` }));
      }
    });
    expect(result.current.toasts).toHaveLength(3);
    expect(result.current.toasts.map((t) => t.id)).toEqual(['n2', 'n3', 'n4']);
  });
});
