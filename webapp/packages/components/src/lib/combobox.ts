import {
  type KeyboardEvent,
  type MouseEvent,
  useEffect,
  useId,
  useRef,
  useState,
} from 'react';

export interface ComboboxOptions<T> {
  /** The options listed, already filtered and ordered. */
  items: readonly T[];
  /** Unique among items; the option's DOM id suffix. */
  keyOf: (item: T, index: number) => string;
  onPick: (item: T) => void;
  /** Whether the list is showing. */
  open: boolean;
  /** The option highlighted at first, -1 for none. */
  initialIndex: number;
}

/**
 * The ARIA combobox pattern's wiring, for a text box that drives a list of
 * options: the box keeps focus and points at the highlighted option with
 * aria-activedescendant, Up/Down move the highlight (wrapping at the ends),
 * Enter picks it, and a click picks an option; nothing in the list takes focus
 * from the box.
 * Keys an input method is composing are the caller's to leave alone.
 */
export function useCombobox<T>({
  items,
  keyOf,
  onPick,
  open,
  initialIndex,
}: ComboboxOptions<T>) {
  const [activeIndex, setActive] = useState(initialIndex);
  const listRef = useRef<HTMLUListElement>(null);
  const listId = useId();
  const active = Math.min(activeIndex, items.length - 1);
  const expanded = open && items.length > 0;
  const optionId = (index: number) => `${listId}-${keyOf(items[index], index)}`;

  // On opening too: the list renders afresh, scrolled to its top, and the
  // highlight it opens on may be the one it closed on.
  useEffect(() => {
    listRef.current?.children[active]?.scrollIntoView({ block: 'nearest' });
  }, [active, expanded]);

  /** Handles Up, Down and Enter on an open list; says whether it did. */
  const handleKeyDown = (event: KeyboardEvent): boolean => {
    const last = items.length - 1;
    switch (event.key) {
      case 'ArrowDown':
        setActive(active >= last ? 0 : active + 1);
        break;
      case 'ArrowUp':
        setActive(active <= 0 ? last : active - 1);
        break;
      case 'Enter':
        if (!items[active]) return false;
        onPick(items[active]);
        break;
      default:
        return false;
    }
    event.preventDefault();
    return true;
  };

  return {
    active,
    setActive,
    expanded,
    handleKeyDown,
    inputProps: {
      role: 'combobox',
      'aria-controls': listId,
      'aria-expanded': expanded,
      'aria-autocomplete': 'list',
      'aria-activedescendant':
        expanded && items[active] ? optionId(active) : undefined,
    } as const,
    listProps: {
      ref: listRef,
      id: listId,
      role: 'listbox',
      // Nothing in the list takes focus from the box: not an option, not its
      // scrollbar or padding.
      onMouseDown: (event: MouseEvent) => event.preventDefault(),
    } as const,
    optionProps: (index: number) =>
      ({
        id: optionId(index),
        role: 'option',
        'aria-selected': index === active,
        onClick: () => onPick(items[index]),
      }) as const,
  };
}
