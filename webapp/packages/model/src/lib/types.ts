/**
 * The UI's view of the HomeShell domain: Project → Workstream → Session.
 * A presentation model carrying exactly what the UI renders.
 */

/**
 * Normalized session state. The daemon reports `idle`, `running`, `waiting`,
 * `starting` (the provider is still bringing it up — e.g. building a container),
 * `unknown` (signal lost), `stopped` (deliberately halted), and `error` (the
 * creation failed and the row stands for the attempt, with the reason in
 * {@link Session.error}). `done` is a UI-only overlay: a session the daemon
 * reports as `idle` that the user has not looked at since it finished reads as
 * `done` (see {@link displaySessionState}). Drives badge colors and labels for
 * individual sessions.
 */
export type SessionState =
  | 'idle'
  | 'running'
  | 'waiting'
  | 'starting'
  | 'unknown'
  | 'stopped'
  | 'done'
  | 'error';

export const SESSION_STATES: readonly SessionState[] = [
  'idle',
  'running',
  'waiting',
  'starting',
  'unknown',
  'stopped',
  'done',
  'error',
];

/** Maps an idle-but-unseen session to `done`; every other state passes through. */
export function displaySessionState(
  state: SessionState,
  seen: boolean,
): SessionState {
  return state === 'idle' && !seen ? 'done' : state;
}

/**
 * Whether a session is actively working or waiting for input. Used to reset the
 * `seen` flag (fresh activity means the next idle is again "unseen") and to gate
 * removal in the UI.
 *
 * `starting` is deliberately not active: there is no runtime behind it yet, so
 * the actions this gates (Stop above all) would be sent against a session the
 * provider cannot find. It is not terminal either — see
 * {@link isSessionTerminal} — so it sits outside both, which is what a session
 * that has not begun should do. Note the daemon's `SessionState.Busy()` *does*
 * count starting; that answers a different question (would removing the
 * workstream interrupt work), not "is there something live to drive".
 */
export function isSessionActive(state: SessionState): boolean {
  return state === 'running' || state === 'waiting';
}

/**
 * Whether a session has reached a terminal state — deliberately stopped,
 * finished, or failed to start — and so has no live terminal to interact with.
 * Mirrors the daemon's `SessionState.Terminal()`. Used to gate the send-text
 * input: a stopped session is rejected by the daemon (409), a done one has
 * nothing to type into, and an errored one never had a terminal at all.
 */
export function isSessionTerminal(state: SessionState): boolean {
  return state === 'stopped' || state === 'done' || state === 'error';
}

/**
 * Runtime state of a workspace container as reported by a provider. Orthogonal
 * to session states.
 */
export type RuntimeState =
  | 'idle'
  | 'unknown'
  | 'stopped'
  | 'running'
  | 'stopping'
  | 'starting'
  | 'creating'
  | 'error';

export const RUNTIME_STATES: readonly RuntimeState[] = [
  'idle',
  'unknown',
  'stopped',
  'running',
  'stopping',
  'starting',
  'creating',
  'error',
];

/** The display state combining runtime and session information, derived by the daemon. */
export type EffectiveState =
  | SessionState
  | 'stopped'
  | 'stopping'
  | 'starting'
  | 'creating'
  | 'error'
  | 'unknown';

export const EFFECTIVE_STATES: readonly EffectiveState[] = [
  'idle',
  'running',
  'waiting',
  'done',
  'stopped',
  'stopping',
  'starting',
  'creating',
  'error',
  'unknown',
];

/** What a status dot can show: an effective state, or a session that ended. */
export type DotState = EffectiveState | 'ended';

/** A single agent conversation running inside a workstream. */
export interface Session {
  id: string;
  title: string;
  /** Display name of the agent driving the session, e.g. "Claude Code". */
  agent: string;
  /**
   * Opaque ID of the session's owning session provider, distinct from the
   * workstream's provider. Resolves the session's own capabilities.
   */
  provider?: string;
  state: SessionState;
  /** Plain-text tail of the session's recent terminal output. */
  preview?: string;
  /** Provider-specific live observation detail; never persisted. */
  observationData?: unknown;
  /**
   * Why a session in the `error` state never started — the provider's own
   * message. Present only with that state, and it is the row's whole account of
   * itself: there is no way to clear the text short of removing the session,
   * which is what the row's × does.
   */
  error?: string;
  /** ID of the starting placeholder this session replaced, if it replaced one. */
  placeholderId?: string;
  /**
   * Whether the session can be restarted with settings, as far as the daemon
   * can tell up front: false for a placeholder or a failed create, for a
   * session starting or restarting, and for one its provider cannot restart.
   */
  restartable?: boolean;
  /**
   * The provider settings whose value differs from what the session would start
   * with now, in the provider's own names: its config changed since the session
   * started, and a restart applies the change.
   */
  outdatedSettings?: string[];
  /**
   * Whether the session has ended (stopped or done, not a failed start), as the
   * daemon decides: it is listed under Ended and counted as ended.
   */
  ended?: boolean;
  /** When the session ended (RFC 3339); omitted when the daemon does not know. */
  endedAt?: string;
}

