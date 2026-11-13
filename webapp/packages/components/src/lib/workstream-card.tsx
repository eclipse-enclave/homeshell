import { displayPath, NOTE_ACTION_ID, sessionCounts } from '@homeshell/model';
import type {
  RuntimeState,
  Session,
  SessionProviderSchemas,
  SessionState,
  Workstream,
} from '@homeshell/model';
import { useEffect, useImperativeHandle, useRef, useState } from 'react';
import type { KeyboardEvent, Ref } from 'react';
import { Button } from './button';
import { CopyablePath } from './copyable-path';
import type { FormHandle, Refusal } from './create-form';
import {
  DeleteWorkstreamDialog,
  hasMaterializedFiles,
  type DeleteWorkstreamOptions,
} from './delete-workstream-dialog';
import { useReturnFocus } from './focus-return';
import {
  ExternalLinkIcon,
  FolderIcon,
  GitBranchIcon,
  GitForkIcon,
  PlusIcon,
  StickyNoteIcon,
  TrashIcon,
} from './icons';
import { type ItemAction, runAction, useActionsMenu } from './item-actions';
import { isDeleteKey, isModified } from './keys';
import { controlTabIndexFor, listItemKeyShortcuts } from './list-navigation';
import { MenuButton } from './menu-button';
import { assignMnemonics, MNEMONICS, mnemonicScope } from './mnemonic';
import { NewSessionForm, type NewSessionData } from './new-session-form';
import { renameInputClass, useRenamableTitle } from './renamable-title';
import { SessionCounts } from './session-counts';
import { SessionList } from './session-list';
import { SessionRow, type SessionRowHandle } from './session-row';
import { StatusDot } from './status-dot';
import { titleWhenTruncated } from './truncated-title';
import { useKeyboardFocus } from './use-keyboard-focus';
import styles from './workstream-card.module.css';

export interface WorkstreamCardProps {
  workstream: Workstream;
  /** The project's folder, which a separate folder's path is shown relative to. */
  projectPath: string;
  /** Opens a session in this workstream. Called with the session ID. */
  onOpenSession?: (sessionId: string) => void;
  /** Stops a session (wired only for sessions whose provider supports it). */
  onStopSession?: (sessionId: string) => void;
  /** (Re)starts a stopped session (wired only when supported). */
  onStartSession?: (sessionId: string) => void;
  /** Whether a session's own provider supports stop/start; defaults to true. */
  canSessionStartStop?: (session: Session) => boolean;
  /**
   * Asks for settings to restart a session with (see SessionRow); offered only
   * on the sessions the daemon marks restartable.
   */
  onRestartSession?: (sessionId: string) => void;
  /** Removes a non-busy session. */
  onRemoveSession?: (sessionId: string) => void;
  /**
   * Renames a session. The title is what the user typed, or the empty string when
   * they cleared the box — which asks for the provider's own name back (see
   * SessionRow.onRename). A returned message is a **refusal**, which the row shows
   * against itself rather than the page showing it somewhere else.
   */
  onRenameSession?: (
    sessionId: string,
    title: string,
  ) => void | Promise<string | null | void>;
  /** Sends a line of text to a session. Provided only when the provider supports one-off prompts. */
  onPromptSession?: (sessionId: string, text: string) => void;
  /** Sends a single named key to a session (menu navigation). Rides the same capability as onPromptSession. */
  onSendKeySession?: (sessionId: string, key: string) => void;
  /** Whether a session's own provider supports prompts and keys; defaults to true. */
  canPromptSession?: (session: Session) => boolean;
  /** Marks a session seen (clears its `done` overlay) when its row is clicked. */
  onSeenSession?: (sessionId: string) => void;
  /** Deletes the workstream. When omitted, no delete action is shown. */
  onDelete?: (opts: DeleteWorkstreamOptions) => void;
  /** Renames the workstream, resolving to null on success or an error message. */
  onRename?: (name: string) => Promise<string | null>;
  /** The default open, naming no mode. When omitted, no open control is shown. */
  onOpen?: () => void;
  /** Opens the workstream in one of the modes the daemon advertised (`openModes`). */
  onOpenWith?: (mode: string) => void;
  /**
   * Runs one of the workstream-level actions the daemon advertised (`actions`),
   * by ID. Rendered among the header's buttons, before Delete. Which actions
   * exist is the daemon's answer alone: the card renders what it is given,
   * with the daemon's own label and tooltip.
   */
  onAction?: (actionId: string) => void;
  /**
   * Discards the workstream's status message. The message is daemon state, so
   * this asks the daemon to clear it (every client then drops it) rather than
   * hiding it locally — a local hide would leave it in the projection and it
   * would be back on the next reload. When omitted, no dismiss affordance is
   * shown.
   *
   * Not offered on a workstream in `error` state, whatever is passed here: there
   * the message is not a note attached to a working workstream but the failure's
   * only account of itself, so dismissing it would leave a permanently red card
   * that never explains why. The daemon refuses it too (409) — this just keeps
   * the UI from offering a button that cannot work.
   */
  onDismissStatus?: () => void;
  /** When true, Delete skips the confirmation dialog (keepFiles: false). */
  skipConfirmation?: boolean;
  /** Lazily loads the New session form's options; when omitted the action is disabled. */
  loadSessionOptions?: () => Promise<SessionProviderSchemas[]>;
  /** Creates a session from the chosen option; returns null on success. */
  onCreateSession?: (data: NewSessionData) => Promise<string | Refusal | null>;
  /** UI display-state override per session (e.g. the computed `done`). */
  displayStateFor?: (session: Session) => SessionState;
  /** How many terminal-tail lines an expanded session row shows. */
  previewLines?: number;
  /**
   * When set, every action on this card and its session rows that needs the
   * daemon is disabled, with this text as the reason (see SessionRow). Purely
   * local controls — expanding a session's output panel, cancelling a form —
   * stay live, because nothing about them needs the daemon.
   */
  disabledReason?: string;
  /**
   * The workstream or session ID holding the list's single tab stop (roving
   * tabindex): that card or row gets tabIndex 0, every other one -1. Set, the
   * card and its rows become keyboard-navigable items — with the card focused,
   * Enter opens the workstream, F2 renames it and Delete deletes it. Unset,
   * neither is focusable itself.
   */
  tabStopId?: string;
  /**
   * The list's selected card or row, which commands on the selection act on:
   * it is marked as current, and drawn so even while focus is elsewhere.
   */
  selectedId?: string;
  ref?: Ref<WorkstreamCardHandle>;
}

