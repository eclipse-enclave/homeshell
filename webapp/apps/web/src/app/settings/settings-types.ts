/**
 * HomeShell user settings persisted by the daemon in settings.json, served over
 * GET/PUT /api/settings. Per-browser preferences (theme, last-selected project)
 * live in localStorage instead.
 */

export type SidebarSort = 'insertion' | 'name';

/** How activity notifications are delivered; the daemon routes on this. */
export type NotificationStyle = 'native' | 'in-app' | 'both' | 'none';

/**
 * The per-event half of the notification settings: whether a kind of activity
 * raises a notification at all, and whether that notification makes a sound.
 * `sound` only applies while `notify` is set — an alert that never fires cannot
 * be heard. The daemon owns both decisions; the UI only edits them.
 */
export interface NotificationEvent {
  notify: boolean;
  sound: boolean;
}

/**
 * A setting that is either a fixed value or "remember the last used value".
 * `lastUsed` wins when `useLastUsed` is set and a value has been recorded.
 */
export interface Defaultable<T> {
  value: T;
  useLastUsed: boolean;
  lastUsed?: T;
}

export interface Settings {
  projects: {
    rememberLastSelected: boolean;
    sidebarSort: SidebarSort;
    confirmRemove: boolean;
  };
  workstreams: {
    defaultProvider: Defaultable<string>;
    defaultStrategy: Defaultable<string>;
    /** Template for the default workstream name; `{project}` expands to the project name. */
    nameTemplate: string;
    /** Filesystem location for out-of-project strategies (worktree/copy). */
    location: string;
    confirmRemove: boolean;
  };
  sessions: {
    /** Which agent a new session defaults to; the agent picks its own model. */
    defaultAgent: Defaultable<string>;
    /** How many terminal-tail lines an expanded session row shows (1..24). */
    previewLines: number;
  };
  providers: {
    refreshIntervalSec: number;
  };
  notifications: {
    /** Which channel(s) deliver activity notifications. */
    style: NotificationStyle;
    /** Sound name to play; applicable whenever the style is not `none`. */
    sound: string;
    /** The alert for a session that has become ready for input. */
    waiting: NotificationEvent;
    /** The alert for a session that has finished its work. */
    finished: NotificationEvent;
    /**
     * Seconds the same event stays quiet for the same session after firing.
     * Session state is detected from the terminal screen and can flap, so
     * without this every flap is an alert; 0 disables the suppression.
     */
    quietSeconds: number;
  };
  openWith: {
    /** Every entry becomes an item in each workstream's Open menu. */
    entries: OpenWithEntry[];
  };
  terminal: {
    /** `auto`, `custom`, or the id of an emulator from `GET /api/terminal-emulators`. */
    emulator: string;
    /** The `custom` template: a shell line in which `{command}` stands for the shell-quoted command to run. */
    command: string;
  };
}

/** One row of the "open with" list. */
export interface OpenWithEntry {
  /** Addresses the row as the open mode `open-with:<id>`; the daemon assigns one if empty. */
  id: string;
  label: string;
  /** Shell line run in the workstream's directory; empty means an interactive shell. */
  command: string;
  /** The user's statement that the command needs a terminal window around it. */
  inTerminal: boolean;
  /** The row the plain Open runs. Daemon-owned; such a row cannot be removed. */
  fixed?: boolean;
}

export const DEFAULT_SETTINGS: Settings = {
  projects: {
    rememberLastSelected: true,
    sidebarSort: 'insertion',
    confirmRemove: true,
  },
  workstreams: {
    // Empty values defer to the provider schema's own default.
    defaultProvider: { value: '', useLastUsed: false },
    defaultStrategy: { value: '', useLastUsed: false },
    nameTemplate: '',
    location: '',
    confirmRemove: true,
  },
  sessions: {
    defaultAgent: { value: 'claude', useLastUsed: false },
    previewLines: 20,
  },
  providers: {
    refreshIntervalSec: 0.5,
  },
  notifications: {
    style: 'native',
    sound: 'default',
    waiting: { notify: true, sound: true },
    finished: { notify: true, sound: true },
    // Three poll intervals at the default 5 s cadence — mirrors the daemon's
    // settings.Defaults(), which is the authority.
    quietSeconds: 15,
  },
  openWith: {
    // Mirrors the daemon's shipped default: the host's terminal.
    entries: [
      { id: 'terminal', label: 'Terminal', command: '', inTerminal: true, fixed: true },
    ],
  },
  terminal: { emulator: 'auto', command: '' },
};

/** The effective default value of a `Defaultable`: last used when enabled and recorded, else the fixed value. */
export function resolveDefault<T>(d: Defaultable<T>): T {
  return d.useLastUsed && d.lastUsed !== undefined ? d.lastUsed : d.value;
}

const SIDEBAR_SORTS: readonly SidebarSort[] = ['insertion', 'name'];
const NOTIFICATION_STYLES: readonly NotificationStyle[] = [
  'native',
  'in-app',
  'both',
  'none',
];

/**
 * Deep-merges stored settings over the defaults, type-guarding each known key so
 * partial or wrongly-typed persisted JSON never drops or corrupts one.
 */
export function mergeSettings(stored: unknown): Settings {
  const merged = mergeInto(DEFAULT_SETTINGS, stored) as Settings;
  // A value outside a closed enum would leave its Select/RadioGroup blank.
  if (!SIDEBAR_SORTS.includes(merged.projects.sidebarSort)) {
    merged.projects.sidebarSort = DEFAULT_SETTINGS.projects.sidebarSort;
  }
  if (!NOTIFICATION_STYLES.includes(merged.notifications.style)) {
    merged.notifications.style = DEFAULT_SETTINGS.notifications.style;
  }
  // mergeInto's leaf type check reads any object as "same type as an array", so
  // the entries list is validated by shape here. An empty list is valid.
  // Per entry, not per list: a single unreadable row must not take the rest of
  // the user's list with it, because the next save would persist that loss.
  const entries: unknown = merged.openWith.entries;
  merged.openWith = Array.isArray(entries)
    ? {
        entries: entries.filter(readableEntry).map((entry) => ({
          id: typeof entry.id === 'string' ? entry.id : '',
          label: entry.label as string,
          command: entry.command as string,
          inTerminal: entry.inTerminal === true,
          fixed: entry.fixed === true,
        })),
      }
    : DEFAULT_SETTINGS.openWith;
  return merged;
}

/** Whether a stored open-with row carries the two fields the UI cannot do without. */
function readableEntry(entry: unknown): entry is Record<string, unknown> {
  return (
    isPlainObject(entry) &&
    typeof entry.label === 'string' &&
    typeof entry.command === 'string'
  );
}

function isPlainObject(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v);
}

function mergeInto(base: unknown, override: unknown): unknown {
  if (!isPlainObject(base)) {
    // Leaf default: accept a stored value only when its type matches.
    return typeof override === typeof base && override !== undefined
      ? override
      : base;
  }
  if (!isPlainObject(override)) return base;
  const out: Record<string, unknown> = { ...base };
  for (const key of Object.keys(override)) {
    out[key] =
      key in base ? mergeInto(base[key], override[key]) : override[key];
  }
  return out;
}
