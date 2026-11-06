import { type KeyboardEvent, useRef } from 'react';
import { useCombobox } from './combobox';
import { type FocusTarget, useModalFocus } from './focus-return';
import { handleModalKeys } from './focus-trap';
import { Kbd } from './kbd';
import styles from './quick-pick.module.css';
import { titleWhenTruncated } from './truncated-title';

export interface QuickPickItem {
  /** Unique within items; also the option's DOM id suffix. */
  id: string;
  label: string;
  /** Secondary text beside the label, e.g. where the item lives. */
  detail?: string;
  /** The item's keyboard shortcut, already formatted for display. */
  shortcut?: string;
  /**
   * The span of label, as [start, end) offsets, that gives way first when the
   * row runs short, cut off while the rest stays in view: a long name before
   * its verb. Matching and the tooltip still have the label in full.
   */
  elided?: readonly [start: number, end: number];
}

function Label({ label, elided }: Pick<QuickPickItem, 'label' | 'elided'>) {
  if (!elided) {
    return (
      <span className={styles.label} {...titleWhenTruncated(label)}>
        {label}
      </span>
    );
  }
  const [start, end] = elided;
  return (
    <span className={`${styles.label} ${styles.split}`}>
      {label.slice(0, start)}
      <span className={styles.elided} {...titleWhenTruncated(label)}>
        {label.slice(start, end)}
      </span>
      {label.slice(end)}
    </span>
  );
}

export interface QuickPickProps<T extends QuickPickItem> {
  /** Accessible name of the dialog and its input. */
  label: string;
  value: string;
  onValueChange: (value: string) => void;
  /** What to list for the current value, already filtered and ordered. */
  items: readonly T[];
  onSelect: (item: T) => void;
  onClose: () => void;
  placeholder?: string;
  /** Shown instead of the list when nothing matches. */
  emptyText: string;
  /**
   * Where focus goes on close when the element it came from has left the
   * page meanwhile (a menu item, say, whose menu closed as this opened). Read
   * once, when it opens.
   */
  fallbackFocus?: FocusTarget;
  /**
   * Lets the app's keyboard shortcuts through, which every other modal keeps
   * from what it was opened over: the command palette's own keys switch it.
   */
  passShortcuts?: boolean;
}

/**
 * A VS Code-style quick pick: a modal text box over a list it filters, driven
 * entirely from the box. Up/Down move the highlight (the input keeps focus and
 * points at it with aria-activedescendant), Enter picks, Escape or a click
 * outside closes and focus goes back to where it was.
 */
export function QuickPick<T extends QuickPickItem>({
  label,
  value,
  onValueChange,
  items,
  onSelect,
  onClose,
  placeholder,
  emptyText,
  fallbackFocus,
  passShortcuts,
}: QuickPickProps<T>) {
  const inputRef = useRef<HTMLInputElement>(null);
  const combobox = useCombobox({
    items,
    keyOf: (item) => item.id,
    onPick: onSelect,
    open: true,
    initialIndex: 0,
  });
  useModalFocus(() => inputRef.current, fallbackFocus);

  const handleKeyDown = (event: KeyboardEvent<HTMLElement>) => {
    if (!handleModalKeys(event, onClose)) combobox.handleKeyDown(event);
  };

  return (
    <div
      className={styles.overlay}
      onKeyDown={handleKeyDown}
      onClick={(event) => {
        if (event.target === event.currentTarget) onClose();
      }}
      role="dialog"
      aria-modal="true"
      aria-label={label}
      data-pass-shortcuts={passShortcuts || undefined}
    >
      <div className={styles.panel}>
        <input
          ref={inputRef}
          className={styles.input}
          type="text"
          {...combobox.inputProps}
          aria-label={label}
          placeholder={placeholder}
          value={value}
          onChange={(event) => {
            combobox.setActive(0);
            onValueChange(event.target.value);
          }}
        />
        <ul {...combobox.listProps} className={styles.list}>
          {items.map((item, index) => (
            <li
              key={item.id}
              {...combobox.optionProps(index)}
              className={styles.option}
              // Spelled out: read from its boxes, a label split around its
              // elided span would be read in pieces.
              aria-label={[item.label, item.detail, item.shortcut]
                .filter(Boolean)
                .join(' ')}
            >
              <Label label={item.label} elided={item.elided} />
              {item.detail && (
                <span
                  className={styles.detail}
                  {...titleWhenTruncated(item.detail)}
                >
                  {item.detail}
                </span>
              )}
              {item.shortcut && (
                <Kbd className={styles.shortcut}>{item.shortcut}</Kbd>
              )}
            </li>
          ))}
        </ul>
        {/* Always present, so a screen reader announces the text appearing. */}
        <p className={styles.empty} role="status">
          {items.length === 0 ? emptyText : ''}
        </p>
      </div>
    </div>
  );
}

export default QuickPick;
