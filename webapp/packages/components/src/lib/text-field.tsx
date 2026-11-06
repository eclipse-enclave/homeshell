import { useId } from 'react';
import { FormField } from './form-field';
import styles from './form-controls.module.css';

export interface TextFieldProps {
  label: string;
  value: string;
  onChange: (value: string) => void;
  hint?: string;
  badge?: string;
  /** Validation message shown beneath the input. */
  error?: string;
  placeholder?: string;
  disabled?: boolean;
  id?: string;
}

/** Labelled single-line text input. */
export function TextField({
  label,
  value,
  onChange,
  hint,
  badge,
  error,
  placeholder,
  disabled,
  id,
}: TextFieldProps) {
  const generatedId = useId();
  const controlId = id ?? generatedId;
  return (
    <FormField label={label} htmlFor={controlId} hint={hint} badge={badge} error={error}>
      <input
        id={controlId}
        className={styles.input}
        type="text"
        value={value}
        placeholder={placeholder}
        onChange={(e) => onChange(e.target.value)}
        disabled={disabled}
      />
    </FormField>
  );
}

export default TextField;
