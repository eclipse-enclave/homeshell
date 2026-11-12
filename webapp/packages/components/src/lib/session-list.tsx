import { type Session, splitSessions } from '@homeshell/model';
import { type KeyboardEvent, type ReactNode, useRef, useState } from 'react';
import { flushSync } from 'react-dom';
import { ChevronDownIcon } from './icons';
import { isModified } from './keys';
import { controlTabIndexFor, focusNavItem } from './list-navigation';
import styles from './session-list.module.css';

/** How many ended sessions show before the rest fold away. */
const ENDED_VISIBLE = 3;

export interface SessionListProps {
  sessions: readonly Session[];
  /** The list's selected row, which never folds away. */
  selectedId?: string;
  /** The row holding the list's tab stop, which never folds away either. */
  tabStopId?: string;
  /** A row's tabIndex in the list (see WorkstreamCard.tabStopId). */
  tabIndexFor: (id: string) => number | undefined;
  /** A session's row: an `<li>` keyed by the session's ID. */
  renderRow: (session: Session) => ReactNode;
}

/**
 * A workstream's session rows: the live ones first, then under "Ended · N" the
 * ended ones, newest first. Past the first few, ended rows fold away, except
 * the one the list is on: the selection and the tab stop never vanish into the
 * fold.
 */
export function SessionList({
  sessions,
  selectedId,
  tabStopId,
  tabIndexFor,
  renderRow,
}: SessionListProps) {
  const [showAll, setShowAll] = useState(false);
  const listRef = useRef<HTMLUListElement>(null);
  const { live, ended } = splitSessions(sessions);
  const foldable = ended.length > ENDED_VISIBLE;
  const staysShown = (session: Session, index: number) =>
    !foldable ||
    showAll ||
    index < ENDED_VISIBLE ||
    session.id === selectedId ||
    session.id === tabStopId;
  const shownEnded: Session[] = [];
  const folded: Session[] = [];
  for (const [index, session] of ended.entries()) {
    (staysShown(session, index) ? shownEnded : folded).push(session);
  }
  // The fold is Tab's next stop after the last ended row shown, as that row's
  // own controls are.
  const lastShown = shownEnded.at(-1);
  const foldTabIndex = lastShown
    ? controlTabIndexFor(tabIndexFor(lastShown.id))
    : undefined;

  // Opened from the keyboard, the fold hands focus to the first row it held,
  // so the arrows go on from there; a click leaves focus on the fold.
  const handleFoldKeyDown = (event: KeyboardEvent<HTMLButtonElement>) => {
    if ((event.key !== 'Enter' && event.key !== ' ') || isModified(event)) {
      return;
    }
    // Taken, so that the browser does not click it as well.
    event.preventDefault();
    if (event.repeat) return;
    const revealed = folded[0];
    flushSync(() => setShowAll(!showAll));
    if (revealed && listRef.current) focusNavItem(listRef.current, revealed.id);
  };

  // One array of keyed items, live and ended alike, so a session that ends
  // moves within it and keeps its row (and what is open in it) rather than
  // being made anew in another.
  return (
    <ul ref={listRef} className={styles.sessions}>
      {[
        ...live.map(renderRow),
        ended.length > 0 && (
          <li key="ended" className={styles.divider}>
            Ended · {ended.length}
          </li>
        ),
        ...shownEnded.map(renderRow),
        foldable && (showAll || folded.length > 0) && (
          <li key="fold" className={styles.fold}>
            <button
              type="button"
              className={styles.foldButton}
              tabIndex={foldTabIndex}
              aria-expanded={showAll}
              onClick={() => setShowAll(!showAll)}
              onKeyDown={handleFoldKeyDown}
            >
              <ChevronDownIcon size={14} />
              {showAll
                ? 'Show fewer ended sessions'
                : `${folded.length} more ended ${folded.length === 1 ? 'session' : 'sessions'}`}
            </button>
          </li>
        ),
      ]}
    </ul>
  );
}
