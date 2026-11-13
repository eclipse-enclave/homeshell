import {
  Fragment,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type KeyboardEvent as ReactKeyboardEvent,
  type ReactNode,
} from 'react';
import { createPortal } from 'react-dom';
import { Button } from './button';
import { ariaKeys, formatKeys, nextIndex } from './keys';
import { MnemonicLabel } from './mnemonic';
import styles from './menu-button.module.css';

export interface MenuButtonItem {
  /** Opaque key echoed back to onSelect; never interpreted here. */
  id: string;
  label: string;
  /** Starts a group of its own, set off from the items above by a separator. */
  separated?: boolean;
  /** Keys that run it outside the menu, shown after the label as VS Code does. */
  shortcut?: string;
}

export interface MenuButtonProps {
  label: string;
  /** Drawn before the label. */
  icon?: ReactNode;
  items: MenuButtonItem[];
  onSelect: (id: string) => void;
  /**
   * The control's default action. When set, the component is a split button: the
   * labelled button fires this and a separate caret opens the menu.
   */
  onPrimary?: () => void;
  /** Accessible name for the trigger; defaults to the visible label. */
  ariaLabel?: string;
  title?: string;
  disabled?: boolean;
  /** For its buttons: -1 takes them out of the Tab order. */
  tabIndex?: number;
  /**
   * The letter Alt presses it by (see mnemonic.tsx): the default action in
   * split mode, with Alt+Shift opening the menu, else what opens the menu.
   */
  mnemonic?: string;
}

/** Gap between the anchor and the menu, in pixels. */
const MENU_OFFSET = 4;

export interface MenuProps {
  /** Accessible name of the menu. */
  label: string;
  items: readonly MenuButtonItem[];
  /** What the menu hangs under, right-aligned, or above it without room. */
  anchor: HTMLElement;
  /** The item that has focus when it opens. */
  initialIndex?: number;
  onSelect: (id: string) => void;
  /**
   * Asks the owner to close it. `returnFocus` is set when focus should go back
   * to where the menu was opened from — Escape, Tab, a choice — and unset when
   * it already went somewhere else, with a click outside say.
   */
  onClose: (returnFocus: boolean) => void;
}

/**
 * A menu of data-driven choices, driven by the keyboard as VS Code's are: focus
 * is on an item, the arrows (and Home and End) move it, Enter or Space
 * chooses, and Escape, Tab or a click elsewhere closes. It is portalled and
 * positioned `fixed`, since an ancestor card sets `overflow: hidden` and would
 * clip an absolutely-positioned one.
 */