/**
 * A session the daemon named when it refused to remove a workstream (`sessions`
 * on the 409 body). Rendered as-is: which sessions count as busy is the daemon's
 * judgement, never the UI's.
 */
export type BusySession = Pick<Session, 'id' | 'title' | 'state'>;

/**
 * The daemon's machine-readable `reason` on that 409 (api.ReasonSessionsRunning).
 * Branching on the code rather than the message keeps the UI off the daemon's
 * prose.
 */
export const REASON_SESSIONS_RUNNING = 'sessionsRunning';

/**
 * One way a workstream can be handed to the user for interactive work. The
 * daemon derives the set and authors the label; the UI echoes `id` back to open.
 */
export interface OpenMode {
  id: string;
  label: string;
  /** Opening this mode answers with a URL for the client to open in a tab. */
  opensInBrowser?: boolean;
  /**
   * This is the mode the plain open (`POST …/open`) runs. None carries it when
   * the plain open would fail, so the UI offers that shortcut only when some mode
   * carries it.
   */
  default?: boolean;
}

/** The ID of the daemon's workstream action that opens its note. */
export const NOTE_ACTION_ID = 'note';

/**
 * One thing that can be done to a workstream as a whole, beside opening it. Same
 * contract as {@link OpenMode}: the daemon derives the set and authors the
 * words, the UI renders one control per action and echoes `id` back to run it.
 */
export interface WorkstreamAction {
  id: string;
  label: string;
  /** What running it will do, for a tooltip. Authored by the daemon. */
  description?: string;
}

/**
 * What an open returned. `url` is present only when the client has to finish the
 * hand-off itself; every other mode is already open by the time this arrives.
 */
export interface OpenResult {
  mode: string;
  data?: { url?: string; [key: string]: unknown };
}

/** An isolated-or-not working environment for a project. */
export interface Workstream {
  id: string;
  name: string;
  /**
   * Opaque ID of the workstream's owning provider; capabilities are looked up
   * by it in the provider list. Display with providerLabel, never this.
   */
  provider: string;
  /** The provider's display label, when the daemon gives it one. */
  providerLabel?: string;
  /** Raw filesystem strategy identifier, e.g. "worktree", "copy", "in-place". */
  strategyId: string;
  /** Filesystem strategy display name, e.g. "worktree" or "in place". */
  strategy: string;
  /** Present for git-backed filesystem strategies. */
  branch?: string;
  /** Aggregate session state, derived by the daemon from the sessions. */
  state: SessionState;
  /** Workspace runtime state (start/stop/error) as reported by the provider. */
  runtimeState: RuntimeState;
  /** Combined display state (runtime + session), derived by the daemon. */
  effectiveState: EffectiveState;
  /** Human-readable progress message when the workstream is in a transitional state. */
  statusMessage?: string;
  /** The ways this workstream can be opened right now; empty means no open action. */
  openModes?: OpenMode[];
  /**
   * The workstream-level actions available right now, beside the open modes.
   * Derived by the daemon for the same reason: whether one is possible can
   * depend on its filesystem, which a client cannot see.
   */
  actions?: WorkstreamAction[];
  /** The folder the workstream works in, when the daemon knows it. */
  workingDir?: string;
  /** Set when that folder is the project's own; the daemon decides, by its taken-folder rule. */
  inProjectFolder?: boolean;
  sessions: Session[];
  /** ID of the creation placeholder this workstream replaced, if it replaced one. */
  placeholderId?: string;
  /**
   * Set on the placeholder of a create that failed: it stands for no
   * workstream, so it offers nothing but its removal, and statusMessage says why.
   */
  createFailed?: boolean;
  /**
   * Set on a worktree workstream whose folder is gone: the daemon keeps it
   * listed while a session in it has not ended, and removes it after.
   */
  folderMissing?: boolean;
}

