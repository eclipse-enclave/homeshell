import { useEffect, useLayoutEffect, useRef, useState } from 'react';
import { Button } from '@homeshell/components';
import { useSettings } from './use-settings';
import type { OpenWithEntry } from './settings-types';
import styles from './open-with-settings.module.css';

/** What an entry with no command means; the daemon resolves it per host. */
const SYSTEM_DEFAULT_PLACEHOLDER = 'System default terminal';


/** A row id for entries added in this browser, keeping React keys stable. */
function newEntryId(): string {
  const uuid = globalThis.crypto?.randomUUID?.();
  return `open-with-${uuid ?? Math.random().toString(16).slice(2)}`;
}

/**
 * Whether the daemon would accept this row: a label, and a command unless the
 * row runs in a terminal (no command there means an interactive shell).
 */
function complete(entry: OpenWithEntry): boolean {
  if (entry.label.trim() === '') return false;
  return entry.command.trim() !== '' || entry.inTerminal;
}

/** The row controls focus can be sent to once the rows have re-rendered. */
type RowControl = 'label' | 'up' | 'down' | 'delete';

function sameEntries(a: OpenWithEntry[], b: OpenWithEntry[]): boolean {
  if (a.length !== b.length) return false;
  return a.every(
    (entry, i) =>
      entry.id === b[i].id &&
      entry.label === b[i].label &&
      entry.command === b[i].command &&
      entry.inTerminal === b[i].inTerminal,
  );
}

