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
  /** Keep a toast on screen while the user points at or focuses it. */
  hold: (id: string) => void;
  /** Let a held toast go: it gets a full lifetime again from now. */
  release: (id: string) => void;
}

/** How long a toast stays before auto-dismissing. */
const TOAST_TTL_MS = 6000;
/** Cap the visible stack so a burst (e.g. the demo simulator) can't fill the screen. */
const MAX_TOASTS = 3;

/**
 * Presents daemon-pushed in-app notifications (issue #66) as toasts and plays
 * their sound. Whether a notification arrives at all — and via which channel —
 * is decided by the daemon from the Notifications settings category; this hook
 * only renders the in-app ones it is handed.
 */
export function useNotifications(): UseNotifications {
  const [toasts, setToasts] = useState<NotificationToast[]>([]);
  const timers = useRef(new Map<string, ReturnType<typeof setTimeout>>());

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
    },
    [stopTimer],
  );

  const release = useCallback(
    (id: string) => {
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
      setToasts((prev) => [...prev, toast].slice(-MAX_TOASTS));
      release(message.id);
      playNotificationSound(message.sound);
    },
    [release],
  );

  useEffect(() => {
    const pending = timers.current;
    return () => {
      for (const timer of pending.values()) clearTimeout(timer);
      pending.clear();
    };
  }, []);

  // Holding a toast is just stopping its auto-dismiss timer.
  return { toasts, present, dismiss, hold: stopTimer, release };
}