/** A named, persisted reference to a codebase. */
export interface Project {
  id: string;
  name: string;
  path: string;
  gitBacked: boolean;
  /** Aggregate session state, derived by the daemon from the workstreams. */
  state: SessionState;
  /** Aggregate runtime state across all workstreams. */
  runtimeState: RuntimeState;
  /** Combined display state (runtime + session), derived by the daemon. */
  effectiveState: EffectiveState;
  workstreams: Workstream[];
  /** Git worktrees of the project that no workstream works in, offered for adding. */
  untrackedWorktrees: UntrackedWorktree[];
}

/** A git worktree of a project's repository that HomeShell does not track yet. */
export interface UntrackedWorktree {
  /** The name Add would give the workstream: the branch, else the folder's name. */
  name: string;
  /** Absent when HEAD is detached. */
  branch?: string;
  path: string;
  /** Sessions found running in the folder. */
  sessionCount: number;
}

/**
 * One daemon log entry as streamed by `GET /api/logs`. `seq` is a monotonic
 * sequence number used as a stable key and for stream ordering.
 */
export interface LogRecord {
  seq: number;
  time: string;
  level: string;
  msg: string;
  attrs?: Record<string, unknown>;
}

/**
 * A provider's declared capabilities, served by `GET /api/providers`. The UI
 * gates actions on these flags rather than on a provider's identity.
 */
export interface Capabilities {
  /** Opaque display strings for the modes the provider can open. */
  openModes: string[];
  /** Whether the provider can start/stop a workstream's runtime. */
  startStop: boolean;
  /** Whether the provider can deliver a one-off prompt to a session. */
  oneOffPrompt: boolean;
  /** Whether the provider can stop and (re)start individual sessions. */
  sessionStartStop: boolean;
  /**
   * Whether the provider can restart a session with a new config, keeping or
   * starting over its conversation.
   */
  sessionRestart: boolean;
  /** Max concurrent (non-terminal) sessions per workstream; 0 means unlimited. */
  maxConcurrentSessions: number;
  /** Whether the provider can detect the waiting (needs-input) state. */
  detectsWaiting: boolean;
}

/**
 * One session type a provider can create in a workstream: a stable id echoed
 * back on create, a label for the selector, and its creation-form JSON Schema.
 */
export interface SessionSchemaOption {
  id: string;
  label: string;
  schema?: unknown;
}

/** Groups the session types a single provider offers, from the session-schemas endpoint. */
export interface SessionProviderSchemas {
  providerId: string;
  label: string;
  options: SessionSchemaOption[];
}

/**
 * What a session was last started with (`GET …/sessions/{id}/launch-config`):
 * the provider and session option a restart starts it as, the config sent, or
 * null when there is none (a session the daemon adopted rather than created),
 * and the option's creation-form JSON Schema, null when it has none. A session
 * the daemon cannot restart is refused instead.
 */
export interface LaunchConfig {
  providerId: string;
  optionId: string;
  config: Record<string, unknown> | null;
  schema: unknown;
  /**
   * Why a restart cannot continue the session's conversation, so the daemon
   * accepts only a fresh one; absent when it can.
   */
  continueRefusal?: string;
}

/**
 * A user-facing notification pushed over the SSE `notification` event and
 * rendered as an in-app toast. Fire-once: never replayed on reconnect.
 */
export interface NotificationMessage {
  /** Stable id for this notification, used as a toast key. */
  id: string;
  /** What kind of activity fired it: `'waiting'` (needs input) or `'finished'`. */
  kind: string;
  title: string;
  body: string;
  /** Optional sound name the client maps to a tone; absent means silent. */
  sound?: string;
  projectId: string;
  projectName: string;
  workstreamId: string;
  sessionId: string;
  sessionTitle: string;
  agent: string;
}

/** A provider's availability; `reason` explains an 'unavailable' state. */
export interface ProviderStatus {
  state: 'available' | 'unavailable';
  reason?: string;
}

/**
 * A workstream provider from `GET /api/providers`: identity, status,
 * capabilities, and the schema for the workstream type it creates. Matched to a
 * workstream by `Workstream.provider` === `Provider.id`.
 */
export interface Provider {
  id: string;
  label: string;
  status: ProviderStatus;
  capabilities: Capabilities;
  /** JSON Schema describing the fields the UI solicits to create a workstream. */
  workstreamSchema?: unknown;
}