/** The "Open with" settings category: the user's list of ways to open a workstream. */
export function OpenWithSettings() {
  const { settings, setSection } = useSettings();
  const stored = settings.openWith.entries;

  const [draft, setDraft] = useState<OpenWithEntry[]>(stored);
  // The complete rows this panel last wrote or read, distinguishing an echo of
  // our own write from an external change the draft must follow.
  const committedRef = useRef<OpenWithEntry[]>(stored);

  useEffect(() => {
    if (sameEntries(stored, committedRef.current)) return;
    committedRef.current = stored;
    setDraft(stored);
  }, [stored]);

  // Renders `next` and persists its complete rows when they changed. Incomplete
  // rows stay local: the daemon refuses a row with no label. The default row is
  // sent as last saved instead, as the daemon would restore it as shipped.
  const commit = (next: OpenWithEntry[]) => {
    setDraft(next);
    const ready = next
      .map((entry) =>
        complete(entry) || !entry.fixed
          ? entry
          : (committedRef.current.find((saved) => saved.id === entry.id) ?? entry),
      )
      .filter(complete);
    if (sameEntries(ready, committedRef.current)) return;
    committedRef.current = ready;
    setSection('openWith', { entries: ready });
  };

  const patchEntry = (index: number, patch: Partial<OpenWithEntry>) =>
    draft.map((entry, i) => (i === index ? { ...entry, ...patch } : entry));

  // Where focus goes once the rows have re-rendered: an added row's label, a
  // moved row's same move button, or the Delete of the row that took a deleted
  // one's place — Add entry when there is none.
  const listRef = useRef<HTMLUListElement>(null);
  const addRef = useRef<HTMLButtonElement>(null);
  const focusAfter = useRef<{ row: number; control: RowControl } | null>(null);
  useLayoutEffect(() => {
    const target = focusAfter.current;
    if (!target) return;
    focusAfter.current = null;
    const row = listRef.current?.children[target.row];
    const selector = `[data-control="${target.control}"]`;
    (row?.querySelector<HTMLElement>(selector) ?? addRef.current)?.focus();
  });

  const move = (index: number, offset: -1 | 1) => {
    const next = [...draft];
    [next[index], next[index + offset]] = [next[index + offset], next[index]];
    commit(next);
    focusAfter.current = { row: index + offset, control: offset < 0 ? 'up' : 'down' };
  };

  return (
    <div className={styles.panel}>
      <p className={styles.intro}>
        Ways to open a workstream. Each entry becomes an item in every
        workstream&rsquo;s <strong>Open</strong> menu: the label you pick from,
        and the command behind it — a shell line run in the workstream&rsquo;s
        folder (<code>vim .</code>).
      </p>
      <p className={styles.intro}>
        Tick <strong>in a terminal</strong> for a program that needs a terminal
        window around it: <code>vim .</code> would otherwise exit the moment it
        starts. An entry with no command is the terminal itself — this
        host&rsquo;s default, resolved by the daemon.
      </p>
      <p className={styles.intro}>
        The row marked <strong>default</strong> is what the
        <strong> Open</strong> button itself runs, so pointing it at another
        command changes what a plain click does. Rename and move it like any
        other row; only deleting it is not possible, as every workstream needs
        something behind that button.
      </p>

      {draft.length === 0 ? (
        <p className={styles.muted}>No entries. Add one below.</p>
      ) : (
        <ul
          ref={listRef}
          className={styles.entries}
          aria-label="Open with entries"
        >
          {draft.map((entry, index) => (
            <li key={entry.id || index} className={styles.entry}>
              <span className={styles.order}>
                <Button
                  data-control="up"
                  aria-disabled={index === 0}
                  onClick={() => move(index, -1)}
                  aria-label={`Move ${entry.label || `entry ${index + 1}`} up`}
                  title="Move up"
                >
                  ↑
                </Button>
                <Button
                  data-control="down"
                  aria-disabled={index === draft.length - 1}
                  onClick={() => move(index, 1)}
                  aria-label={`Move ${entry.label || `entry ${index + 1}`} down`}
                  title="Move down"
                >
                  ↓
                </Button>
              </span>
              <label className={styles.field}>
                <span className={styles.fieldLabel}>Label</span>
                <input
                  data-control="label"
                  className={styles.input}
                  value={entry.label}
                  onChange={(event) =>
                    setDraft(patchEntry(index, { label: event.target.value }))
                  }
                  onBlur={() => commit(draft)}
                  placeholder="Vim"
                  aria-label={`Label of entry ${index + 1}`}
                />
              </label>
              <label className={styles.field}>
                <span className={styles.fieldLabel}>Command</span>
                <input
                  className={styles.input}
                  value={entry.command}
                  onChange={(event) =>
                    setDraft(patchEntry(index, { command: event.target.value }))
                  }
                  onBlur={() => commit(draft)}
                  placeholder={entry.inTerminal ? SYSTEM_DEFAULT_PLACEHOLDER : 'vim .'}
                  aria-label={`Command of entry ${index + 1}`}
                />
              </label>
              <label className={styles.terminalToggle}>
                <input
                  type="checkbox"
                  checked={entry.inTerminal}
                  onChange={(event) =>
                    commit(patchEntry(index, { inTerminal: event.target.checked }))
                  }
                  aria-label={`Run entry ${index + 1} in a terminal`}
                />
                <span>in a terminal</span>
              </label>
              {!complete(entry) && (
                <span
                  className={styles.unsaved}
                  title="Saved once it has a label — and a command, unless it runs in a terminal."
                >
                  unsaved
                </span>
              )}
              {/* Fixed-width slot so rows without a Delete button stay aligned. */}
              <span className={styles.rowAction}>
                {entry.fixed ? (
                  <span
                    className={styles.defaultBadge}
                    title="What the Open button runs. Change it freely; it cannot be deleted."
                  >
                    default
                  </span>
                ) : (
                  <Button
                    onClick={() => {
                      commit(draft.filter((_, i) => i !== index));
                      focusAfter.current = { row: index, control: 'delete' };
                    }}
                    data-control="delete"
                    title="Delete this entry"
                    aria-label={`Delete ${entry.label || `entry ${index + 1}`}`}
                  >
                    Delete
                  </Button>
                )}
              </span>
            </li>
          ))}
        </ul>
      )}

      <Button
        ref={addRef}
        onClick={() => {
          commit([
            ...draft,
            { id: newEntryId(), label: '', command: '', inTerminal: false },
          ]);
          focusAfter.current = { row: draft.length, control: 'label' };
        }}
        title="Add an entry"
      >
        Add entry
      </Button>
    </div>
  );
}

export default OpenWithSettings;
