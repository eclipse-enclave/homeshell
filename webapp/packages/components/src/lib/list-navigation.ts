import type { Workstream } from '@homeshell/model';
import {
  type FocusEvent,
  type KeyboardEvent,
  useId,
  useLayoutEffect,
  useRef,
  useState,
} from 'react';
import type { FocusTarget } from './focus-return';
import { ariaKeys, isMac, isModified, type KeyHelp, platformKeys } from './keys';

const NAV_ITEM_SELECTOR = '[data-nav-id]';

function navItems(root: ParentNode): HTMLElement[] {
  return Array.from(root.querySelectorAll<HTMLElement>(NAV_ITEM_SELECTOR));
}

/** The card or row `element` is, or is inside of. */
export function closestNavItem(element: Element): HTMLElement | null {
  return element.closest<HTMLElement>(NAV_ITEM_SELECTOR);
}

/** Focuses the card or row under `root` whose workstream or session ID is `id`. */
export function focusNavItem(root: ParentNode, id: string) {
  navItems(root)
    .find((item) => item.dataset.navId === id)
    ?.focus();
}

/** The card or row under `root` that holds the list's tab stop, if any. */
export function tabStopItem(root: ParentNode): HTMLElement | undefined {
  return navItems(root).find((item) => item.tabIndex === 0);
}

/** A key of the workstream list, described for the user. */
export interface ListKey extends KeyHelp {
  /** The items that handle it themselves; unset for the list's own keys. */
  on?: readonly ('card' | 'session')[];
}

/**
 * The keys of the workstream list (handleListKeyDown, and WorkstreamCard and
 * SessionRow for the focused item's own).
 */
export const LIST_KEYS: readonly ListKey[] = [
  {
    keys: ['arrowup', 'arrowdown'],
    label: 'Move between workstreams and sessions',
  },
  { keys: ['home', 'end'], label: 'Go to the first or last item' },
  {
    keys: ['arrowright'],
    label: 'Expand, or go to the first session',
    on: ['session'],
  },
  {
    keys: ['arrowleft'],
    label: 'Collapse, or go to the workstream',
    on: ['session'],
  },
  {
    keys: ['enter'],
    label: 'Open the focused workstream or session',
    on: ['card', 'session'],
  },
  {
    keys: ['f2'],
    label: 'Rename the focused workstream or session',
    on: ['card', 'session'],
  },
  {
    keys: ['delete'],
    mac: ['delete', 'mod+backspace'],
    label: 'Delete the focused workstream or session',
    on: ['card', 'session'],
  },
  {
    keys: ['shift+f10'],
    label: 'Open the actions menu',
    on: ['card', 'session'],
  },
];

/** The keys a focused `item` of the list answers, as aria-keyshortcuts. */
export function listItemKeyShortcuts(
  item: 'card' | 'session',
  mac = isMac(),
): string {
  return LIST_KEYS.filter((key) => key.on?.includes(item))
    .flatMap((key) => platformKeys(key, mac).map((keys) => ariaKeys(keys, mac)))
    .join(' ');
}

/** Every key of the list and what it does, for a screen reader. */
export function describeListKeys(mac = isMac()): string {
  return LIST_KEYS.map(
    (key) =>
      `${platformKeys(key, mac)
        .map((keys) => ariaKeys(keys, mac))
        .join(' or ')}: ${key.label}.`,
  ).join(' ');
}

/**
 * The tabIndex of the controls in a list item whose own is `itemTabIndex`: Tab
 * reaches them only from the item holding the list's tab stop, so that from
 * there it leaves the list rather than walking every item's controls.
 */
export function controlTabIndexFor(itemTabIndex: number | undefined) {
  return itemTabIndex === -1 ? -1 : undefined;
}

/**
 * Moves focus across the workstream cards and their session rows as one tree,
 * the way VS Code's explorer does: Up/Down/Home/End walk the items in reading
 * order, Left goes to a row's card and Right to a card's first row. Only
 * focus moves here; what Enter, F2 or Delete do to the focused item is the
 * item's own business, and a key it already handled arrives prevented.
 */
export function handleListKeyDown(event: KeyboardEvent<HTMLElement>) {
  const item = event.target as HTMLElement;
  if (
    event.defaultPrevented ||
    isModified(event) ||
    !item.matches(NAV_ITEM_SELECTOR)
  ) {
    return;
  }
  const items = navItems(event.currentTarget);
  const index = items.indexOf(item);
  const targets: Record<string, HTMLElement | null | undefined> = {
    ArrowDown: items[index + 1],
    ArrowUp: items[index - 1],
    Home: items[0],
    End: items[items.length - 1],
    ArrowLeft: item.parentElement?.closest<HTMLElement>(NAV_ITEM_SELECTOR),
    ArrowRight: item.querySelector<HTMLElement>(NAV_ITEM_SELECTOR),
  };
  const target = targets[event.key];
  if (!target) return;
  event.preventDefault();
  target.focus();
}

