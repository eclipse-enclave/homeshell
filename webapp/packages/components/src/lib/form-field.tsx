import type { ReactNode } from 'react';
import styles from './form-controls.module.css';

export interface FormFieldProps {
  label: string;
  /** Associates the label with a control rendered as a child. */
  htmlFor?: string;
  hint?: string;
  /** Short tag shown next to the label, e.g. "applies when supported". */
  badge?: string;
  error?: string;
  children: ReactNode;
}

/**
 * Label + optional hint/badge/error frame for a single form control. The
 * control is rendered as a child so the same frame serves selects, text and
 * number inputs alike.
 */
export function FormField({
  label,
  htmlFor,
  hint,
  badge,
  error,
  children,
}: FormFieldProps) {
  return (
    <div className={styles.field}>
      <div className={styles.labelRow}>
        <label className={styles.label} htmlFor={htmlFor}>
          {label}
        </label>
        {badge && (
          <span className={styles.badge} title={badge}>
            {badge}
          </span>
        )}
      </div>
      {children}
      {hint && <small className={styles.hint}>{hint}</small>}
      {error && (
        <p className={styles.error} role="alert">
          {error}
        </p>
      )}
    </div>
  );
}

export default FormField;
