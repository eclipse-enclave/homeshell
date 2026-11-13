import { useEffect, useRef, useState, type ReactNode } from 'react';
import {
  Button,
  NumberField,
  RadioGroup,
  Select,
  TextField,
  Toggle,
  type SelectOption,
} from '@homeshell/components';
import type { Provider } from '@homeshell/model';
import { DefaultableField } from './defaultable-field';
import { useSettings } from './use-settings';
import type { NotificationEvent, Settings } from './settings-types';
import { levelOptions } from '../log-levels';
import { playNotificationSound } from '../notification-sound';
import { sendTestNotification } from '../notification-test';
import { DEFAULT_MAX_RECORDS } from '../logs/use-log-stream';
import { useTheme, type Theme } from '../use-theme';
import { OpenWithSettings } from './open-with-settings';
import { TerminalSettings } from './terminal-settings';
import styles from './settings-view.module.css';

/** Badge shown on fields the daemon does not yet honor. */
const SUPPORTED = 'applies when supported';

const THEME_OPTIONS: SelectOption[] = [
  { value: 'system', label: 'System' },
  { value: 'light', label: 'Light' },
  { value: 'dark', label: 'Dark' },
];

const SORT_OPTIONS: SelectOption[] = [
  { value: 'insertion', label: 'Order added' },
  { value: 'name', label: 'Name (A–Z)' },
];

// The names the create form shows, from daemon/internal/provider/core.go.
const STRATEGY_OPTIONS: SelectOption[] = [
  { value: '', label: 'Provider default' },
  { value: 'in-place', label: 'In place' },
  { value: 'worktree', label: 'Worktree' },
  { value: 'copy', label: 'Folder: a copy of the project' },
];

const AGENT_OPTIONS: SelectOption[] = [
  { value: 'claude', label: 'Claude Code' },
  { value: 'codex', label: 'Codex' },
  { value: 'gemini', label: 'Gemini' },
];

const NOTIFICATION_STYLE_OPTIONS: SelectOption[] = [
  { value: 'native', label: 'OS-native' },
  { value: 'in-app', label: 'In-app' },
  { value: 'both', label: 'Both' },
  { value: 'none', label: 'None' },
];

const NOTIFICATION_SOUND_OPTIONS: SelectOption[] = [
  { value: 'default', label: 'Default' },
  { value: 'chime', label: 'Chime' },
  { value: 'glass', label: 'Glass' },
  { value: 'ping', label: 'Ping' },
  { value: 'none', label: 'Silent' },
];

/** The notification settings fields that hold a per-event pair of switches. */
type NotificationEventKey = 'waiting' | 'finished';

/**
 * The notify-worthy events, in the order the daemon detects them. The key is
 * both the settings field and the daemon's notification kind, so the two lists
 * are the same list.
 */
const NOTIFICATION_EVENTS: {
  key: NotificationEventKey;
  label: string;
  hint: string;
}[] = [
  {
    key: 'waiting',
    label: 'Needs my input',
    hint: 'The agent is asking something, like a permission prompt or a question.',
  },
  {
    key: 'finished',
    label: 'Finished working',
    hint: 'The agent stopped working and the session went idle.',
  },
];

/** The ordered settings categories shown in the master list. */
interface Category {
  id: string;
  label: string;
  /** Stored settings group this category resets; omitted for read-only groups. */
  section?: keyof Settings;
  description?: string;
}

export const CATEGORIES: Category[] = [
  {
    id: 'appearance',
    label: 'Appearance',
    description: 'Theme is a per-browser preference, stored in this browser.',
  },
  { id: 'projects', label: 'Projects', section: 'projects' },
  { id: 'workstreams', label: 'Workstreams', section: 'workstreams' },
  { id: 'sessions', label: 'Sessions & agents', section: 'sessions' },
  { id: 'providers', label: 'Providers', section: 'providers' },
  // No `description`: the panel introduces itself.
  { id: 'open-with', label: 'Open with', section: 'openWith' },
  {
    id: 'terminal',
    label: 'Terminal',
    section: 'terminal',
    description:
      'The emulator HomeShell opens terminal windows in, for attached sessions, workstream shells, and "Open with" entries that run in a terminal.',
  },
  { id: 'notifications', label: 'Notifications', section: 'notifications' },
  {
    id: 'diagnostics',
    label: 'Diagnostics',
    description:
      "Changes the running daemon's log level immediately, for all clients.",
  },
];