export interface WorkstreamCardHandle {
  /** What New session does: opens the form, or focuses it again if open. */
  newSession: () => void;
  /**
   * What the card, or one of its session rows, offers — as its buttons and its
   * actions menu do. Empty for an ID that is neither.
   */
  actions: (itemId: string) => ItemAction[];
}

/**
 * Runtime states in which the workstream is live, so its sessions say what is
 * happening in it. In any other the runtime's own state is the news: setting
 * up, starting, stopping, stopped or failed.
 */
const LIVE_RUNTIME: readonly RuntimeState[] = ['running', 'idle', 'unknown'];

/** A workstream and the sessions it hosts, with the actions its providers support. */
export function WorkstreamCard({
  workstream,
  projectPath,
  onOpenSession,
  onStopSession,
  onStartSession,
  canSessionStartStop,
  onRestartSession,
  onRemoveSession,
  onRenameSession,
  onSeenSession,
  onPromptSession,
  onSendKeySession,
  canPromptSession,
  onDelete,
  onRename,
  onOpen,
  onOpenWith,
  onAction,
  onDismissStatus,
  skipConfirmation,
  loadSessionOptions,
  onCreateSession,
  displayStateFor,
  previewLines,
  disabledReason,
  tabStopId,
  selectedId,
  ref,
}: WorkstreamCardProps) {
  const blocked = Boolean(disabledReason);
  const [confirming, setConfirming] = useState(false);
  const [creating, setCreating] = useState(false);
  const [options, setOptions] = useState<SessionProviderSchemas[] | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const canCreate = Boolean(loadSessionOptions && onCreateSession);
  const [renaming, setRenaming] = useState(false);
  const [draftName, setDraftName] = useState(workstream.name);
  const [renameError, setRenameError] = useState<string | null>(null);
  const renameInputRef = useRef<HTMLInputElement>(null);
  const cardRef = useRef<HTMLElement>(null);
  const newSessionButtonRef = useRef<HTMLButtonElement>(null);
  const newSessionFormRef = useRef<FormHandle>(null);
  const headerRef = useRef<HTMLElement>(null);
  const rows = useRef(new Map<string, SessionRowHandle>());
  const navigable = tabStopId !== undefined;
  const { focusedByKeyboard, focusHandlers } = useKeyboardFocus();
  // Back to what had focus when the rename started, else to the card — or,
  // where it cannot take focus, to its first button.
  const setReturnRenameFocus = useReturnFocus(renaming, () =>
    navigable ? cardRef.current : newSessionButtonRef.current,
  );
  const setReturnCreateFocus = useReturnFocus(
    creating,
    () => newSessionButtonRef.current,
  );

  // A `creating-…` placeholder stands for a workstream the daemon is still
  // making: it has no identity of its own yet and is replaced wholesale by the
  // real one. So a rename here would be dropped (the PATCH 404s after the swap),
  // and the footer's count would be describing something that does not exist.
  const isPlaceholder = workstream.effectiveState === 'creating';
  // A failed create's placeholder stands for nothing at all: Delete is all it
  // offers.
  const failed = Boolean(workstream.createFailed);
  const canRename = Boolean(onRename) && !isPlaceholder && !failed;

  // Close the delete confirmation only when switching to a different workstream.
  useEffect(() => {
    setConfirming(false);
  }, [workstream.id]);

  // Exit rename mode and re-sync the draft when the workstream or its name
  // changes underneath us.
  useEffect(() => {
    setRenaming(false);
    setDraftName(workstream.name);
  }, [workstream.id, workstream.name]);

  useEffect(() => {
    if (renaming) renameInputRef.current?.select();
  }, [renaming]);

  const startCreating = async () => {
    if (!loadSessionOptions) return;
    setCreating(true);
    setOptions(null);
    setLoadError(null);
    try {
      setOptions(await loadSessionOptions());
    } catch (cause) {
      setLoadError(
        cause instanceof Error && cause.message
          ? cause.message
          : 'Could not load session types.',
      );
    }
  };

  const startNewSession = () =>
    creating ? newSessionFormRef.current?.focus() : startCreating();

  const cancelCreating = () => {
    setReturnCreateFocus(true);
    setCreating(false);
  };

  const handleCreate = async (
    data: NewSessionData,
  ): Promise<string | Refusal | null> => {
    if (!onCreateSession) return 'Cannot create sessions here.';
    const result = await onCreateSession(data);
    if (!result) setCreating(false);
    return result;
  };

  const startRename = () => {
    setDraftName(workstream.name);
    setRenameError(null);
    setRenaming(true);
  };

  const commitRename = async () => {
    // The rename is a daemon call, so it is refused here rather than by disabling
    // the box. Disabling a focused input blurs it, and the blur is what commits —
    // losing the daemon mid-edit would fire the very request this exists to stop,
    // then strand the user in a dead field holding unsaved text. The box stays
    // editable and says why instead.
    if (blocked) {
      setRenameError(disabledReason ?? null);
      setReturnRenameFocus(false);
      return;
    }
    const trimmed = draftName.trim();
    if (!trimmed || trimmed === workstream.name) {
      setRenaming(false);
      setDraftName(workstream.name);
      setRenameError(null);
      return;
    }
    const error = (await onRename?.(trimmed)) ?? null;
    setRenameError(error);
    // A failed rename keeps the field and the draft so the user can correct it.
    // No refocus: focus is already where the user left it.
    if (!error) setRenaming(false);
    else setReturnRenameFocus(false);
  };

  const cancelRename = () => {
    setRenaming(false);
    setDraftName(workstream.name);
    setRenameError(null);
  };

  const handleRenameKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key === 'Enter') {
      event.preventDefault();
      setReturnRenameFocus(true);
      commitRename();
    } else if (event.key === 'Escape') {
      setReturnRenameFocus(true);
      cancelRename();
    }
  };

  const requestDelete = () => {
    if (!onDelete) return;
    // Workstreams with files always show the dialog, so the user can choose
    // "Keep files"; only in-place can skip it.
    if (skipConfirmation && !hasMaterializedFiles(workstream)) {
      onDelete({ keepFiles: false });
    } else {
      setConfirming(true);
    }
  };

  const tabIndexFor = (id: string) =>
    navigable ? (id === tabStopId ? 0 : -1) : undefined;
  const controlTabIndex = controlTabIndexFor(tabIndexFor(workstream.id));

  const dismissible =
    Boolean(workstream.statusMessage) && workstream.runtimeState !== 'error';

  // The one account of what the card offers: its buttons, its keys and its
  // actions menu all read it.
  // F2 and Delete act on the card only where it can take focus.
  const rename: ItemAction | undefined = canRename
    ? {
        id: 'rename',
        label: 'Rename',
        run: startRename,
        disabled: blocked,
        shortcut: navigable ? 'f2' : undefined,
      }
    : undefined;
  const deletion: ItemAction | undefined = onDelete
    ? {
        id: 'delete',
        label: 'Delete',
        run: requestDelete,
        disabled: blocked,
        destructive: true,
        shortcut: navigable ? 'delete' : undefined,
      }
    : undefined;
  const runOpen = failed ? undefined : onOpen;
  const canOpen = runOpen !== undefined;
  // The plain open runs the mode the daemon marked default; without one it
  // would fail, so there is no such action and the Open control is the menu.
  const hasDefaultOpen =
    workstream.openModes?.some((mode) => mode.default) ?? false;
  const open: ItemAction | undefined =
    runOpen && hasDefaultOpen
      ? { id: 'open', label: 'Open', run: runOpen, disabled: blocked }
      : undefined;
  const openModes = (canOpen ? (workstream.openModes ?? []) : []).map(
    (mode) => ({
      mode,
      action: {
        id: `open:${mode.id}`,
        label: `Open with ${mode.label}`,
        run: () => onOpenWith?.(mode.id),
        disabled: blocked,
      },
    }),
  );
  const newSession: ItemAction | undefined = failed
    ? undefined
    : {
        id: 'newSession',
        label: 'New session',
        run: startNewSession,
        disabled: !canCreate || blocked || isPlaceholder,
      };
  const advertisedEntries =
    onAction && !isPlaceholder && !failed ? (workstream.actions ?? []) : [];
  // An advertised action takes the first letter of its label that no other
  // action leaves taken, the page's included.
  const advertisedLetters = assignMnemonics(
    advertisedEntries.map((entry) => entry.label),
    Object.values(MNEMONICS),
  );
  const advertised = advertisedEntries.map((entry, index) => ({
    entry,
    mnemonic: advertisedLetters[index],
    action: {
      id: `action:${entry.id}`,
      label: entry.label,
      run: () => onAction?.(entry.id),
      disabled: blocked,
    },
  }));
  const dismiss: ItemAction | undefined =
    onDismissStatus && dismissible
      ? {
          id: 'dismiss',
          label: 'Dismiss message',
          run: onDismissStatus,
          disabled: blocked,
        }
      : undefined;
  // In the order of the session rows': opening first, what deletes last.
  const actions = [
    open,
    ...openModes.map(({ action }) => action),
    newSession,
    rename,
    ...advertised.map(({ action }) => action),
    dismiss,
    deletion,
  ].filter((entry) => entry !== undefined);

  useImperativeHandle(ref, () => ({
    newSession: () => {
      if (newSession && !newSession.disabled) newSession.run();
    },
    actions: (itemId) =>
      itemId === workstream.id
        ? actions
        : (rows.current.get(itemId)?.actions() ?? []),
  }));
  const actionsMenu = useActionsMenu(
    `Actions for workstream ${workstream.name}`,
    actions,
  );

  // Only the focused card itself, and only once the keyboard put focus there:
  // keys pressed inside it belong to the control or session row that has it.
  const handleCardKeyDown = (event: KeyboardEvent<HTMLElement>) => {
    if (event.target !== event.currentTarget || !focusedByKeyboard()) return;
    if (actionsMenu.openOn(event, headerRef.current)) return;
    if (isModified(event) || event.repeat) return;
    if (event.key === 'Enter') runAction(event, actions, 'open');
    else if (event.key === 'F2') runAction(event, actions, 'rename');
    else if (isDeleteKey(event)) runAction(event, actions, 'delete');
  };

  // The branch says nothing the name does not when the two are the same, which
  // is what a workstream named for its branch (the default) always is.
  const branch =
    workstream.branch !== workstream.name ? workstream.branch : undefined;
  // Where a separate folder is and how it was made; the project folder's own
  // workstream needs neither, as its section says where it works, but names
  // its provider all the same.
  const separate = !workstream.inProjectFolder;
  const meta =
    Boolean(workstream.providerLabel) ||
    (separate && Boolean(workstream.strategy || workstream.workingDir));
  const runtimeNews = !LIVE_RUNTIME.includes(workstream.runtimeState);
  // A renamable name is how it is renamed, and says so; any other is titled
  // only while it is cut off.
  const renamableName = useRenamableTitle<HTMLHeadingElement>({
    name: workstream.name,
    onRename: startRename,
    disabledReason,
    f2: navigable,
    className: styles.name,
  });
  const nameProps = canRename
    ? renamableName
    : { ...titleWhenTruncated(workstream.name), className: styles.name };

  const sessionRow = (session: Session) => {
    const startStop = canSessionStartStop ? canSessionStartStop(session) : true;
    // Gated on the session's own provider, not the workstream's.
    const prompt = canPromptSession ? canPromptSession(session) : true;
    return (
      <SessionRow
        key={session.id}
        ref={(handle) => {
          if (handle) rows.current.set(session.id, handle);
          else rows.current.delete(session.id);
        }}
        session={session}
        displayState={displayStateFor?.(session)}
        onOpen={onOpenSession ? () => onOpenSession(session.id) : undefined}
        onStop={
          startStop && onStopSession
            ? () => onStopSession(session.id)
            : undefined
        }
        onStart={
          startStop && onStartSession
            ? () => onStartSession(session.id)
            : undefined
        }
        onRestartWithSettings={
          onRestartSession && session.restartable
            ? () => onRestartSession(session.id)
            : undefined
        }
        onRemove={
          onRemoveSession ? () => onRemoveSession(session.id) : undefined
        }
        onRename={
          onRenameSession
            ? (title) => onRenameSession(session.id, title)
            : undefined
        }
        onSeen={onSeenSession ? () => onSeenSession(session.id) : undefined}
        onPrompt={
          prompt && onPromptSession
            ? (text) => onPromptSession(session.id, text)
            : undefined
        }
        onSendKey={
          prompt && onSendKeySession
            ? (key) => onSendKeySession(session.id, key)
            : undefined
        }
        previewLines={previewLines}
        disabledReason={disabledReason}
        tabIndex={tabIndexFor(session.id)}
        selected={selectedId === session.id}
      />
    );
  };

  return (
    <section
      ref={cardRef}
      className={styles.card}
      aria-label={`Workstream ${workstream.name}`}
      aria-busy={isPlaceholder || undefined}
      tabIndex={tabIndexFor(workstream.id)}
      data-nav-id={workstream.id}
      {...mnemonicScope('item')}
      aria-current={selectedId === workstream.id || undefined}
      aria-keyshortcuts={navigable ? listItemKeyShortcuts('card') : undefined}
      onKeyDown={handleCardKeyDown}
      onContextMenu={(event) =>
        actionsMenu.openOnContextMenu(event, headerRef.current)
      }
      {...focusHandlers}
    >
      <header ref={headerRef} className={styles.header}>
        <div className={styles.identity}>
          <div className={styles.titleLine}>
            {renaming ? (
              <input
                ref={renameInputRef}
                className={`${renameInputClass} ${styles.nameInput}`}
                type="text"
                value={draftName}
                onChange={(event) => {
                  setDraftName(event.target.value);
                  setRenameError(null);
                }}
                onBlur={commitRename}
                onKeyDown={handleRenameKeyDown}
                aria-label="Workstream name"
              />
            ) : (
              <h3 {...nameProps}>{workstream.name}</h3>
            )}
            {branch && (
              <span className={styles.branch}>
                <GitBranchIcon size={13} />
                <span
                  className={styles.branchName}
                  {...titleWhenTruncated(branch)}
                >
                  {branch}
                </span>
              </span>
            )}
          </div>
          {meta && (
            <div className={styles.meta}>
              {workstream.providerLabel && (
                <span>{workstream.providerLabel}</span>
              )}
              {separate && (
                <>
                  {workstream.strategyId === 'worktree' && (
                    <GitForkIcon size={13} />
                  )}
                  {workstream.strategyId === 'copy' && <FolderIcon size={13} />}
                  {workstream.strategy && <span>{workstream.strategy}</span>}
                  {workstream.workingDir && (
                    <CopyablePath
                      className={styles.path}
                      tabIndex={controlTabIndex}
                      path={workstream.workingDir}
                      label={displayPath(workstream.workingDir, projectPath)}
                    />
                  )}
                </>
              )}
            </div>
          )}
        </div>
        <div className={styles.controls}>
          <span className={styles.status}>
            {runtimeNews || isPlaceholder || failed ? (
              <span className={styles.state} data-testid="workstream-state">
                <StatusDot state={workstream.effectiveState} showLabel />
              </span>
            ) : (
              <SessionCounts
                counts={sessionCounts(
                  workstream.sessions,
                  (session) => displayStateFor?.(session) ?? session.state,
                )}
              />
            )}
          </span>
          {canOpen && (
            // A split control: the button is the default open, the menu holds the
            // daemon-advertised modes. Without a default mode the plain open would
            // fail, so the control is the menu alone.
            <MenuButton
              label="Open"
              icon={<ExternalLinkIcon size={13} />}
              items={workstream.openModes ?? []}
              onPrimary={open?.run}
              onSelect={(id) =>
                openModes.find(({ mode }) => mode.id === id)?.action.run()
              }
              ariaLabel={`Open workstream ${workstream.name}`}
              title={
                disabledReason ?? 'Open this workstream for interactive work'
              }
              disabled={blocked}
              tabIndex={controlTabIndex}
              mnemonic={MNEMONICS.open}
            />
          )}
          {newSession && (
            <Button
              ref={newSessionButtonRef}
              variant="primary"
              icon={<PlusIcon size={14} />}
              onClick={newSession.run}
              disabled={newSession.disabled}
              tabIndex={controlTabIndex}
              mnemonic={MNEMONICS.newChild}
              title={
                disabledReason ??
                (isPlaceholder
                  ? 'Available once the workstream is set up'
                  : canCreate
                    ? 'Start a new session'
                    : 'Not available for this provider')
              }
            >
              New session
            </Button>
          )}
          {advertised.map(({ entry, action, mnemonic }) => {
            // The note is an icon; the others show their label.
            const note = entry.id === NOTE_ACTION_ID;
            return (
              <Button
                key={entry.id}
                icon={note ? <StickyNoteIcon size={14} /> : undefined}
                onClick={action.run}
                disabled={action.disabled}
                tabIndex={controlTabIndex}
                mnemonic={mnemonic}
                aria-label={`${action.label} for workstream ${workstream.name}`}
                title={disabledReason ?? entry.description ?? action.label}
              >
                {note ? undefined : action.label}
              </Button>
            );
          })}
          {deletion && (
            <Button
              icon={<TrashIcon size={14} />}
              disabled={deletion.disabled}
              tabIndex={controlTabIndex}
              mnemonic={MNEMONICS.delete}
              shortcuts={navigable ? ['delete'] : undefined}
              title={disabledReason ?? 'Delete this workstream'}
              onClick={deletion.run}
              aria-label={`Delete workstream ${workstream.name}`}
            />
          )}
          {/* A failed create offers only Delete, which is already here. While
              everything is disabled (offline), so is this, in its place. */}
          {!failed &&
            actionsMenu.moreActionsButton({
              tabIndex: controlTabIndex,
              title: disabledReason,
            })}
        </div>
      </header>
      {renameError && (
        <p className={styles.renameError} role="alert">
          {renameError}
        </p>
      )}
      {actionsMenu.menu}
      {confirming && onDelete && (
        <DeleteWorkstreamDialog
          workstream={workstream}
          onConfirm={(opts) => {
            setConfirming(false);
            onDelete(opts);
          }}
          onCancel={() => setConfirming(false)}
        />
      )}
      {workstream.statusMessage && (
        <p className={styles.statusMessage}>
          <span className={styles.statusText}>{workstream.statusMessage}</span>
          {dismiss && (
            <button
              type="button"
              className={styles.statusDismiss}
              onClick={dismiss.run}
              disabled={dismiss.disabled}
              tabIndex={controlTabIndex}
              aria-label={`Dismiss message for workstream ${workstream.name}`}
              title={disabledReason ?? 'Dismiss this message'}
            >
              ×
            </button>
          )}
        </p>
      )}
      {creating &&
        (loadError ? (
          <p className={styles.empty} role="alert">
            {loadError}
          </p>
        ) : options !== null ? (
          <NewSessionForm
            ref={newSessionFormRef}
            providers={options}
            onSubmit={handleCreate}
            onCancel={cancelCreating}
            disabledReason={disabledReason}
          />
        ) : (
          <p className={styles.empty}>Loading session types…</p>
        ))}
      {workstream.sessions.length === 0 &&
        !creating &&
        !isPlaceholder &&
        !failed && <p className={styles.empty}>No sessions yet.</p>}
      {workstream.sessions.length > 0 && (
        <SessionList
          sessions={workstream.sessions}
          selectedId={selectedId}
          tabStopId={tabStopId}
          tabIndexFor={tabIndexFor}
          renderRow={sessionRow}
        />
      )}
    </section>
  );
}

export default WorkstreamCard;
