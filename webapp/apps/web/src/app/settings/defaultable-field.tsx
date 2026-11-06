import type { ReactNode } from 'react';
import { Toggle } from '@homeshell/components';
import type { Defaultable } from './settings-types';
import styles from './defaultable-field.module.css';

export interface DefaultableFieldProps {
  field: Defaultable<string>;
  onChange: (next: Defaultable<string>) => void;
  /**
   * Renders the value control. Receives the current value, a change handler,
   * and a `disabled` flag that is set while "Last used" is selected.
   */
  children: (props: {
    value: string;
    onChange: (value: string) => void;
    disabled: boolean;
  }) => ReactNode;
  /** Maps a remembered value to a human-readable label for the hint. */
  lastUsedLabel?: (value: string | undefined) => string;
}

/**
 * Wraps a value control with a "Last used" checkbox. When checked, the control
 * is disabled and the effective default becomes the remembered last-used value.
 */
export function DefaultableField({
  field,
  onChange,
  children,
  lastUsedLabel,
}: DefaultableFieldProps) {
  const remembered =
    lastUsedLabel?.(field.lastUsed) ??
    (field.lastUsed !== undefined
      ? `Remembering: ${field.lastUsed}`
      : 'Nothing used yet.');

  return (
    <div className={styles.defaultable}>
      {children({
        value: field.value,
        onChange: (value) => onChange({ ...field, value }),
        disabled: field.useLastUsed,
      })}
      <Toggle
        label="Last used"
        checked={field.useLastUsed}
        onChange={(useLastUsed) => onChange({ ...field, useLastUsed })}
        hint={field.useLastUsed ? remembered : undefined}
      />
    </div>
  );
}

export default DefaultableField;