export interface SettingsViewProps {
  onClose: () => void;
  /** The selected category id; defaults to the first category. */
  category?: string;
  /** Selects a category (the parent updates the route). */
  onSelectCategory: (id: string) => void;
  /** Provider catalogue used to populate the default-provider choices. */
  providers?: Provider[];
  /** The daemon's current effective log level (from `GET /api/config`). */
  logLevel?: string;
  /**
   * Changes the daemon's log level live; resolves to an error message (or null)
   * so a rejection can be surfaced. When omitted, the control is read-only.
   */
  onLogLevelChange?: (level: string) => void | Promise<string | null>;
  /** Log ring-buffer size (a per-device, browser-local preference; min 100). */
  logBufferSize?: number;
  /** Changes the log buffer size. */
  onLogBufferSizeChange?: (size: number) => void;
  /**
   * When set, the daemon-backed settings are read-only and this text says why.
   * Every category with a `section` is stored in the daemon, as is the log level,
   * so an unreachable daemon means a write that cannot land — the same reason a
   * failed initial load blocks editing. The browser-local preferences (theme, log
   * buffer size) stay editable: nothing about them needs the daemon.
   */
  disabledReason?: string;
}

/**
 * The settings page: a category list on the left and the selected category's
 * settings in a details pane on the right. Preferences are browser-local
 * (`localStorage`); the Diagnostics log level is live daemon state.
 */
