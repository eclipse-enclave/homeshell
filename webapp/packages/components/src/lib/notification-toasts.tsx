import { useRef } from 'react';

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
  /** The pointer or focus entered a toast: keep it on screen. */
  onHold?: (id: string) => void;
  /** The pointer or focus left a toast. */
  onRelease?: (id: string) => void;
}

/**
 * A stack of in-app notification toasts (the always-visible "UI notification"
 * of issue #66). Pure and transport-agnostic: it renders whatever items it is
 * given and reports dismissals, opens and holds; the daemon authors the text
 * and the app decides when to show and hide each one, and where one leads.
 */
export function NotificationToasts({
  items,
  onDismiss,
  onOpen,
  onHold,
  onRelease,
}: NotificationToastsProps) {
  // Per toast, whether the pointer and focus are in it: it is held while
  // either is, and released only once both have left.
  const holders = useRef(new Map<string, { pointer: boolean; focus: boolean }>());
  const setHolder = (id: string, by: 'pointer' | 'focus', inside: boolean) => {
    const before = holders.current.get(id) ?? { pointer: false, focus: false };
    const after = { ...before, [by]: inside };
    holders.current.set(id, after);
    const wasHeld = before.pointer || before.focus;
    const isHeld = after.pointer || after.focus;
    if (isHeld && !wasHeld) onHold?.(id);
    if (!isHeld && wasHeld) onRelease?.(id);
  };

  if (items.length === 0) return null;
  return (
    <div className={styles.stack} role="region" aria-label="Notifications">
      {items.map((item) => {
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
            onPointerEnter={() => setHolder(item.id, 'pointer', true)}
            onPointerLeave={() => setHolder(item.id, 'pointer', false)}
            onFocus={() => setHolder(item.id, 'focus', true)}
            onBlur={(event) => {
              // Focus moving between the toast's own buttons stays inside it.
              if (event.currentTarget.contains(event.relatedTarget)) return;
              setHolder(item.id, 'focus', false);
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
