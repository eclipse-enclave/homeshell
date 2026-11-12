import { type KeyboardEvent, useId, useRef, useState } from 'react';
import type { UntrackedWorktree } from '@homeshell/model';
import { Button } from './button';
import controls from './controls.module.css';
import { CopyablePath } from './copyable-path';
import foldStyles from './fold.module.css';
import { ChevronDownIcon } from './icons';
import { isModified } from './keys';
import {
  controlTabIndexFor,
  listItemKeyShortcuts,
  NAV_ITEM_SELECTOR,
} from './list-navigation';
import styles from './untracked-worktrees.module.css';

const FOLD_NAV_ID = 'untracked-worktrees';
const rowNavId = (worktree: UntrackedWorktree) => `untracked:${worktree.path}`;

/**
 * The IDs of the fold and its rows in the workstream list, in page order: the
 * list's tab stop can rest on them (see useListTabStop's `trailing`).
 */
export function untrackedNavIds(
  worktrees: readonly UntrackedWorktree[],
): string[] {
  return worktrees.length === 0
    ? []
    : [FOLD_NAV_ID, ...worktrees.map(rowNavId)];
}

export interface UntrackedWorktreesProps {
  worktrees: readonly UntrackedWorktree[];
  /**
   * The item of the workstream list holding its one tab stop (roving
   * tabindex), as for WorkstreamCard. Set, the fold and the rows take Tab only
   * when one of them holds it, and an Add button only from its row.
   */
  tabStopId?: string;
  onAdd: (path: string) => void;
  /** The path an Add is in flight for: its button is busy, and every Add waits. */
  adding?: string;
  /** Why no Add can be made, when something other than an Add in flight blocks them. */
  disabledReason?: string;
}

export function UntrackedWorktrees({
  worktrees,
  tabStopId,
  onAdd,
  adding,
  disabledReason,
}: UntrackedWorktreesProps) {
  const [expanded, setExpanded] = useState(false);
  const pathIdPrefix = useId();
  const foldRef = useRef<HTMLButtonElement>(null);
  const listRef = useRef<HTMLUListElement>(null);
  if (worktrees.length === 0) return null;

  const blocked = Boolean(adding) || Boolean(disabledReason);
  // As WorkstreamCard's tabIndexFor: unset outside a navigable list.
  const tabIndexFor = (navId: string) =>
    tabStopId === undefined ? undefined : navId === tabStopId ? 0 : -1;
  const rowTabIndex = (worktree: UntrackedWorktree) =>
    tabIndexFor(rowNavId(worktree));
  // The fold and its rows are items of the workstream list's arrow-key tree
  // (see handleListKeyDown), which walks [data-nav-id]: Up and Down reach them
  // from the cards. Right and Left work as on a card and its session rows.
  // They are not selectable: focus on one leaves nothing selected, so that no
  // Alt letter acts on a card or session the user has moved away from.
  const handleFoldKeyDown = (event: KeyboardEvent<HTMLButtonElement>) => {
    if (isModified(event)) return;
    if (event.key === 'ArrowRight') {
      event.preventDefault();
      if (expanded) {
        listRef.current?.querySelector<HTMLElement>(NAV_ITEM_SELECTOR)?.focus();
      } else setExpanded(true);
    } else if (event.key === 'ArrowLeft' && expanded) {
      event.preventDefault();
      setExpanded(false);
    }
  };
  const handleRowKeyDown = (
    event: KeyboardEvent<HTMLLIElement>,
    path: string,
  ) => {
    if (event.target !== event.currentTarget || isModified(event)) return;
    if (event.key === 'Enter' && !event.repeat) {
      event.preventDefault();
      if (!blocked) onAdd(path);
    } else if (event.key === 'ArrowLeft') {
      event.preventDefault();
      foldRef.current?.focus();
    }
  };
  return (
    <div className={foldStyles.fold}>
      <button
        ref={foldRef}
        type="button"
        className={foldStyles.foldButton}
        tabIndex={tabIndexFor(FOLD_NAV_ID)}
        aria-expanded={expanded}
        data-nav-id={FOLD_NAV_ID}
        onClick={(event) => {
          // Focused, so the list's tab stop leaves a row that folds away
          // (not every browser focuses a button it clicks).
          event.currentTarget.focus();
          setExpanded((open) => !open);
        }}
        onKeyDown={handleFoldKeyDown}
      >
        <ChevronDownIcon size={14} />
        {worktrees.length}{' '}
        {worktrees.length === 1 ? 'untracked worktree' : 'untracked worktrees'}
      </button>
      {expanded && (
        <ul
          ref={listRef}
          className={styles.list}
          aria-label="Untracked worktrees"
        >
          {worktrees.map((worktree, index) => (
            <li
              key={worktree.path}
              className={styles.item}
              tabIndex={rowTabIndex(worktree) ?? -1}
              data-nav-id={rowNavId(worktree)}
              aria-keyshortcuts={listItemKeyShortcuts('worktree')}
              onKeyDown={(event) => handleRowKeyDown(event, worktree.path)}
            >
              <span className={styles.name}>{worktree.name}</span>
              {worktree.branch !== worktree.name && (
                <span className={styles.branch}>
                  {worktree.branch ?? 'detached'}
                </span>
              )}
              <CopyablePath
                path={worktree.path}
                id={`${pathIdPrefix}-${index}`}
                className={styles.path}
              />
              {worktree.sessionCount > 0 && (
                <span className={styles.sessions}>
                  {worktree.sessionCount}{' '}
                  {worktree.sessionCount === 1 ? 'session' : 'sessions'}
                </span>
              )}
              <Button
                className={controls.compact}
                tabIndex={controlTabIndexFor(rowTabIndex(worktree))}
                aria-label={`Add ${worktree.name}`}
                // The path tells apart worktrees whose folders share a name.
                aria-describedby={`${pathIdPrefix}-${index}`}
                aria-disabled={blocked}
                aria-busy={adding === worktree.path}
                title={
                  disabledReason ?? (adding ? 'Adding a worktree' : undefined)
                }
                onClick={() => onAdd(worktree.path)}
              >
                Add
              </Button>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