export function Menu({
  label,
  items,
  anchor,
  initialIndex = 0,
  onSelect,
  onClose,
}: MenuProps) {
  const [position, setPosition] = useState({ top: 0, left: 0 });
  const [activeIndex, setActiveIndex] = useState(initialIndex);
  const menuRef = useRef<HTMLUListElement>(null);
  const itemRefs = useRef<(HTMLButtonElement | null)[]>([]);
  const close = useRef(onClose);
  useLayoutEffect(() => {
    close.current = onClose;
  });

  // The items are live data, so the index can outlive the item it pointed at.
  const active = Math.min(activeIndex, Math.max(items.length - 1, 0));

  // Focus follows the active item, which is what makes the menu keyboard-usable
  // at all: on open it lands on an item, and arrows move real focus.
  useLayoutEffect(() => {
    itemRefs.current[active]?.focus();
  }, [active]);

  // Measured before paint so the menu never shows at a stale position first.
  useLayoutEffect(() => {
    const rect = anchor.getBoundingClientRect();
    const height = menuRef.current?.offsetHeight ?? 0;
    const width = menuRef.current?.offsetWidth ?? rect.width;
    const below = window.innerHeight - rect.bottom;
    const flip =
      height > 0 && below < height + MENU_OFFSET && rect.top > height;
    setPosition({
      top: flip ? rect.top - height - MENU_OFFSET : rect.bottom + MENU_OFFSET,
      // Clamped so it cannot leave the viewport.
      left: Math.max(MENU_OFFSET, rect.right - width),
    });
  }, [anchor, items.length]);

  // Closing by itself, focus goes back only if it was in the menu: on one of
  // its items, or fallen to <body> with an item that went away.
  const holdsFocus = () =>
    document.activeElement === document.body ||
    (menuRef.current?.contains(document.activeElement) ?? false);

  // The items can run out while it is open.
  useEffect(() => {
    if (items.length === 0) close.current(holdsFocus());
  }, [items.length]);

  // Close on an outside click. A scroll or resize moves the anchor out from
  // under a fixed menu, so close then too — but only a scroll of something
  // holding the anchor: a pane beside it scrolls when its rows change, and
  // leaves the anchor where it was.
  useEffect(() => {
    const onPointerDown = (event: MouseEvent) => {
      const target = event.target as Node;
      if (anchor.contains(target) || menuRef.current?.contains(target)) return;
      close.current(false);
    };
    // Focus landing outside the menu — Tab, or a click on another control —
    // closes it. The initial focus into the menu happens in a layout effect,
    // before this listener exists, so it cannot close the menu it just opened.
    const onFocusIn = (event: FocusEvent) => {
      const target = event.target as Node;
      if (target === anchor || menuRef.current?.contains(target)) return;
      close.current(false);
    };
    const onViewportChange = () => close.current(holdsFocus());
    const onScroll = (event: Event) => {
      if (event.target instanceof Node && event.target.contains(anchor)) {
        onViewportChange();
      }
    };
    document.addEventListener('mousedown', onPointerDown);
    document.addEventListener('focusin', onFocusIn);
    // Capture: a scroll inside a pane does not bubble to the document.
    window.addEventListener('scroll', onScroll, true);
    window.addEventListener('resize', onViewportChange);
    return () => {
      document.removeEventListener('mousedown', onPointerDown);
      document.removeEventListener('focusin', onFocusIn);
      window.removeEventListener('scroll', onScroll, true);
      window.removeEventListener('resize', onViewportChange);
    };
  }, [anchor]);

  const select = (id: string) => {
    onClose(true);
    onSelect(id);
  };

  // Its keys go no further than a modal's: portalled, the menu still bubbles
  // them through the item it was opened from, whose own keys they are not.
  const onMenuKeyDown = (event: ReactKeyboardEvent<HTMLUListElement>) => {
    event.stopPropagation();
    const next = nextIndex(event.key, active, items.length, { wrap: true });
    if (next !== undefined) {
      event.preventDefault();
      setActiveIndex(next);
      return;
    }
    switch (event.key) {
      case 'Enter':
      case ' ':
        // Taken over from the button's native activation, so Space cannot also
        // scroll the page behind the menu.
        event.preventDefault();
        select(items[active].id);
        break;
      case 'Escape':
        event.preventDefault();
        onClose(true);
        break;
      case 'Tab':
        onClose(true);
        break;
      default:
        break;
    }
  };

  return createPortal(
    <ul
      className={styles.menu}
      role="menu"
      aria-label={label}
      ref={menuRef}
      style={{ top: position.top, left: position.left }}
      onKeyDown={onMenuKeyDown}
    >
      {items.map((item, index) => (
        <Fragment key={item.id}>
          {item.separated && index > 0 && (
            <li role="separator" className={styles.separator} />
          )}
          <li role="none">
            <button
              type="button"
              role="menuitem"
              className={styles.item}
              // Roving tabindex: one stop for the menu, not one per item.
              tabIndex={index === active ? 0 : -1}
              ref={(node) => {
                itemRefs.current[index] = node;
              }}
              onClick={() => select(item.id)}
              aria-keyshortcuts={
                item.shortcut ? ariaKeys(item.shortcut) : undefined
              }
            >
              {item.label}
              {item.shortcut && (
                // Said by aria-keyshortcuts, not as part of the item's name.
                <span className={styles.shortcut} aria-hidden="true">
                  {formatKeys(item.shortcut)}
                </span>
              )}
            </button>
          </li>
        </Fragment>
      ))}
    </ul>,
    document.body,
  );
}

