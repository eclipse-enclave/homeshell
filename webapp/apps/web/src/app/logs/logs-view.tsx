import {
  useCallback,
  useEffect,
  useRef,
  useState,
} from 'react';
import type { LogRecord } from '@homeshell/model';
import { Button, LogViewer, Select, usePaneFocus } from '@homeshell/components';
import type { LogColumn, SelectModifiers } from '@homeshell/components';

import { levelOptions } from '../log-levels';
import type { DaemonConnection } from '../use-daemon-state';
import styles from './logs-view.module.css';

const CONNECTION_LABEL: Record<string, string> = {
  connecting: 'connecting…',
  connected: 'live',
  offline: 'daemon offline',
};

/**
 * The columns for the daemon's API access log (method/path/status/bytes/
 * duration), read from each record's attributes as emitted by the access-log
 * middleware. Records that are not request logs leave these blank. This shape
 * lives here, with the app that knows it, rather than in the generic LogViewer.
 */
const REQUEST_COLUMNS: readonly LogColumn[] = [
  {
    key: 'method',
    header: 'Method',
    className: styles.method,
    render: (record) => attrString(record, 'method'),
  },
  {
    key: 'path',
    header: 'Path',
    className: styles.path,
    render: (record) => attrString(record, 'path'),
    title: (record) => attrString(record, 'path') || undefined,
  },
  {
    key: 'status',
    header: 'Status',
    className: styles.status,
    render: (record) => attrNumber(record, 'status'),
  },
  {
    key: 'bytes',
    header: 'Bytes',
    className: styles.bytes,
    render: (record) => attrNumber(record, 'bytes'),
  },
  {
    key: 'duration',
    header: 'Duration',
    className: styles.duration,
    render: (record) => {
      const duration = record.attrs?.duration;
      return typeof duration === 'number' ? formatDuration(duration) : '';
    },
  },
];

/**
 * Empty-state copy for the stream. The access log is emitted at debug level, so
 * at the default `info` level nothing arrives; the hint tells the reader why the
 * pane is quiet and how to make request logs appear.
 */
const EMPTY_MESSAGE =
  'Waiting for log activity… API request logs are emitted at the debug level — lower the level above to see them.';

// Per-device sizing of the pane. Persisted so the height sticks across reloads.
const HEIGHT_KEY = 'hs-logpane-height';
const DEFAULT_HEIGHT = 260;
const MIN_HEIGHT = 120;
// Leave at least this much of the viewport above the pane.
const MIN_TOP = 160;
const RESIZE_STEP = 32;

interface Selection {
  /** Selected record sequence numbers. */
  seqs: Set<number>;
  /** Anchor for range selection and the row whose details are shown. */
  focused: number | null;
}

const EMPTY_SELECTION: Selection = { seqs: new Set(), focused: null };

export interface LogPaneProps {
  /** Closes the pane (the toolbar Close button). */
  onClose: () => void;
  /** The daemon's live log level (shared with Settings › Diagnostics). */
  logLevel?: string;
  /** Changes the daemon's live level; resolves to an error message or null. */
  onLogLevelChange: (level: string) => Promise<string | null>;
  /** Buffered log records (oldest first). Owned by the app so the ring buffer
   * survives closing and reopening the pane. */
  records: readonly LogRecord[];
  connection: DaemonConnection;
  /** True once the ring buffer has dropped older entries. */
  overflowed: boolean;
  /** The configured ring-buffer size, shown in the overflow notice. */
  bufferSize: number;
  /**
   * When set, the level control is disabled with this text as the reason: changing
   * the level is a daemon call (`PATCH /api/config`), unlike everything else in
   * this pane. Reading the buffer, selecting, copying and closing are all local and
   * stay available — the log pane is exactly where a user looks when the daemon has
   * gone away.
   *
   * Distinct from `connection`, which is the *log stream's* own state and is
   * displayed, not acted on.
   */
  disabledReason?: string;
}

/**
 * Dockable bottom log pane. Renders the app's live log buffer (GET /api/logs,
 * owned by the app so it keeps accumulating while the pane is closed) into a
 * scrolling list over a details pane, resizable by dragging the sash at its top
 * edge, so the main app stays usable while logs accumulate. The toolbar changes
 * the daemon's live level inline (shared with Settings › Diagnostics), copies
 * the selected entries (or all when none are selected), and closes the pane. The
 * ring buffer keeps the most recent entries and a notice marks where older ones
 * were dropped.
 */