/** A card's items in the list: the card, then its session rows. */
const cardItems = (workstream: Workstream) => [
  workstream.id,
  ...workstream.sessions.map((session) => session.id),
];

/** Where the list's tab stop was put: the item, and where it sat. */
interface TabStop {
  id: string;
  index: number;
  workstreamId: string;
  /** Among its card's items. */
  position: number;
}

function placeTabStop(
  id: string,
  workstreams: readonly Workstream[],
): TabStop | null {
  for (const workstream of workstreams) {
    const position = cardItems(workstream).indexOf(id);
    if (position !== -1) {
      const index = workstreams.flatMap(cardItems).indexOf(id);
      return { id, index, workstreamId: workstream.id, position };
    }
  }
  return null;
}

/**
 * The item holding the tab stop: the one it was put on or, when that has gone,
 * whatever took its place — in its own card first, since a promoted session is
 * listed before the placeholder it replaces goes, else in the whole list.
 */
function tabStopHolder(
  stop: TabStop | null,
  workstreams: readonly Workstream[],
): string | undefined {
  const navIds = workstreams.flatMap(cardItems);
  if (!stop) return navIds[0];
  if (navIds.includes(stop.id)) return stop.id;
  const card = workstreams.find(
    (workstream) => workstream.id === stop.workstreamId,
  );
  if (card) {
    const items = cardItems(card);
    return items[Math.min(stop.position, items.length - 1)];
  }
  return navIds[Math.min(stop.index, navIds.length - 1)];
}

export interface ListTabStopOptions {
  /** A new value starts the list over, with nothing selected. */
  resetKey: string;
  /** Takes focus when the list is empty, and when its last item goes. */
  emptyFocus: FocusTarget;
}

/**
 * The workstream list's one tab stop (roving tabindex), its keys and its
 * selection, for the element around the cards: spread `listProps` on it, have
 * it described by `describedBy`, and give each card `tabStopId` and
 * `selectedId`.
 *
 * The tab stop follows focus, into an item's controls too, so Tab comes back
 * to where the user left off. Once focus has been in the list, the item
 * holding it is the selection that commands on it act on, shown as selected
 * after focus has left too. Focus that falls to <body> from inside the list
 * goes to the item holding the tab stop — whatever took the place of one that
 * went, as in VS Code's explorer — or to `emptyFocus` when none is left.
 */
export function useListTabStop(
  workstreams: readonly Workstream[],
  { resetKey, emptyFocus }: ListTabStopOptions,
) {
  const listRef = useRef<HTMLDivElement>(null);
  const describedBy = useId();
  // Whether focus is somewhere in the list, or was when the focused item went
  // away. A click onto blank page clears it, so that <body> keeps focus then.
  const hasFocus = useRef(false);
  const [stop, setStop] = useState<TabStop | null>(null);
  const tabStopId = tabStopHolder(stop, workstreams);

  // A layout effect, so that it runs before an effect of the caller's that
  // focuses an item of what the reset brought in, and so gives it the stop.
  useLayoutEffect(() => setStop(null), [resetKey]);

  const recoverFocus = (list: HTMLElement) => {
    if (tabStopId) focusNavItem(list, tabStopId);
  };

  useLayoutEffect(() => {
    if (!hasFocus.current || document.activeElement !== document.body) return;
    if (listRef.current) {
      recoverFocus(listRef.current);
    } else {
      hasFocus.current = false;
      emptyFocus()?.focus();
    }
  });

  return {
    listRef,
    tabStopId,
    selectedId: stop ? tabStopId : undefined,
    describedBy,
    /** Focuses the item `itemId` names, else the tab stop's, else `emptyFocus`. */
    focus: (itemId?: string) => {
      const id = itemId ?? tabStopId;
      if (listRef.current && id) focusNavItem(listRef.current, id);
      else emptyFocus()?.focus();
    },
    listProps: {
      ref: listRef,
      onKeyDown: handleListKeyDown,
      onFocus: (event: FocusEvent<HTMLElement>) => {
        // A portalled menu's focus bubbles here through React too, but its
        // outside click unmounts it before any blur could say focus left.
        if (!event.currentTarget.contains(event.target)) return;
        hasFocus.current = true;
        const id = closestNavItem(event.target)?.dataset.navId;
        if (id) setStop(placeTabStop(id, workstreams));
      },
      onBlur: (event: FocusEvent<HTMLElement>) => {
        // A control disabled while it has focus drops it without a render to
        // follow, so the recovery above cannot catch it.
        if (event.target.matches(':disabled')) {
          recoverFocus(event.currentTarget);
          return;
        }
        // A removed item blurs too, but it is no longer connected.
        if (
          event.target.isConnected &&
          !event.currentTarget.contains(event.relatedTarget)
        ) {
          hasFocus.current = false;
        }
      },
    },
  };
}
