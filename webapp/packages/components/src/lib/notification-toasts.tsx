import { useEffect, useRef, useState } from 'react';

import styles from './notification-toasts.module.css';

/** One toast to render. Shaped by what the toast shows, not the wire model. */
export interface NotificationToastItem {
  id: string;
  title: string;
  body: string;
  kind?: string;
  /** Whether the toast leads somewhere, so clicking its text opens it. */
  openable?: boolean;
}

export interface NotificationToastsProps {
  items: readonly NotificationToastItem[];
  onDismiss: (id: string) => void;
  /** An openable toast's text was clicked. */
  onOpen?: (id: string) => void;
  /**
   * Keep a toast on screen: the pointer is over the stack, or focus is in the
   * toast. Called once per toast until onRelease.
   */
  onHold?: (id: string) => void;
  /** A held toast is neither pointed at nor focused any more. */
  onRelease?: (id: string) => void;
}

/**
 * A stack of in-app notification toasts (the always-visible "UI notification"
 * of issue #66). Pure and transport-agnostic: it renders whatever items it is
 * given and reports dismissals, opens and holds; the daemon authors the text
 * and the app decides when to show and hide each one, and where one leads.
 */
export function NotificationToasts(props: NotificationToastsProps) {
  if (props.items.length === 0) return null;
  return <ToastStack {...props} />;
}

/**
 * The stack itself, mounted only while it has toasts, so the pointer it last
 * saw over it goes with it.
 *
 * The pointer anywhere over the stack holds every toast in it, ones that
 * arrive meanwhile included, and focus holds the toast it is in. The newest
 * toast goes on top: the stack is anchored to the bottom of the screen, so a
 * new one grows it upwards and leaves the toast under the pointer where it is.
 * Why not hold just the toast pointed at: anything that moves the stack would
 * slide a different one under a pointer that has not moved.
 */
function ToastStack({
  items,
  onDismiss,
  onOpen,
  onHold,
  onRelease,
}: NotificationToastsProps) {
  const [pointerInside, setPointerInside] = useState(false);
  const [focusedId, setFocusedId] = useState<string | null>(null);
  // What onHold has been called for and onRelease not since.
  const held = useRef(new Set<string>());

  useEffect(() => {
    const ids = new Set(items.map((item) => item.id));
    const wanted = new Set(
      pointerInside ? ids : focusedId && ids.has(focusedId) ? [focusedId] : [],
    );
    for (const id of held.current) {
      if (wanted.has(id)) continue;
      held.current.delete(id);
      // One already gone needs no letting go.
      if (ids.has(id)) onRelease?.(id);
    }
    for (const id of wanted) {
      if (held.current.has(id)) continue;
      held.current.add(id);
      onHold?.(id);
    }
  }, [items, pointerInside, focusedId, onHold, onRelease]);

  return (
    <div
      className={styles.stack}
      role="region"
      aria-label="Notifications"
      onPointerEnter={() => setPointerInside(true)}
      onPointerLeave={() => setPointerInside(false)}
    >
      {[...items].reverse().map((item) => {
        const text = (
          <>
            <strong className={styles.title}>{item.title}</strong>
            {item.body && <span className={styles.body}>{item.body}</span>}
          </>
        );
        return (
          <div
            key={item.id}
            className={styles.toast}
            role="alert"
            onFocus={() => setFocusedId(item.id)}
            onBlur={(event) => {
              // Focus moving between the toast's own buttons stays inside it.
              if (event.currentTarget.contains(event.relatedTarget)) return;
              setFocusedId((current) => (current === item.id ? null : current));
            }}
          >
            <span className={styles.icon} aria-hidden="true">
              🔔
            </span>
            {item.openable && onOpen ? (
              <button
                type="button"
                className={`${styles.text} ${styles.open}`}
                onClick={() => onOpen(item.id)}
              >
                {text}
              </button>
            ) : (
              <div className={styles.text}>{text}</div>
            )}
            <button
              type="button"
              className={styles.dismiss}
              aria-label="Dismiss notification"
              onClick={() => onDismiss(item.id)}
            >
              ×
            </button>
          </div>
        );
      })}
    </div>
  );
}

export default NotificationToasts;
