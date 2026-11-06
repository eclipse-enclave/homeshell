import { useId } from 'react';
import styles from './form-controls.module.css';

export interface ToggleProps {
  label: string;
  checked: boolean;
  onChange: (checked: boolean) => void;
  hint?: string;
  badge?: string;
  disabled?: boolean;
  id?: string;
}

/** Labelled boolean control built on a native checkbox. */
export function Toggle({
  label,
  checked,
  onChange,
  hint,
  badge,
  disabled,
  id,
}: ToggleProps) {
  const generatedId = useId();
  const controlId = id ?? generatedId;
  return (
    <div className={styles.field}>
      <label className={styles.checkboxRow} htmlFor={controlId}>
        <input
          id={controlId}
          type="checkbox"
          checked={checked}
          onChange={(e) => onChange(e.target.checked)}
          disabled={disabled}
        />
        <span>{label}</span>
        {badge && (
          <span className={styles.badge} title={badge}>
            {badge}
          </span>
        )}
      </label>
      {hint && <small className={styles.hint}>{hint}</small>}
    </div>
  );
}

export default Toggle;
