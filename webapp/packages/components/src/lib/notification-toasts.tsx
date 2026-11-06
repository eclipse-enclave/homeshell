import styles from './notification-toasts.module.css';

/** One toast to render. Shaped by what the toast shows, not the wire model. */
export interface NotificationToastItem {
  id: string;
  title: string;
  body: string;
  kind?: string;
}

export interface NotificationToastsProps {
  items: readonly NotificationToastItem[];
  onDismiss: (id: string) => void;
}

/**
 * A stack of in-app notification toasts (the always-visible "UI notification"
 * of issue #66). Pure and transport-agnostic: it renders whatever items it is
 * given and reports dismissals; the daemon authors the text and the app decides
 * when to show and hide each one.
 */
export function NotificationToasts({
  items,
  onDismiss,
}: NotificationToastsProps) {
  if (items.length === 0) return null;
  return (
    <div className={styles.stack} role="region" aria-label="Notifications">
      {items.map((item) => (
        <div key={item.id} className={styles.toast} role="alert">
          <span className={styles.icon} aria-hidden="true">
            🔔
          </span>
          <div className={styles.text}>
            <strong className={styles.title}>{item.title}</strong>
            {item.body && <span className={styles.body}>{item.body}</span>}
          </div>
          <button
            type="button"
            className={styles.dismiss}
            aria-label="Dismiss notification"
            onClick={() => onDismiss(item.id)}
          >
            ×
          </button>
        </div>
      ))}
    </div>
  );
}

export default NotificationToasts;
