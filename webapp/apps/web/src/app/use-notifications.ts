import type { NotificationMessage } from '@homeshell/model';
import { useCallback, useEffect, useRef, useState } from 'react';
import { playNotificationSound } from './notification-sound';

/** A live in-app toast derived from a daemon notification. */
export interface NotificationToast {
  id: string;
  title: string;
  body: string;
  kind: string;
  /** The session the toast is about, to go to on click; none for a test. */
  target?: NotificationTarget;
}

/** Where a toast leads: its session, with the workstream and project holding it. */
export interface NotificationTarget {
  projectId: string;
  workstreamId: string;
  sessionId: string;
}

export interface UseNotifications {
  /** Toasts currently on screen (newest last), for the toast stack. */
  toasts: NotificationToast[];
  /**
   * Feed a notification pushed by the daemon over the SSE `notification` event.
   * Shows an in-app toast and plays the sound the daemon chose. The daemon
   * decides *whether* to push (per the Notifications style setting) and *which*
   * sound; the client is a dumb renderer, so there is no style logic here.
   * The OS-native channel is delivered by the daemon itself, not the browser.
   */
  present: (message: NotificationMessage) => void;
  /** Dismiss a toast by id. */
  dismiss: (id: string) => void;
  /**
   * Keep a toast on screen while the user points at or focuses it: no timer
   * dismisses it, and the stack cap does not evict it.
   */
  hold: (id: string) => void;
  /** Let a held toast go: it gets a full lifetime again from now. */
  release: (id: string) => void;
}

/** How long a toast stays before auto-dismissing. */
const TOAST_TTL_MS = 6000;
/** Cap the visible stack so a burst (e.g. the demo simulator) can't fill the screen. */
const MAX_TOASTS = 3;

/**
 * Trims a stack (newest last) to MAX_TOASTS by dropping its oldest toasts, but
 * never a held one, nor the newest: the user may be about to click a held
 * toast, and a burst of new ones must not take it from under them. The stack
 * outgrows the cap while too many are held.
 */
function capToasts(
  stack: NotificationToast[],
  held: ReadonlySet<string>,
): NotificationToast[] {
  let excess = stack.length - MAX_TOASTS;
  return stack.filter((toast, index) => {
    const evictable =
      excess > 0 && index < stack.length - 1 && !held.has(toast.id);
    if (evictable) excess--;
    return !evictable;
  });
}

/**
 * Presents daemon-pushed in-app notifications (issue #66) as toasts and plays
 * their sound. Whether a notification arrives at all — and via which channel —
 * is decided by the daemon from the Notifications settings category; this hook
 * only renders the in-app ones it is handed.
 */
export function useNotifications(): UseNotifications {
  const [toasts, setToasts] = useState<NotificationToast[]>([]);
  const timers = useRef(new Map<string, ReturnType<typeof setTimeout>>());
  const held = useRef(new Set<string>());

  const stopTimer = useCallback((id: string) => {
    const timer = timers.current.get(id);
    if (timer) {
      clearTimeout(timer);
      timers.current.delete(id);
    }
  }, []);

  const dismiss = useCallback(
    (id: string) => {
      setToasts((prev) => prev.filter((toast) => toast.id !== id));
      stopTimer(id);
      held.current.delete(id);
    },
    [stopTimer],
  );

  const hold = useCallback(
    (id: string) => {
      held.current.add(id);
      stopTimer(id);
    },
    [stopTimer],
  );

  const release = useCallback(
    (id: string) => {
      held.current.delete(id);
      stopTimer(id);
      timers.current.set(
        id,
        setTimeout(() => dismiss(id), TOAST_TTL_MS),
      );
    },
    [stopTimer, dismiss],
  );

  const present = useCallback(
    (message: NotificationMessage) => {
      const toast: NotificationToast = {
        id: message.id,
        title: message.title,
        body: message.body,
        kind: message.kind,
        target: message.projectId
          ? {
              projectId: message.projectId,
              workstreamId: message.workstreamId,
              sessionId: message.sessionId,
            }
          : undefined,
      };
      setToasts((prev) => {
        const next = capToasts([...prev, toast], held.current);
        for (const gone of prev) {
          if (!next.includes(gone)) stopTimer(gone.id);
        }
        return next;
      });
      release(message.id);
      playNotificationSound(message.sound);
    },
    [release, stopTimer],
  );

  useEffect(() => {
    const pending = timers.current;
    return () => {
      for (const timer of pending.values()) clearTimeout(timer);
      pending.clear();
    };
  }, []);

  return { toasts, present, dismiss, hold, release };
}
