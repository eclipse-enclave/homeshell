import { type KeyboardEvent, type MouseEvent, useState } from 'react';
import { closestNavItem } from './list-navigation';
import { Menu } from './menu-button';

/**
 * Something a workstream card or session row offers. One list per item feeds
 * its buttons and keys, its actions menu and the app's commands for it, so
 * they cannot disagree about what is on offer.
 */
export interface ItemAction {
  id: string;
  label: string;
  run: () => void;
  /** Its control is shown but disabled; the menu and the commands leave it out. */
  disabled?: boolean;
  /** Deletes or removes: the menu lists it last, set apart. */
  destructive?: boolean;
  /** The key that runs it on the focused item, which the menu shows beside it. */
  shortcut?: string;
}

/** `actions` as they are offered right now: the ones not disabled. */
export function offeredActions(actions: readonly ItemAction[]): ItemAction[] {
  return actions.filter((action) => !action.disabled);
}

/**
 * Runs the action `id` a key asks for, if `actions` offer it, and takes the
 * key. Reports whether it did.
 */
export function runAction(
  event: KeyboardEvent,
  actions: readonly ItemAction[],
  id: string,
): boolean {
  const action = offeredActions(actions).find((entry) => entry.id === id);
  if (!action) return false;
  event.preventDefault();
  action.run();
  return true;
}

/**
 * The focused item's actions as a menu, opened as a context menu is everywhere
 * else. `openOn` takes the item's keydown, Shift+F10, and reports whether it
 * opened the menu; `openOnContextMenu` its contextmenu event, which the menu
 * key and a right-click raise. Either hangs the menu under `anchor`. Focus goes
 * back to the item when the menu closes by keyboard or a choice. What deletes
 * or removes comes last, after a separator.
 */
export function useActionsMenu(label: string, actions: readonly ItemAction[]) {
  const [open, setOpen] = useState<{
    anchor: HTMLElement;
    item: HTMLElement;
  } | null>(null);
  const offered = offeredActions(actions);
  const safe = offered.filter((action) => !action.destructive);
  const destructive = offered.filter((action) => action.destructive);
  const items = [
    ...safe,
    ...destructive.map((action, index) => ({
      ...action,
      separated: index === 0,
    })),
  ];

  const show = (
    event: KeyboardEvent<HTMLElement> | MouseEvent<HTMLElement>,
    anchor: HTMLElement | null,
  ): boolean => {
    if (offered.length === 0) return false;
    event.preventDefault();
    const item = event.currentTarget;
    setOpen({ anchor: anchor ?? item, item });
    return true;
  };

  const openOn = (
    event: KeyboardEvent<HTMLElement>,
    anchor: HTMLElement | null,
  ): boolean =>
    event.key === 'F10' &&
    event.shiftKey &&
    !event.ctrlKey &&
    !event.altKey &&
    !event.metaKey &&
    show(event, anchor);

  // The item's own, not a nested row's, a text box's, whose native menu edits
  // text, a dialog's the item holds, which is modal over it, or a form's the
  // item holds, which is about what is being made, not the item.
  const openOnContextMenu = (
    event: MouseEvent<HTMLElement>,
    anchor: HTMLElement | null,
  ) => {
    const target = event.target as Element;
    if (
      closestNavItem(target) === event.currentTarget &&
      !target.closest('input, textarea, form, [aria-modal="true"]')
    ) {
      show(event, anchor);
    }
  };

  const menu = open && (
    <Menu
      label={label}
      items={items}
      anchor={open.anchor}
      onSelect={(id) => offered.find((action) => action.id === id)?.run()}
      onClose={(returnFocus) => {
        setOpen(null);
        if (returnFocus) open.item.focus();
      }}
    />
  );

  return { openOn, openOnContextMenu, menu };
}
