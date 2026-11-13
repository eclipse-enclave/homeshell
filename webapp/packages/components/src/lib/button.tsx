import type { ComponentPropsWithRef, ReactNode } from 'react';
import styles from './controls.module.css';
import { cx } from './cx';
import { MnemonicLabel, mnemonicProps, withShortcuts } from './mnemonic';

// ComponentPropsWithRef, not ButtonHTMLAttributes: `ref` is a plain prop on
// React 19 function components, so it rides along in `...rest` onto the button
// and callers can focus one (a form returns focus to the button that opened it).
export interface ButtonProps extends ComponentPropsWithRef<'button'> {
  variant?: 'ghost' | 'primary' | 'danger';
  /**
   * The letter Alt presses it by (see mnemonic.tsx): underlined in a text
   * label while Alt is held, and named in the tooltip.
   */
  mnemonic?: string;
  /** Other keys that do what it does, named in the tooltip (Stroke notation). */
  shortcuts?: readonly string[];
  /**
   * An icon before the label; without a label, the button is the icon alone,
   * named by its aria-label.
   */
  icon?: ReactNode;
}

/**
 * Small action button in HomeShell's visual style. `aria-disabled` makes it as
 * inert as `disabled` does, a submit button included, but keeps it focusable:
 * disabling the button that has focus drops focus to <body>.
 */
export function Button({
  variant = 'ghost',
  className,
  onClick,
  mnemonic,
  shortcuts,
  title,
  icon,
  children,
  ...rest
}: ButtonProps) {
  const inert =
    rest['aria-disabled'] === true || rest['aria-disabled'] === 'true';
  // A disabled button's keys do nothing (useMnemonics skips it), so its
  // tooltip says why it is disabled and no more.
  const keyed =
    Boolean(mnemonic || shortcuts?.length) && !inert && !rest.disabled;
  return (
    <button
      type="button"
      className={cx(
        {
          ghost: styles.button,
          primary: styles.primary,
          danger: styles.danger,
        }[variant],
        icon ? (children == null ? styles.iconButton : styles.labelled) : null,
        className,
      )}
      onClick={inert ? (event) => event.preventDefault() : onClick}
      title={
        keyed
          ? withShortcuts(
              title ??
                (typeof children === 'string' ? children : rest['aria-label']),
              shortcuts,
              mnemonic,
            )
          : title
      }
      {...mnemonicProps(mnemonic, shortcuts)}
      {...rest}
    >
      {icon}
      {typeof children === 'string' ? (
        <MnemonicLabel label={children} letter={mnemonic} />
      ) : (
        children
      )}
    </button>
  );
}

export default Button;
