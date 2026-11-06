import type { KeyboardEvent } from 'react';
import { isModified, moveFocus } from './keys';
import styles from './new-workstream-form.module.css';

/** Options with ≤ this many items render as pill radio buttons instead of a dropdown. */
export const PILL_MAX = 5;

export interface PillSelectOption {
  value: string;
  label: string;
}

export interface PillSelectProps {
  /** Accessible label for the group (shown above the pills). */
  label: string;
  options: PillSelectOption[];
  value: string;
  /** Takes no input, but a pill stays focusable, so the one with focus keeps it. */
  disabled?: boolean;
  onChange: (value: string) => void;
  'aria-invalid'?: boolean;
  'aria-describedby'?: string;
}

/**
 * The pills themselves, as a radio group: Tab stops once, on the checked pill
 * (the first when none is), and the arrows move the choice along, wrapping,
 * as Home and End move it to either end. Enter submits the form they are in.
 */
export function PillGroup({
  label,
  options,
  value,
  disabled,
  onChange,
  ...aria
}: PillSelectProps) {
  const tabStop = options.some((o) => o.value === value)
    ? value
    : options[0]?.value;

  const handleKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    if (disabled || isModified(event)) return;
    // As on a native radio: Enter submits the form the pills are in.
    if (event.key === 'Enter') {
      const form = event.currentTarget.closest('form');
      if (form) {
        event.preventDefault();
        form.requestSubmit();
      }
      return;
    }
    const pills = Array.from(event.currentTarget.querySelectorAll('button'));
    const next = moveFocus(event, pills, { wrap: true, orientation: 'both' });
    if (next !== undefined) onChange(options[next].value);
  };

  return (
    <div
      className={styles.pillGroup}
      role="radiogroup"
      aria-label={label}
      {...aria}
      onKeyDown={handleKeyDown}
    >
      {options.map((o) => (
        <button
          key={o.value}
          type="button"
          role="radio"
          aria-checked={value === o.value}
          tabIndex={o.value === tabStop ? 0 : -1}
          className={styles.pill}
          aria-disabled={disabled}
          onClick={() => {
            if (!disabled) onChange(o.value);
          }}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}

/**
 * A row of pill-styled radio buttons for selecting among a small set of
 * options. Use `PILL_MAX` to decide whether to render this or a `<select>`.
 */
export function PillSelect({ label, ...group }: PillSelectProps) {
  return (
    <div className={styles.label}>
      {label}
      <PillGroup label={label} {...group} />
    </div>
  );
}