export function LogPane({
  onClose,
  logLevel,
  onLogLevelChange,
  records,
  connection,
  overflowed,
  bufferSize,
  disabledReason,
}: LogPaneProps) {
  const [selection, setSelection] = useState<Selection>(EMPTY_SELECTION);
  const [levelError, setLevelError] = useState<string | null>(null);
  const [height, setHeight] = useState(loadHeight);
  const heightRef = useRef(height);
  heightRef.current = height;
  const paneRef = useRef<HTMLElement>(null);

  usePaneFocus(() => paneRef.current);

  // Resize by dragging the sash: the pane grows as the pointer moves up.
  const draggingRef = useRef(false);
  const clampHeight = (px: number) =>
    Math.max(MIN_HEIGHT, Math.min(px, window.innerHeight - MIN_TOP));
  useEffect(() => {
    const onMove = (event: MouseEvent) => {
      if (!draggingRef.current) return;
      setHeight(clampHeight(window.innerHeight - event.clientY));
    };
    const onUp = () => {
      if (!draggingRef.current) return;
      draggingRef.current = false;
      localStorage.setItem(HEIGHT_KEY, String(Math.round(heightRef.current)));
    };
    window.addEventListener('mousemove', onMove);
    window.addEventListener('mouseup', onUp);
    return () => {
      window.removeEventListener('mousemove', onMove);
      window.removeEventListener('mouseup', onUp);
    };
  }, []);

  const resizeByKey = (event: React.KeyboardEvent) => {
    const delta =
      event.key === 'ArrowUp' ? RESIZE_STEP : event.key === 'ArrowDown' ? -RESIZE_STEP : 0;
    if (delta === 0) return;
    event.preventDefault();
    setHeight((current) => {
      const next = clampHeight(current + delta);
      localStorage.setItem(HEIGHT_KEY, String(Math.round(next)));
      return next;
    });
  };

  const select = (seq: number, mods: SelectModifiers) => {
    setSelection((prev) => resolveSelection(prev, seq, mods, records));
  };

  const copy = async () => {
    const chosen = selection.seqs.size
      ? records.filter((record) => selection.seqs.has(record.seq))
      : records;
    await navigator.clipboard?.writeText(chosen.map(formatLine).join('\n'));
  };

  const changeLevel = useCallback(
    async (level: string) => {
      setLevelError(null);
      const error = await onLogLevelChange(level);
      if (error) setLevelError(error);
    },
    [onLogLevelChange]
  );

  const copyLabel = selection.seqs.size
    ? `Copy ${selection.seqs.size} selected`
    : 'Copy all';

  return (
    <section
      ref={paneRef}
      className={styles.pane}
      style={{ height }}
      aria-label="Logs"
      tabIndex={-1}
    >
      <div
        className={styles.sash}
        role="separator"
        aria-orientation="horizontal"
        aria-label="Resize logs pane"
        tabIndex={0}
        onMouseDown={(event) => {
          event.preventDefault();
          draggingRef.current = true;
        }}
        onKeyDown={resizeByKey}
      />
      <header className={styles.toolbar}>
        <span className={styles.title}>Logs</span>
        <span
          className={`${styles.connection} ${styles[connection]}`}
          role="status"
        >
          {CONNECTION_LABEL[connection]}
        </span>
        <span className={styles.spacer} />
        <Button
          onClick={copy}
          disabled={records.length === 0}
          title="Copy selected log entries to the clipboard (all when none are selected)"
        >
          {copyLabel}
        </Button>
        <Select
          label="Level"
          value={logLevel ?? ''}
          options={levelOptions(logLevel)}
          onChange={changeLevel}
          disabled={!logLevel || Boolean(disabledReason)}
          hint={
            disabledReason ?? (logLevel ? undefined : 'Daemon unavailable.')
          }
          error={levelError ?? undefined}
        />
        <Button onClick={onClose} aria-label="Close logs pane">
          Close
        </Button>
      </header>
      <LogViewer
        records={records}
        selected={selection.seqs}
        focusedSeq={selection.focused}
        onSelect={select}
        columns={REQUEST_COLUMNS}
        emptyMessage={EMPTY_MESSAGE}
        leadingNotice={
          overflowed
            ? `Earlier entries were dropped — showing the most recent ${bufferSize}.`
            : undefined
        }
      />
    </section>
  );
}

/** Reads the persisted pane height, defaulting when unset or out of range. */
function loadHeight(): number {
  const stored = Number(localStorage.getItem(HEIGHT_KEY));
  return Number.isFinite(stored) && stored >= MIN_HEIGHT ? stored : DEFAULT_HEIGHT;
}

/** A record attribute rendered as a string, or blank when absent/other-typed. */
function attrString(record: LogRecord, key: string): string {
  const value = record.attrs?.[key];
  return typeof value === 'string' ? value : '';
}

/** A numeric record attribute rendered as a string, blank when absent. */
function attrNumber(record: LogRecord, key: string): string {
  const value = record.attrs?.[key];
  return typeof value === 'number' ? String(value) : '';
}

/** Formats a nanosecond duration compactly (ns → µs → ms → s). */
function formatDuration(ns: number): string {
  if (ns < 1_000) return `${ns}ns`;
  if (ns < 1_000_000) return `${(ns / 1_000).toFixed(1)}µs`;
  if (ns < 1_000_000_000) return `${(ns / 1_000_000).toFixed(1)}ms`;
  return `${(ns / 1_000_000_000).toFixed(2)}s`;
}

/** Computes the next selection from a click and its modifier keys. */
function resolveSelection(
  prev: Selection,
  seq: number,
  mods: SelectModifiers,
  records: readonly LogRecord[]
): Selection {
  if (mods.toggle) {
    const seqs = new Set(prev.seqs);
    if (seqs.has(seq)) seqs.delete(seq);
    else seqs.add(seq);
    return { seqs, focused: seq };
  }
  if (mods.range && prev.focused != null) {
    const from = records.findIndex((record) => record.seq === prev.focused);
    const to = records.findIndex((record) => record.seq === seq);
    if (from !== -1 && to !== -1) {
      const [lo, hi] = from <= to ? [from, to] : [to, from];
      const seqs = new Set(records.slice(lo, hi + 1).map((record) => record.seq));
      return { seqs, focused: seq };
    }
  }
  return { seqs: new Set([seq]), focused: seq };
}

/** Renders one record as a single copyable log line. */
function formatLine(record: LogRecord): string {
  const attrs =
    record.attrs && Object.keys(record.attrs).length
      ? ` ${JSON.stringify(record.attrs)}`
      : '';
  return `${record.time} ${record.level.toUpperCase()} ${record.msg}${attrs}`;
}

export default LogPane;
