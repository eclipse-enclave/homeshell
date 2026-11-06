import { useCallback, useLayoutEffect, useRef } from 'react';
import { flushSync } from 'react-dom';
import { MNEMONIC_EVENT, MNEMONICS, mnemonicProps } from './mnemonic';
import styles from './renamable-title.module.css';

export interface RenamableTitleOptions {
  /** The name the title shows, which its tooltip repeats. */
  name: string;
  /** Starts the rename. */
  onRename: () => void;
  /** Why it cannot be renamed right now; set, the title says so and does nothing. */
  disabledReason?: string;
  /** Whether F2 renames it too, which the tooltip then names. */
  f2?: boolean;
  /** The title's own class, which the props returned carry. */
  className?: string;
}

/**
 * The box a rename puts in the title's place, drawn with shadows so nothing
 * moves as it appears. `--rename-fill` sets its fill on a non-page surface.
 */
export const renameInputClass = styles.input;

/**
 * Props that make a title rename what it names on a double-click or Alt+R,
 * tinted on hover and while Alt reaches it, with a tooltip saying how — or why
 * not, in which case it has neither tint nor letter. It is not a control of
 * its own: a click still selects what it is on.
 */
export function useRenamableTitle({
  name,
  onRename,
  disabledReason,
  f2 = false,
  className,
}: RenamableTitleOptions) {
  const disabled = Boolean(disabledReason);
  const rename = useRef<(() => void) | undefined>(undefined);
  useLayoutEffect(() => {
    rename.current = disabled ? undefined : onRename;
  });
  // Alt+R must not click it, which would select instead, so the event is
  // taken either way. flushSync renders the box and focuses it before the next
  // key arrives: React would render an update from this non-React event a task
  // later, and keys typed straight after Alt+R would be lost.
  const ref = useCallback((element: HTMLElement | null) => {
    if (!element) return;
    const onMnemonic = (event: Event) => {
      event.preventDefault();
      const start = rename.current;
      if (start) flushSync(start);
    };
    element.addEventListener(MNEMONIC_EVENT, onMnemonic);
    return () => element.removeEventListener(MNEMONIC_EVENT, onMnemonic);
  }, []);

  return {
    ref,
    className: disabled
      ? className
      : [className, styles.renamable].filter(Boolean).join(' '),
    title: disabled
      ? `${name}\n\n${disabledReason}`
      : `${name}\n\nDouble-click to rename${f2 ? ' (or press F2)' : ''}`,
    onDoubleClick: disabled ? undefined : onRename,
    ...mnemonicProps(disabled ? undefined : MNEMONICS.rename),
  };
}
