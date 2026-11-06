import type { ComponentPropsWithoutRef } from 'react';
import styles from './kbd.module.css';

/** A key or key combination, drawn as a keycap. */
export function Kbd({ className, ...rest }: ComponentPropsWithoutRef<'kbd'>) {
  return (
    <kbd
      className={className ? `${styles.kbd} ${className}` : styles.kbd}
      {...rest}
    />
  );
}

export default Kbd;