export function SettingsView({
  onClose,
  category,
  onSelectCategory,
  providers = [],
  logLevel,
  onLogLevelChange,
  logBufferSize = 1000,
  onLogBufferSizeChange,
  disabledReason,
}: SettingsViewProps) {
  const { settings, setSection, reset, saveError, loaded, ready } =
    useSettings();
  const { theme, setTheme } = useTheme();
  const selected =
    CATEGORIES.find((c) => c.id === category) ?? CATEGORIES[0];
  const categoriesRef = useRef<HTMLElement>(null);

  // Opens on the selected category, so the arrows and Tab start from there.
  useEffect(() => {
    categoriesRef.current
      ?.querySelector<HTMLElement>('[aria-current="true"]')
      ?.focus();
  }, []);

  const set = <K extends keyof Settings>(section: K, patch: Partial<Settings[K]>) =>
    setSection(section, patch);

  // Local draft committed on blur, not per keystroke: a transient out-of-range
  // value would be rejected by PUT validation and roll the field back mid-edit.
  const [previewLinesDraft, setPreviewLinesDraft] = useState(
    settings.sessions.previewLines,
  );
  useEffect(() => {
    setPreviewLinesDraft(settings.sessions.previewLines);
  }, [settings.sessions.previewLines]);

  // The quiet period is committed on blur for the same reason as the preview
  // lines: an intermediate value while typing (an empty field, a lone "-")
  // would be PUT, rejected, and roll the field back mid-edit.
  const [quietSecondsDraft, setQuietSecondsDraft] = useState(
    settings.notifications.quietSeconds,
  );
  useEffect(() => {
    setQuietSecondsDraft(settings.notifications.quietSeconds);
  }, [settings.notifications.quietSeconds]);

  // Style None silences every channel, so the per-event switches below it have
  // nothing to gate — grey them out rather than let a user tune alerts that
  // cannot fire.
  const notificationsOff = settings.notifications.style === 'none';

  /** Patches one notification event's switches, leaving its sibling alone. */
  const setEvent = (
    key: NotificationEventKey,
    patch: Partial<NotificationEvent>,
  ) =>
    set('notifications', {
      [key]: { ...settings.notifications[key], ...patch },
    } as Pick<Settings['notifications'], NotificationEventKey>);

  // The log level is live daemon state, not a persisted setting.
  const [logLevelError, setLogLevelError] = useState<string | null>(null);
  const changeLogLevel = async (level: string) => {
    setLogLevelError(null);
    const error = await onLogLevelChange?.(level);
    if (error) setLogLevelError(error);
  };

  // Diagnostics is not a daemon-settings group, so it cannot use reset(section).
  // Its two halves also part company when the daemon is gone: the log level is a
  // daemon call and is skipped, while the buffer size is a localStorage write that
  // still lands — its own field stays editable right below, so a reset it refused
  // would be the odd one out. Tested against `disabledReason` rather than
  // `blocked`, which is declared further down.
  const resetDiagnostics = () => {
    if (!disabledReason && logLevel && onLogLevelChange) changeLogLevel('info');
    onLogBufferSizeChange?.(DEFAULT_MAX_RECORDS);
  };

  const handleReset = () => {
    if (selected.section) reset(selected.section);
    else if (selected.id === 'diagnostics') resetDiagnostics();
  };
  const canReset = selected.section !== undefined || selected.id === 'diagnostics';

  // A save error begins with the offending field's dotted path, so it can be
  // shown beneath that field. The refresh interval is absent by design: it
  // clamps client-side and has no inline error slot.
  const FIELD_ERROR_PATHS = [
    'projects.sidebarSort',
    'sessions.previewLines',
    'workstreams.location',
    'notifications.quietSeconds',
    'terminal.emulator',
    'terminal.command',
  ];
  const errorFor = (path: string) =>
    saveError && saveError.startsWith(`${path} `) ? saveError : undefined;
  // A save error with no field to attach to surfaces as a banner instead.
  const generalError =
    saveError && !FIELD_ERROR_PATHS.some((p) => saveError.startsWith(`${p} `))
      ? saveError
      : null;

  // Daemon-backed categories are disabled until a successful load, so a
  // full-document PUT cannot overwrite the daemon's settings with the code
  // defaults. Appearance and Diagnostics are not daemon settings and stay usable.
  const daemonBacked = selected.section !== undefined;
  // An unreachable daemon blocks writing for the same reason a failed load does:
  // the PUT has nowhere to go. Reported separately from loadFailed, since
  // "couldn't load" and "offline right now" are different situations.
  const blocked = Boolean(disabledReason);
  const fieldsetDisabled = daemonBacked && (!ready || blocked);
  const loadFailed = daemonBacked && loaded && !ready;

  const providerOptions: SelectOption[] = [
    { value: '', label: 'Auto (first available)' },
    ...providers.map((p) => ({ value: p.id, label: p.label })),
  ];

  const fields: Record<string, ReactNode> = {
    'open-with': <OpenWithSettings />,
    terminal: (
      <TerminalSettings
        emulatorError={errorFor('terminal.emulator')}
        commandError={errorFor('terminal.command')}
      />
    ),
    appearance: (
      <RadioGroup
        label="Theme"
        value={theme}
        options={THEME_OPTIONS}
        onChange={(value) => setTheme(value as Theme)}
      />
    ),
    projects: (
      <>
        <Toggle
          label="Remember the last selected project"
          checked={settings.projects.rememberLastSelected}
          onChange={(rememberLastSelected) =>
            set('projects', { rememberLastSelected })
          }
        />
        <Select
          label="Sidebar order"
          value={settings.projects.sidebarSort}
          options={SORT_OPTIONS}
          onChange={(sidebarSort) =>
            set('projects', {
              sidebarSort: sidebarSort as Settings['projects']['sidebarSort'],
            })
          }
          error={errorFor('projects.sidebarSort')}
        />
        <Toggle
          label="Confirm before deleting a project"
          checked={settings.projects.confirmRemove}
          onChange={(confirmRemove) => set('projects', { confirmRemove })}
        />
      </>
    ),
    workstreams: (
      <>
        <DefaultableField
          field={settings.workstreams.defaultProvider}
          onChange={(defaultProvider) => set('workstreams', { defaultProvider })}
          lastUsedLabel={(v) => {
            const label = providers.find((p) => p.id === v)?.label ?? v;
            return label ? `Remembering: ${label}` : 'Nothing used yet.';
          }}
        >
          {({ value, onChange, disabled }) => (
            <Select
              label="Default provider"
              value={value}
              options={providerOptions}
              onChange={onChange}
              disabled={disabled}
            />
          )}
        </DefaultableField>

        <DefaultableField
          field={settings.workstreams.defaultStrategy}
          onChange={(defaultStrategy) => set('workstreams', { defaultStrategy })}
        >
          {({ value, onChange, disabled }) => (
            <Select
              label="Default filesystem strategy"
              value={value}
              options={STRATEGY_OPTIONS}
              onChange={onChange}
              disabled={disabled}
            />
          )}
        </DefaultableField>

        <TextField
          label="Workstream name template"
          value={settings.workstreams.nameTemplate}
          onChange={(nameTemplate) => set('workstreams', { nameTemplate })}
          placeholder="e.g. {project}-ws"
          hint="{project} expands to the project name. Leave blank for a generated name."
        />
        <TextField
          label="Worktree / copy location"
          value={settings.workstreams.location}
          onChange={(location) => set('workstreams', { location })}
          placeholder="e.g. ~/.homeshell/worktrees"
          hint="Base directory for worktree/copy workstreams: absolute, ~/…, or relative to the project (e.g. ../worktrees). Empty uses the default location."
          error={errorFor('workstreams.location')}
        />
        <Toggle
          label="Confirm before deleting a workstream"
          checked={settings.workstreams.confirmRemove}
          onChange={(confirmRemove) => set('workstreams', { confirmRemove })}
        />
      </>
    ),
    sessions: (
      <>
        <DefaultableField
          field={settings.sessions.defaultAgent}
          onChange={(defaultAgent) => set('sessions', { defaultAgent })}
        >
          {({ value, onChange, disabled }) => (
            <Select
              label="Default agent"
              value={value}
              options={AGENT_OPTIONS}
              onChange={onChange}
              disabled={disabled}
              badge={SUPPORTED}
              hint="The agent picks its own model, so no default model is stored."
            />
          )}
        </DefaultableField>
        <NumberField
          label="Session preview lines"
          value={previewLinesDraft}
          onChange={setPreviewLinesDraft}
          onCommit={(previewLines) => set('sessions', { previewLines })}
          min={1}
          max={24}
          hint="How many terminal-tail lines an expanded session row shows."
          error={errorFor('sessions.previewLines')}
        />
      </>
    ),
    providers: (
      <>
        <NumberField
          label="Session-state refresh interval (seconds)"
          value={settings.providers.refreshIntervalSec}
          onChange={(refreshIntervalSec) =>
            set('providers', { refreshIntervalSec })
          }
          min={0.1}
          max={86400}
          commitOnBlur
          hint="How often the provider re-checks each session's state."
        />
      </>
    ),
    notifications: (
      <>
        {/*
          Picking a style sends a test notification through it, for the same
          reason picking a sound plays it: "OS-native" and "In-app" describe
          where an alert appears, and the only other way to find out what that
          looks like is to wait for an agent to need you. The daemon does the
          delivering — it owns both channels — so what arrives is the real
          thing. "None" is included: it demonstrates itself by showing nothing.
        */}
        <Select
          label="Notification style"
          value={settings.notifications.style}
          options={NOTIFICATION_STYLE_OPTIONS}
          onChange={(style) => {
            set('notifications', {
              style: style as Settings['notifications']['style'],
            });
            void sendTestNotification(style);
          }}
          hint="OS-native shows a desktop notification; In-app shows a toast in this UI. Picking one sends a test notification."
        />
        {/*
          Picking a sound plays it: the names alone ("Chime", "Glass") say
          nothing about what you will hear, and the alternative is to wait for a
          real notification to find out. Only the pick previews — the panel must
          stay silent while it is merely open. "Silent" plays nothing, which is
          exactly the right preview for it.
        */}
        <Select
          label="Sound effect"
          value={settings.notifications.sound}
          options={NOTIFICATION_SOUND_OPTIONS}
          onChange={(sound) => {
            set('notifications', { sound });
            playNotificationSound(sound);
          }}
          disabled={notificationsOff}
          hint="Which sound the alerts below play. Picking one plays it. Disabled when the style is None."
        />

        {/*
          Which events alert, and which of those are audible, are separate
          switches per event: the common ask is to hear the session that needs
          you and only see the one that finished. The daemon does the gating —
          these only edit the settings it reads.
        */}
        <h3 className={styles.groupTitle}>Alert me when a session…</h3>
        {NOTIFICATION_EVENTS.map(({ key, label, hint }) => (
          <div key={key} className={styles.eventRow}>
            <Toggle
              label={label}
              checked={settings.notifications[key].notify}
              onChange={(notify) => setEvent(key, { notify })}
              disabled={notificationsOff}
              hint={hint}
            />
            <Toggle
              label="Play a sound"
              checked={settings.notifications[key].sound}
              onChange={(sound) => setEvent(key, { sound })}
              disabled={notificationsOff || !settings.notifications[key].notify}
            />
          </div>
        ))}

        <NumberField
          label="Quiet period for repeats (seconds)"
          value={quietSecondsDraft}
          onChange={setQuietSecondsDraft}
          onCommit={(quietSeconds) => set('notifications', { quietSeconds })}
          min={0}
          max={3600}
          disabled={notificationsOff}
          hint="Ignores a session flipping back to a state it left less than this long ago. 0 disables."
          error={errorFor('notifications.quietSeconds')}
        />
      </>
    ),
    diagnostics: (
      <>
        <Select
          label="Daemon log level"
          value={logLevel ?? ''}
          options={levelOptions(logLevel)}
          onChange={changeLogLevel}
          disabled={!logLevel || !onLogLevelChange || blocked}
          hint={
            blocked ? disabledReason : logLevel ? undefined : 'Daemon unavailable.'
          }
          error={logLevelError ?? undefined}
        />
        <NumberField
          label="Log buffer size (entries)"
          value={logBufferSize}
          onChange={(size) => onLogBufferSizeChange?.(size)}
          min={100}
          disabled={!onLogBufferSizeChange}
          hint="How many recent log entries the viewer keeps in this browser."
        />
      </>
    ),
  };

  return (
    <div className={styles.view}>
      <header className={styles.header}>
        <h1 className={styles.title}>Settings</h1>
        <div className={styles.headerActions}>
          <Button
            onClick={() => reset()}
            disabled={blocked}
            title={disabledReason}
          >
            Reset all
          </Button>
          <Button variant="primary" onClick={onClose}>
            Close
          </Button>
        </div>
      </header>

      <div className={styles.body}>
        <nav
          ref={categoriesRef}
          className={styles.categories}
          aria-label="Settings categories"
        >
          {CATEGORIES.map((c) => (
            <button
              key={c.id}
              type="button"
              className={
                c.id === selected.id ? styles.categorySelected : styles.category
              }
              aria-current={c.id === selected.id ? 'true' : undefined}
              onClick={() => onSelectCategory(c.id)}
            >
              {c.label}
            </button>
          ))}
        </nav>

        <section className={styles.detail} aria-label={selected.label}>
          <div className={styles.detailHeader}>
            <h2 className={styles.detailTitle}>{selected.label}</h2>
            {canReset && (
              <Button
                onClick={handleReset}
                // Only a daemon-backed category's reset is a daemon write.
                // Diagnostics' reset also restores the browser-local buffer size,
                // which needs nothing, so it stays available while offline.
                disabled={blocked && daemonBacked}
                title={blocked && daemonBacked ? disabledReason : undefined}
              >
                Reset
              </Button>
            )}
          </div>
          {selected.description && (
            <p className={styles.detailDescription}>{selected.description}</p>
          )}
          {blocked && daemonBacked && (
            <p className={styles.loadError} role="alert">
              {disabledReason}
            </p>
          )}
          {loadFailed && (
            <p className={styles.loadError} role="alert">
              Couldn't load settings from the daemon — changes are disabled until
              it is reachable.
            </p>
          )}
          {generalError && (
            <p className={styles.loadError} role="alert">
              {generalError}
            </p>
          )}
          <fieldset
            className={styles.fields}
            disabled={fieldsetDisabled}
            title={fieldsetDisabled ? disabledReason : undefined}
          >
            {fields[selected.id]}
          </fieldset>
        </section>
      </div>
    </div>
  );
}

export default SettingsView;
