import { useId } from 'react';
import { FormField } from './form-field';
import styles from './form-controls.module.css';

export interface SelectOption {
  value: string;
  label: string;
}

export interface SelectProps {
  label: string;
  value: string;
  options: readonly SelectOption[];
  onChange: (value: string) => void;
  hint?: string;
  badge?: string;
  /** Validation message shown beneath the control. */
  error?: string;
  disabled?: boolean;
  id?: string;
}

/** Labelled dropdown built on a native `<select>`. */
export function Select({
  label,
  value,
  options,
  onChange,
  hint,
  badge,
  error,
  disabled,
  id,
}: SelectProps) {
  const generatedId = useId();
  const controlId = id ?? generatedId;
  return (
    <FormField label={label} htmlFor={controlId} hint={hint} badge={badge} error={error}>
      <select
        id={controlId}
        className={styles.input}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        disabled={disabled}
      >
        {options.map((option) => (
          <option key={option.value} value={option.value}>
            {option.label}
          </option>
        ))}
      </select>
    </FormField>
  );
}

export default Select;
