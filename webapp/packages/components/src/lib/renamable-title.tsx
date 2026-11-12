import { cx } from './cx';
import { MNEMONICS, mnemonicProps, useMnemonicHandler } from './mnemonic';
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
export function useRenamableTitle<T extends HTMLElement>({
  name,
  onRename,
  disabledReason,
  f2 = false,
  className,
}: RenamableTitleOptions) {
  const disabled = Boolean(disabledReason);
  // Alt+R must not click it, which would select instead, so it takes the
  // letter even while it cannot be renamed.
  const ref = useMnemonicHandler<T>(disabled ? undefined : onRename);

  return {
    ref,
    className: disabled ? className : cx(className, styles.renamable),
    title: disabled
      ? `${name}\n\n${disabledReason}`
      : `${name}\n\nDouble-click to rename${f2 ? ' (or press F2)' : ''}`,
    onDoubleClick: disabled ? undefined : onRename,
    ...mnemonicProps(disabled ? undefined : MNEMONICS.rename),
  };
}
