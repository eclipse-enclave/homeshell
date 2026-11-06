import { useId, useState } from 'react';
import { FormField } from './form-field';
import styles from './form-controls.module.css';

export interface NumberFieldProps {
  label: string;
  value: number;
  onChange: (value: number) => void;
  /**
   * Called on blur with the clamped value, in addition to `onChange`. Lets a
   * caller defer an expensive/validated commit (e.g. a daemon PUT) to blur while
   * `onChange` only tracks the in-progress edit — so a transient out-of-range
   * keystroke never round-trips. When omitted, `onChange` alone is the commit.
   */
  onCommit?: (value: number) => void;
  hint?: string;
  badge?: string;
  /** Validation message shown beneath the input. */
  error?: string;
  min?: number;
  max?: number;
  /**
   * When set, `onChange` fires only on blur (with the clamped value) rather than
   * on every keystroke. Use this for daemon-backed fields so an intermediate
   * value typed on the way to the final one (e.g. `0` while typing `0.5`) is not
   * round-tripped and rejected.
   */
  commitOnBlur?: boolean;
  disabled?: boolean;
  id?: string;
}

// Digits with at most one leading sign and one decimal point; also matches the
// empty and partial ("0.") states so a fractional value can be typed.
const NUMERIC = /^-?\d*\.?\d*$/;

/**
 * Labelled numeric input. It holds the raw text locally (rendered as a
 * `type="text"` field with a decimal input mode) so partial entries like `0.`
 * survive editing; a `type="number"` field would sanitize them away on the
 * controlled round-trip. A value outside `min`/`max` is clamped to the nearest
 * bound **on blur**, not while typing — clamping mid-keystroke would make a
 * bound like `min={100}` impossible to type toward.
 */
export function NumberField({
  label,
  value,
  onChange,
  onCommit,
  hint,
  badge,
  error,
  min,
  max,
  commitOnBlur,
  disabled,
  id,
}: NumberFieldProps) {
  const generatedId = useId();
  const controlId = id ?? generatedId;

  const [text, setText] = useState(() => toText(value));
  // Adopt an external value change (e.g. reset to defaults) without clobbering
  // an in-progress edit. While the field is focused the user owns the text, so
  // skip the resync entirely — otherwise a partial entry that parses to a
  // different number (e.g. `0.`, which parses to 0) would be overwritten by an
  // external update mid-edit. When unfocused, resync only if the shown text
  // does not already mean the incoming number.
  const [focused, setFocused] = useState(false);
  const [prevValue, setPrevValue] = useState(value);
  if (value !== prevValue) {
    setPrevValue(value);
    if (!focused && parseNumber(text) !== value) setText(toText(value));
  }

  return (
    <FormField label={label} htmlFor={controlId} hint={hint} badge={badge} error={error}>
      <input
        id={controlId}
        className={styles.input}
        type="text"
        inputMode="decimal"
        value={text}
        onFocus={() => setFocused(true)}
        onChange={(e) => {
          const raw = e.target.value;
          if (!NUMERIC.test(raw)) return; // ignore non-numeric keystrokes
          setText(raw);
          if (!commitOnBlur) {
            const next = parseNumber(raw);
            if (next !== undefined) onChange(next);
          }
        }}
        onBlur={(e) => {
          setFocused(false);
          const parsed = parseNumber(e.target.value);
          if (parsed === undefined) {
            setText(toText(value)); // empty/invalid on blur → last good value
            return;
          }
          const clamped = clamp(parsed, min, max);
          setText(toText(clamped));
          if (commitOnBlur) {
            if (clamped !== value) onChange(clamped);
          } else if (clamped !== parsed) {
            onChange(clamped);
          }
          // A caller may defer an expensive/validated commit (e.g. a daemon PUT)
          // to blur with the already-clamped value, so a transient out-of-range
          // keystroke never round-trips.
          onCommit?.(clamped);
        }}
        disabled={disabled}
      />
    </FormField>
  );
}

function toText(value: number): string {
  return Number.isFinite(value) ? String(value) : '';
}

/** The numeric value of a field string, or undefined when empty or not a number. */
function parseNumber(raw: string): number | undefined {
  const trimmed = raw.trim();
  if (trimmed === '') return undefined;
  const n = Number(trimmed);
  return Number.isNaN(n) ? undefined : n;
}

/** Constrains a value to `[min, max]` (either bound optional). */
function clamp(value: number, min?: number, max?: number): number {
  if (min !== undefined && value < min) return min;
  if (max !== undefined && value > max) return max;
  return value;
}

export default NumberField;
