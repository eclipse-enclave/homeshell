import { type KeyboardEvent, useState } from 'react';
import { useCombobox } from './combobox';
import { fuzzyFilter, fuzzyScore } from './fuzzy';
import styles from './suggest-input.module.css';
import { titleWhenTruncated } from './truncated-title';

export interface SuggestInputProps {
  /** Accessible name of the input. */
  label: string;
  value: string;
  onChange: (value: string) => void;
  /** Values to offer; any other text is fine too. */
  suggestions: readonly string[];
  /** Takes no input, but stays focusable. */
  disabled: boolean;
  className?: string;
  'aria-invalid'?: boolean;
  'aria-describedby'?: string;
}

/**
 * A text input that suggests values in a list below it. Typing lists the
 * suggestions matching the text; Down or a click on the input lists them all.
 * Up/Down move the highlight, Enter or a click picks one, Escape closes the
 * list and nothing else, and with nothing highlighted Enter is the form's.
 */
export function SuggestInput({
  label,
  value,
  onChange,
  suggestions,
  disabled,
  className,
  ...aria
}: SuggestInputProps) {
  const [open, setOpen] = useState(false);
  const [filtered, setFiltered] = useState(false);
  const items = filtered
    ? fuzzyFilter(suggestions, (suggestion) => fuzzyScore(value, suggestion))
    : suggestions;
  const combobox = useCombobox({
    items,
    keyOf: (_item, index) => String(index),
    onPick: (item) => {
      onChange(item);
      setOpen(false);
    },
    open,
    initialIndex: -1,
  });

  const show = (all: boolean) => {
    setFiltered(!all);
    setOpen(true);
    combobox.setActive(all ? suggestions.indexOf(value) : -1);
  };

  const handleKeyDown = (event: KeyboardEvent) => {
    if (event.nativeEvent.isComposing || disabled) return;
    if (!combobox.expanded) {
      if (event.key === 'ArrowDown') {
        event.preventDefault();
        show(true);
      }
      return;
    }
    if (combobox.handleKeyDown(event)) return;
    if (event.key === 'Escape') {
      // The list's, not the form's: closing it must not cancel the form.
      event.preventDefault();
      event.stopPropagation();
      setOpen(false);
    }
  };

  return (
    <div className={styles.suggest}>
      <input
        className={className}
        type="text"
        {...combobox.inputProps}
        aria-label={label}
        {...aria}
        value={value}
        onChange={(event) => {
          onChange(event.target.value);
          show(false);
        }}
        onKeyDown={handleKeyDown}
        onPointerDown={() => {
          if (!disabled && !combobox.expanded) show(true);
        }}
        onBlur={() => setOpen(false)}
        readOnly={disabled}
        aria-disabled={disabled}
        autoComplete="off"
      />
      <ul
        {...combobox.listProps}
        className={styles.list}
        hidden={!combobox.expanded}
      >
        {combobox.expanded &&
          items.map((item, index) => (
            // Suggestions can repeat, and the option IDs are positional too.
            <li
              key={index}
              {...combobox.optionProps(index)}
              className={styles.option}
              {...titleWhenTruncated(item)}
            >
              {item}
            </li>
          ))}
      </ul>
    </div>
  );
}

export default SuggestInput;
