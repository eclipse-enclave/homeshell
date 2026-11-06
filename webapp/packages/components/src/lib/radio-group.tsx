import { useId } from 'react';
import styles from './form-controls.module.css';

export interface RadioOption {
  value: string;
  label: string;
}

export interface RadioGroupProps {
  label: string;
  value: string;
  options: readonly RadioOption[];
  onChange: (value: string) => void;
  hint?: string;
  badge?: string;
  disabled?: boolean;
  /** Radio input name; defaults to a generated unique name. */
  name?: string;
}

/** A labelled set of mutually exclusive choices, rendered as radios. */
export function RadioGroup({
  label,
  value,
  options,
  onChange,
  hint,
  badge,
  disabled,
  name,
}: RadioGroupProps) {
  const generatedName = useId();
  const groupName = name ?? generatedName;
  return (
    <fieldset className={styles.group}>
      <div className={styles.labelRow}>
        <legend className={styles.legend}>{label}</legend>
        {badge && (
          <span className={styles.badge} title={badge}>
            {badge}
          </span>
        )}
      </div>
      <div className={styles.options}>
        {options.map((option) => (
          <label key={option.value} className={styles.option}>
            <input
              type="radio"
              name={groupName}
              value={option.value}
              checked={value === option.value}
              onChange={() => onChange(option.value)}
              disabled={disabled}
            />
            <span>{option.label}</span>
          </label>
        ))}
      </div>
      {hint && <small className={styles.hint}>{hint}</small>}
    </fieldset>
  );
}

export default RadioGroup;
