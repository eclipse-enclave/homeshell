import type { LogRecord } from '@homeshell/model';
import { useEffect, useRef, type ReactNode } from 'react';
import styles from './log-viewer.module.css';
import { titleWhenTruncated } from './truncated-title';

/** Modifier keys held during a row click, resolving the selection gesture. */
export interface SelectModifiers {
  /** Cmd/Ctrl: toggle this row in or out of the current selection. */
  toggle: boolean;
  /** Shift: extend the selection from the focused row to this one. */
  range: boolean;
}

/**
 * A caller-supplied column shown between the intrinsic Level and Message
 * columns. The viewer only knows the fields every record has (time, level,
 * message); any column derived from a record's `attrs` — such as the API
 * access log's method/path/status — is described by the app that knows that
 * record shape, so this widget stays reusable for any log stream.
 */
export interface LogColumn {
  /** Stable identity for the column (also its React key). */
  key: string;
  /** Column heading text. */
  header: ReactNode;
  /** Class applied to the header cell and every body cell (width/alignment). */
  className?: string;
  /** Cell content for a record. */
  render: (record: LogRecord) => ReactNode;
  /** Optional tooltip (title attribute) for a record's cell. */
  title?: (record: LogRecord) => string | undefined;
}

export interface LogViewerProps {
  /** Records to display, oldest first. */
  records: readonly LogRecord[];
  /** Sequence numbers of the selected records. */
  selected: ReadonlySet<number>;
  /** Sequence number of the focused record whose details are shown. */
  focusedSeq?: number | null;
  /** Called with a record's sequence number and the click's modifier keys. */
  onSelect: (seq: number, modifiers: SelectModifiers) => void;
  /** Columns shown between Level and Message; none by default. */
  columns?: readonly LogColumn[];
  /** Shown in place of the stream while there are no records. */
  emptyMessage?: ReactNode;
  /**
   * A non-interactive notice pinned above the first record (e.g. "earlier
   * entries dropped"). Rendered only when there is at least one record.
   */
  leadingNotice?: ReactNode;
}

const NO_COLUMNS: readonly LogColumn[] = [];

/**
 * A live log stream over a details pane. The stream lists one row per record
 * with alternating backgrounds; rows are multi-selectable (plain click selects
 * one, Cmd/Ctrl-click toggles, Shift-click extends a range) and the focused row
 * shows its full properties in the pane below. While nothing is selected the
 * stream follows the tail (auto-scrolls to the newest record); a selection pins
 * the view so entries can be inspected and copied.
 */
export function LogViewer({
  records,
  selected,
  focusedSeq,
  onSelect,
  columns = NO_COLUMNS,
  emptyMessage = 'Waiting for log activity…',
  leadingNotice,
}: LogViewerProps) {
  const streamRef = useRef<HTMLDivElement>(null);
  const following = selected.size === 0;

  useEffect(() => {
    if (following && streamRef.current) {
      streamRef.current.scrollTop = streamRef.current.scrollHeight;
    }
  }, [records, following]);

  const focused = records.find((r) => r.seq === focusedSeq) ?? null;

  return (
    <div className={styles.viewer}>
      <div
        className={styles.stream}
        ref={streamRef}
        role="log"
        aria-label="Log entries"
      >
        {records.length === 0 ? (
          <p className={styles.empty}>{emptyMessage}</p>
        ) : (
          <>
            <div className={styles.headerRow}>
              <span className={styles.time}>Time</span>
              <span className={styles.level}>Level</span>
              {columns.map((column) => (
                <span key={column.key} className={column.className}>
                  {column.header}
                </span>
              ))}
              <span className={styles.msg}>Message</span>
            </div>
            <ol className={styles.rows}>
              {leadingNotice != null && (
                <li className={styles.notice} role="note">
                  {leadingNotice}
                </li>
              )}
              {records.map((record) => (
                <li key={record.seq}>
                  <button
                    type="button"
                    className={styles.row}
                    aria-pressed={selected.has(record.seq)}
                    aria-current={record.seq === focusedSeq}
                    onClick={(e) =>
                      onSelect(record.seq, {
                        toggle: e.metaKey || e.ctrlKey,
                        range: e.shiftKey,
                      })
                    }
                  >
                    <time className={styles.time}>{formatTime(record.time)}</time>
                    <span className={`${styles.level} ${levelClass(record.level)}`}>
                      {record.level}
                    </span>
                    {columns.map((column) => (
                      <span
                        key={column.key}
                        className={column.className}
                        title={column.title?.(record)}
                      >
                        {column.render(record)}
                      </span>
                    ))}
                    <span
                      className={styles.msg}
                      {...titleWhenTruncated(record.msg)}
                    >
                      {record.msg}
                    </span>
                  </button>
                </li>
              ))}
            </ol>
          </>
        )}
      </div>
      <div className={styles.details} aria-label="Log entry details">
        {focused ? (
          <dl className={styles.props}>
            <Property label="time" value={focused.time} />
            <Property label="level" value={focused.level} />
            <Property label="message" value={focused.msg} />
            {Object.entries(focused.attrs ?? {}).map(([key, value]) => (
              <Property key={key} label={key} value={value} />
            ))}
          </dl>
        ) : (
          <p className={styles.detailsHint}>Select an entry to see its details.</p>
        )}
      </div>
    </div>
  );
}

function Property({ label, value }: { label: string; value: unknown }) {
  const text =
    typeof value === 'object' && value !== null
      ? JSON.stringify(value, null, 2)
      : String(value);
  return (
    <>
      <dt className={styles.propKey}>{label}</dt>
      <dd className={styles.propValue}>{text}</dd>
    </>
  );
}

/** Resolves the level-color class for a known level, empty for unknown ones. */
function levelClass(level: string): string {
  return styles[`level-${level}`] ?? '';
}

/** Formats an ISO timestamp as a compact local time, falling back to raw. */
function formatTime(time: string): string {
  const date = new Date(time);
  if (Number.isNaN(date.getTime())) return time;
  const hh = String(date.getHours()).padStart(2, '0');
  const mm = String(date.getMinutes()).padStart(2, '0');
  const ss = String(date.getSeconds()).padStart(2, '0');
  const ms = String(date.getMilliseconds()).padStart(3, '0');
  return `${hh}:${mm}:${ss}.${ms}`;
}

export default LogViewer;