/**
 * A button that opens a Menu of data-driven choices. Without `onPrimary` a
 * single item renders as a plain button; zero items render nothing either way.
 */
export function MenuButton({
  label,
  icon,
  items,
  onSelect,
  onPrimary,
  ariaLabel,
  title,
  disabled,
  tabIndex,
  mnemonic,
}: MenuButtonProps) {
  // The item focused on opening, while the menu is open.
  const [openAt, setOpenAt] = useState<number | null>(null);
  // The button that opens the menu: the caret in split mode, the whole control
  // otherwise. The menu hangs under it, and focus goes back to it on close.
  const [trigger, setTrigger] = useState<HTMLButtonElement | null>(null);

  // The items are live data and can shrink while the menu is open. The threshold
  // matches what the render below switches on, so the menu never stays open
  // behind a control that is no longer a menu.
  const menuThreshold = onPrimary ? 1 : 2;
  useEffect(() => {
    if (items.length < menuThreshold) setOpenAt(null);
  }, [items.length, menuThreshold]);

  if (items.length === 0) return null;

  const content = (
    <>
      {icon}
      <MnemonicLabel label={label} letter={mnemonic} />
    </>
  );

  if (!onPrimary && items.length === 1) {
    const only = items[0];
    return (
      <Button
        className={styles.trigger}
        onClick={() => onSelect(only.id)}
        aria-label={ariaLabel ?? label}
        title={title}
        disabled={disabled}
        tabIndex={tabIndex}
        mnemonic={mnemonic}
      >
        {content}
      </Button>
    );
  }

  const open = openAt !== null;
  const menu = open && trigger && (
    <Menu
      label={ariaLabel ?? label}
      items={items}
      anchor={trigger}
      initialIndex={openAt}
      onSelect={onSelect}
      onClose={(returnFocus) => {
        setOpenAt(null);
        if (returnFocus) trigger.focus();
      }}
    />
  );

  // Enter and Space arrive as a click on the native button; the arrows do not.
  const onTriggerKeyDown = (event: ReactKeyboardEvent<HTMLButtonElement>) => {
    if (event.key === 'ArrowDown') {
      event.preventDefault();
      setOpenAt(0);
    } else if (event.key === 'ArrowUp') {
      event.preventDefault();
      setOpenAt(items.length - 1);
    }
  };

  const toggle = () => setOpenAt(open ? null : 0);

  if (onPrimary) {
    return (
      <div className={styles.wrapper}>
        <Button
          className={`${styles.trigger} ${styles.splitPrimary}`}
          onClick={() => onPrimary()}
          aria-label={ariaLabel ?? label}
          title={title}
          disabled={disabled}
          tabIndex={tabIndex}
          mnemonic={mnemonic}
        >
          {content}
        </Button>
        <Button
          ref={setTrigger}
          className={styles.splitCaret}
          onClick={toggle}
          onKeyDown={onTriggerKeyDown}
          aria-label={`${ariaLabel ?? label} with…`}
          aria-haspopup="menu"
          aria-expanded={open}
          title={title}
          disabled={disabled}
          tabIndex={tabIndex}
          mnemonic={mnemonic?.toUpperCase()}
        >
          <span className={styles.caret} aria-hidden="true">
            ▾
          </span>
        </Button>
        {menu}
      </div>
    );
  }

  return (
    <div className={styles.wrapper}>
      <Button
        ref={setTrigger}
        className={styles.trigger}
        onClick={toggle}
        onKeyDown={onTriggerKeyDown}
        aria-label={ariaLabel ?? label}
        aria-haspopup="menu"
        aria-expanded={open}
        title={title}
        disabled={disabled}
        tabIndex={tabIndex}
        mnemonic={mnemonic}
      >
        {content}
        <span className={styles.caret} aria-hidden="true">
          ▾
        </span>
      </Button>
      {menu}
    </div>
  );
}

export default MenuButton;
