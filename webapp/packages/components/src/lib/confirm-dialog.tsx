import { type KeyboardEvent, type ReactNode, useRef } from 'react';
import styles from './confirm-dialog.module.css';
import { type FocusTarget, useModalFocus } from './focus-return';
import { handleModalKeys } from './focus-trap';
import { isModified, moveFocus } from './keys';

export interface ConfirmDialogProps {
  /** Dialog title shown in the header. */
  title: string;
  /** Body content (description, checkboxes, etc.). */
  children: ReactNode;
  /**
   * Footer action buttons. The one marked `data-initial-focus` takes focus when
   * the dialog opens, else the first enabled button does.
   */
  actions: ReactNode;
  /** Called when the dialog is dismissed (Escape or click outside). */
  onClose: () => void;
  /**
   * Where focus goes on close when the element it came from has left the page
   * meanwhile. Read once, when it opens.
   */
  fallbackFocus?: FocusTarget;
  /**
   * What takes focus as it opens, in place of the footer button, e.g. a form's
   * first field. Falls back to the footer's when it finds nothing.
   */
  initialFocus?: (dialog: HTMLElement) => HTMLElement | null;
  /** "wide" for a dialog holding a form: wider, and its body scrolls. */
  size?: 'default' | 'wide';
}

/**
 * A reusable modal overlay for confirmation prompts. Renders an accessible
 * dialog with focus trap, Escape-to-close, and click-outside-to-close. Left and
 * Right (Home and End) move between the footer buttons. Keys pressed inside go
 * no further, so they cannot act on whatever the dialog was opened over.
 */
export function ConfirmDialog({
  title,
  children,
  actions,
  onClose,
  fallbackFocus,
  initialFocus,
  size = 'default',
}: ConfirmDialogProps) {
  const overlayRef = useRef<HTMLDivElement>(null);
  useModalFocus(() => {
    const overlay = overlayRef.current;
    return (
      (overlay && initialFocus?.(overlay)) ??
      overlay?.querySelector<HTMLElement>(
        '[data-initial-focus]:not(:disabled)',
      ) ??
      overlay?.querySelector<HTMLElement>('button:not(:disabled)') ??
      overlay
    );
  }, fallbackFocus);

  const handleFooterKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    if (isModified(event)) return;
    const buttons = event.currentTarget.querySelectorAll<HTMLElement>(
      'button:not(:disabled)',
    );
    moveFocus(event, Array.from(buttons), { orientation: 'horizontal' });
  };

  return (
    <div
      ref={overlayRef}
      className={styles.overlay}
      onKeyDown={(event) => handleModalKeys(event, onClose)}
      onClick={(event) => {
        if (event.target === event.currentTarget) onClose();
      }}
      role="dialog"
      aria-modal="true"
      aria-label={title}
      tabIndex={-1}
    >
      <div
        className={`${styles.dialog} ${size === 'wide' ? styles.wide : ''}`}
      >
        <div className={styles.header}>
          <h3 className={styles.title}>{title}</h3>
        </div>
        <div className={styles.body}>{children}</div>
        <div className={styles.footer} onKeyDown={handleFooterKeyDown}>
          {actions}
        </div>
      </div>
    </div>
  );
}

export default ConfirmDialog;
