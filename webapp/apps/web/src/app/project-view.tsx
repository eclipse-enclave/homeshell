import {
  Button,
  BusyWorkstreamDialog,
  ConfirmDialog,
  CopyablePath,
  type DeleteWorkstreamOptions,
  describeListKeys,
  focusNavItem,
  FolderOpenIcon,
  FolderTreeIcon,
  MNEMONICS,
  mnemonicScope,
  type NewSessionData,
  type NewWorkstreamData,
  NewWorkstreamForm,
  Pill,
  PlusIcon,
  type Refusal,
  renameInputClass,
  RestartSessionDialog,
  type RestartSessionData,
  tabStopItem,
  TrashIcon,
  useCreateForm,
  useListTabStop,
  useRenamableTitle,
  useReturnFocus,
  UntrackedWorktrees,
  untrackedNavIds,
  WorkstreamCard,
  type WorkstreamCardHandle,
} from '@homeshell/components';
import {
  NOTE_ACTION_ID,
  type BusySession,
  type Capabilities,
  type Project,
  type Provider,
  type Workstream,
} from '@homeshell/model';
import {
  type KeyboardEvent,
  type Ref,
  useCallback,
  useEffect,
  useId,
  useImperativeHandle,
  useLayoutEffect,
  useRef,
  useState,
} from 'react';
import { usePendingFocus } from './keyboard/use-pending-focus';
import {
  addDefaultWorkstream,
  addUntrackedWorktree,
  createSession,
  createWorkstream,
  deleteWorkstream,
  dismissWorkstreamStatus,
  fetchProviders,
  fetchLaunchConfig,
  fetchSessionSchemas,
  fetchWorkstreamProviders,
  openSession,
  openWorkstream,
  openWorkstreamNote,
  promptSession,
  refusalMessage,
  removeProject,
  renameProject,
  renameSession,
  renameWorkstream,
  resolveWorkstreamDraft,
  restartSession,
  sendSessionKey,
  sessionAction,
} from './project-requests';
import styles from './project-view.module.css';
import { resolveDefault, type Settings } from './settings/settings-types';
import { useSettings } from './settings/use-settings';
import { useProjectCommands } from './use-project-commands';
import { useSessionAttention } from './use-session-attention';

/** What `creating` holds for Work here: a worktree path is never this. */
const WORK_HERE = 'work-here';

const LIST_KEYS_HINT = describeListKeys();

export interface ProjectViewProps {
  project: Project;
  /**
   * When set, every action in this view that needs the daemon is disabled and this
   * text says why. Supplied by App from the event-stream connection
   * (`offlineReason`); the view itself knows nothing about transport.
   */
  disabledReason?: string;
  /** Called once the daemon has removed the project, with its ID. */
  onRemoved?: (projectId: string) => void;
  ref?: Ref<ProjectViewHandle>;
}

export interface ProjectViewHandle {
  /**
   * Focuses the card or row `itemId` names, else the one holding the list's tab
   * stop, else — with no workstreams — the button that creates the first.
   */
  focus: (itemId?: string) => void;
}

/** Whether a create was refused, rather than answered with what it made. */
function isRefusal<T extends object>(
  result: T | string | Refusal,
): result is string | Refusal {
  return typeof result === 'string' || 'message' in result;
}

/** Expands a workstream name template, or returns undefined for an empty one. */
function expandNameTemplate(
  template: string,
  project: Project,
): string | undefined {
  const trimmed = template.trim();
  if (!trimmed) return undefined;
  return trimmed.replace(/\{project\}/g, project.name);
}

/**
 * Overrides for the create form's schema fields. Only settings the user chose
 * explicitly are included; blanks defer to the provider schema's own default.
 */
function workstreamConfigDefaults(
  workstreams: Settings['workstreams'],
): Record<string, unknown> | undefined {
  const overrides: Record<string, unknown> = {};
  const strategy = resolveDefault(workstreams.defaultStrategy);
  if (strategy) overrides.strategy = strategy;
  return Object.keys(overrides).length > 0 ? overrides : undefined;
}

/** The selected project: header plus its workstreams and their sessions. */
export function ProjectView({
  project,
  disabledReason,
  onRemoved,
  ref,
}: ProjectViewProps) {
  const { settings, update } = useSettings();
  const blocked = Boolean(disabledReason);
  const [editing, setEditing] = useState(false);
  const [editName, setEditName] = useState(project.name);
  const [error, setError] = useState<string | null>(null);
  // Why a rename was refused: the box's own, so editing it again clears it
  // without taking an unrelated failure on the page with it.
  const [renameError, setRenameError] = useState<string | null>(null);
  const [confirming, setConfirming] = useState(false);
  const [creatable, setCreatable] = useState<Provider[] | null>(null);
  const [caps, setCaps] = useState<Record<string, Capabilities>>({});
  // A removal the daemon refused for running sessions, held until the user
  // decides: the original options travel with it so the forced retry keeps the
  // keep-files choice they already made.
  const [busyRemoval, setBusyRemoval] = useState<{
    workstreamId: string;
    opts: DeleteWorkstreamOptions;
    sessions: BusySession[];
  } | null>(null);
  // A session being restarted with settings: its option's schema, what it was
  // last started with, and why it cannot continue its conversation, if it
  // cannot, loaded before the dialog opens.
  const [restarting, setRestarting] = useState<{
    workstreamId: string;
    sessionId: string;
    schema: unknown;
    config?: Record<string, unknown>;
    continueRefusal?: string;
  } | null>(null);
  // The load the restart dialog waits on, one at a time; replaced, its answer
  // is dropped.
  const restartLoad = useRef<object | null>(null);
  const [loadingRestart, setLoadingRestart] = useState(false);
  const sessionOf = (workstreamId: string, sessionId: string) =>
    project.workstreams
      .find((workstream) => workstream.id === workstreamId)
      ?.sessions.find((session) => session.id === sessionId);
  // Named as the session is now. Once it is gone the dialog closes, a commit
  // after its row did, so that focus falls back rather than to the row.
  const restartingSession =
    restarting && sessionOf(restarting.workstreamId, restarting.sessionId);
  useLayoutEffect(() => {
    if (restarting && !restartingSession) setRestarting(null);
  }, [restarting, restartingSession]);
  const attention = useSessionAttention(project);
  const inputRef = useRef<HTMLInputElement>(null);
  const newWorkstreamRef = useRef<HTMLButtonElement>(null);
  const create = useCreateForm(() => newWorkstreamRef.current);
  const closeCreate = create.close;
  const cards = useRef(new Map<string, WorkstreamCardHandle>());
  // The project folder's workstream(s) first, then the separate folders: the
  // order the page shows them in, which the tab stop follows.
  const inPlace = project.workstreams.filter((ws) => ws.inProjectFolder);
  const separate = project.workstreams.filter((ws) => !ws.inProjectFolder);
  const workHereRef = useRef<HTMLButtonElement>(null);
  // The project whose cards, last time it had any, were all in the project
  // folder: once the last of them goes, focus stays in that section.
  const emptiedFolder = useRef<string | null>(null);
  const list = useListTabStop([...inPlace, ...separate], {
    resetKey: project.id,
    trailing: untrackedNavIds(project.untrackedWorktrees),
    emptyFocus: () =>
      (emptiedFolder.current === project.id && workHereRef.current) ||
      newWorkstreamRef.current,
  });
  useLayoutEffect(() => {
    if (project.workstreams.length > 0)
      emptiedFolder.current = separate.length === 0 ? project.id : null;
  });

  // A card or row just created, to focus once it is listed: the placeholder the
  // daemon answered with, or what replaced it. Given up if focus has meanwhile
  // gone somewhere outside the list.
  const focusWhenListed = usePendingFocus((placeholderId: string) => {
    const listed = list.listRef.current;
    if (!listed) return false;
    const active = document.activeElement;
    if (active !== document.body && !listed.contains(active)) return true;
    const created = project.workstreams
      .flatMap((workstream) => [workstream, ...workstream.sessions])
      .find(
        (item) =>
          item.id === placeholderId || item.placeholderId === placeholderId,
      );
    if (!created) return false;
    focusNavItem(listed, created.id);
    return true;
  });

  useImperativeHandle(ref, () => ({ focus: list.focus }));

  useEffect(() => {
    setEditName(project.name);
    setEditing(false);
    setError(null);
    setRenameError(null);
    setConfirming(false);
    closeCreate();
    // A pending force-confirmation belongs to the project it was raised in.
    setBusyRemoval(null);
    setRestarting(null);
    restartLoad.current = null;
    setLoadingRestart(false);
  }, [project.id, project.name, closeCreate]);

  // Provider capabilities gate the per-session actions. Fetched once.
  useEffect(() => {
    let cancelled = false;
    fetchProviders().then((providers) => {
      if (cancelled) return;
      const byId: Record<string, Capabilities> = {};
      for (const provider of providers)
        byId[provider.id] = provider.capabilities;
      setCaps(byId);
    });
    return () => {
      cancelled = true;
    };
  }, []);

  useEffect(() => {
    if (!create.open) return;
    let cancelled = false;
    setCreatable(null);
    fetchWorkstreamProviders(project.id).then((providers) => {
      if (!cancelled) setCreatable(providers);
    });
    return () => {
      cancelled = true;
    };
  }, [create.open, project.id]);

  const resolveDraft = useCallback(
    (data: NewWorkstreamData) => resolveWorkstreamDraft(project.id, data),
    [project.id],
  );

  // The title that started it is not focusable, so focus falls back to New
  // workstream rather than to the header's Delete.
  const setReturnRenameFocus = useReturnFocus(
    editing,
    () => newWorkstreamRef.current,
  );

  const handleCreate = async (
    data: NewWorkstreamData,
  ): Promise<string | Refusal | null> => {
    const result = await createWorkstream(project.id, data);
    if (isRefusal(result)) return result;
    focusWhenListed(result.id);
    create.close();
    const strategy = data.config.strategy;
    update((prev) => ({
      ...prev,
      workstreams: {
        ...prev.workstreams,
        defaultProvider: {
          ...prev.workstreams.defaultProvider,
          lastUsed: data.providerId,
        },
        defaultStrategy:
          typeof strategy === 'string'
            ? { ...prev.workstreams.defaultStrategy, lastUsed: strategy }
            : prev.workstreams.defaultStrategy,
      },
    }));
    return null;
  };

  // The one create in flight: the worktree path of an Add, or WORK_HERE. A
  // second would only be refused, the folder taken.
  const [creating, setCreating] = useState<string | null>(null);
  const createHere = async (
    key: string,
    request: () => Promise<Workstream | string>,
  ) => {
    if (creating !== null) return;
    setCreating(key);
    try {
      const result = await request();
      if (typeof result === 'string') setError(result);
      else focusWhenListed(result.id);
    } finally {
      setCreating(null);
    }
  };
  const handleWorkHere = () =>
    createHere(WORK_HERE, () => addDefaultWorkstream(project.id));
  const handleAddWorktree = (path: string) =>
    createHere(path, () => addUntrackedWorktree(project.id, path));

  const handleCreateSession = async (
    workstreamId: string,
    data: NewSessionData,
  ): Promise<string | Refusal | null> => {
    const result = await createSession(project.id, workstreamId, data);
    if (isRefusal(result)) return result;
    focusWhenListed(result.id);
    return null;
  };

  /**
   * Removes a workstream, never forcing on the first attempt: the daemon owns
   * the running-session guard, so the UI asks, and only escalates to force when
   * the daemon says why it refused. The file choice the user already made
   * (keepFiles) rides along on the retry.
   */
  const handleDeleteWorkstream = async (
    workstreamId: string,
    opts: DeleteWorkstreamOptions,
  ) => {
    const outcome = await deleteWorkstream(project.id, workstreamId, opts);
    if (outcome.status === 'error') setError(outcome.message);
    if (outcome.status === 'busy') {
      setBusyRemoval({ workstreamId, opts, sessions: outcome.sessions });
    }
  };

  const handleForceDeleteWorkstream = async () => {
    if (!busyRemoval) return;
    const { workstreamId, opts } = busyRemoval;
    setBusyRemoval(null);
    const outcome = await deleteWorkstream(
      project.id,
      workstreamId,
      opts,
      true,
    );
    if (outcome.status === 'error') setError(outcome.message);
  };

  // Returns the daemon's message on failure so the card can keep the edit field
  // open and show the error beside it. Success arrives over SSE.
  const handleRenameWorkstream = (workstreamId: string, name: string) =>
    renameWorkstream(project.id, workstreamId, name);

  const handleOpenWorkstream = async (workstreamId: string, mode?: string) => {
    // A mode the daemon declared as browser-bound gets its tab opened here,
    // inside the click: one opened after the request resolves falls outside the
    // user-gesture window and popup blockers swallow it. `opener` is cleared for
    // the protection `noopener` would have given, which cannot be used because
    // it makes window.open return null.
    const opensInBrowser = mode
      ? project.workstreams
          .find((workstream) => workstream.id === workstreamId)
          ?.openModes?.find((candidate) => candidate.id === mode)
          ?.opensInBrowser === true
      : false;
    let tab: Window | null = null;
    if (opensInBrowser) {
      tab = window.open('', '_blank');
      if (tab) tab.opener = null;
    }
    const result = await openWorkstream(project.id, workstreamId, mode, tab);
    if (result) setError(result);
  };

  // The daemon decides which actions a workstream has; this only maps an ID it
  // advertised to the request that runs it. An unknown ID is a daemon newer than
  // this UI, which is not an error the user can do anything about.
  const handleWorkstreamAction = async (
    workstreamId: string,
    actionId: string,
  ) => {
    if (actionId !== NOTE_ACTION_ID) return;
    const result = await openWorkstreamNote(project.id, workstreamId);
    if (result) setError(result);
  };

  const handleDismissStatus = async (workstreamId: string) => {
    const result = await dismissWorkstreamStatus(project.id, workstreamId);
    if (result) setError(result);
  };

  const handleOpenSession = async (workstreamId: string, sessionId: string) => {
    // Opening a session is the "seen" signal that clears its `done` overlay.
    attention.markSeen(sessionId);
    const result = await openSession(project.id, workstreamId, sessionId);
    if (result) setError(result);
  };

  /**
   * Opens the restart dialog on the session option the daemon restarts it as,
   * filled with the config it was started with, or the option's defaults.
   * Asked again while that loads, it waits for the first.
   */
  const handleRestartSession = async (
    workstreamId: string,
    sessionId: string,
  ) => {
    if (!sessionOf(workstreamId, sessionId) || restartLoad.current) return;
    const load = {};
    restartLoad.current = load;
    setLoadingRestart(true);
    try {
      const launched = await fetchLaunchConfig(
        project.id,
        workstreamId,
        sessionId,
      );
      if (restartLoad.current !== load) return;
      setRestarting({
        workstreamId,
        sessionId,
        schema: launched.schema,
        config: launched.config ?? undefined,
        continueRefusal: launched.continueRefusal,
      });
    } catch (cause) {
      if (restartLoad.current !== load) return;
      setError(
        cause instanceof Error && cause.message
          ? cause.message
          : 'Could not load the session settings.',
      );
    } finally {
      if (restartLoad.current === load) {
        restartLoad.current = null;
        setLoadingRestart(false);
      }
    }
  };

  /**
   * Restarts a session running outside HomeShell in HomeShell: a bare restart,
   * so the daemon continues its conversation with what it recorded. A refusal
   * shows as the page's error.
   */
  const handleTakeOverSession = async (
    workstreamId: string,
    sessionId: string,
  ) => {
    const result = await restartSession(
      project.id,
      workstreamId,
      sessionId,
      {},
    );
    const refusal = refusalMessage(result);
    if (typeof refusal === 'string') setError(refusal);
  };

  const handleRestartSubmit = async (
    data: RestartSessionData,
  ): Promise<string | Refusal | null> => {
    if (!restarting) return null;
    const result = await restartSession(
      project.id,
      restarting.workstreamId,
      restarting.sessionId,
      data,
    );
    if (isRefusal(result)) return result;
    setRestarting(null);
    return null;
  };

  const runSessionAction = async (promise: Promise<string | null>) => {
    const result = await promise;
    if (result) setError(result);
  };

  useEffect(() => {
    if (editing) {
      inputRef.current?.select();
    }
  }, [editing]);

  const commitRename = async () => {
    // Refused here rather than by disabling the box: disabling a focused input
    // blurs it, and the blur is what commits, so losing the daemon mid-edit would
    // fire the very request this exists to stop — and then leave a dead field
    // holding unsaved text, with no Cancel (it is not rendered while editing) and
    // no Escape (a blurred input sees no keys). The box stays editable and says
    // why instead.
    if (blocked) {
      setRenameError(disabledReason ?? null);
      setReturnRenameFocus(false);
      return;
    }
    const trimmed = editName.trim();
    if (!trimmed || trimmed === project.name) {
      setEditing(false);
      setEditName(project.name);
      return;
    }
    const result = await renameProject(project.id, trimmed);
    if (result) {
      setRenameError(result);
      // The box stays open on what was typed, so focus stays in it.
      setReturnRenameFocus(false);
    } else {
      setEditing(false);
    }
  };

  const handleRenameKeyDown = (e: KeyboardEvent) => {
    if (e.key === 'Enter') {
      e.preventDefault();
      setReturnRenameFocus(true);
      commitRename();
    } else if (e.key === 'Escape') {
      setReturnRenameFocus(true);
      setEditing(false);
      setEditName(project.name);
      setRenameError(null);
    }
  };

  const startRename = () => {
    setEditName(project.name);
    setRenameError(null);
    setEditing(true);
  };

  const remove = async () => {
    setConfirming(false);
    const result = await removeProject(project.id);
    if (result) setError(result);
    else onRemoved?.(project.id);
  };

  const handleRemove = () =>
    settings.projects.confirmRemove ? setConfirming(true) : remove();

  const newSessionPicker = useProjectCommands({
    project,
    blocked,
    renaming: editing,
    onNewWorkstream: create.show,
    onRename: startRename,
    onRemove: handleRemove,
    selectedId: list.selectedId,
    cards: cards.current,
  });

  // The project has no F2: with nothing selected, there is nothing for it to
  // be pressed on.
  const inPlaceHeadingId = useId();
  const separateHeadingId = useId();
  const nameProps = useRenamableTitle<HTMLHeadingElement>({
    name: project.name,
    onRename: startRename,
    disabledReason,
    className: styles.name,
  });

  const renderCard = (workstream: Workstream) => (
    <WorkstreamCard
      key={workstream.id}
      ref={(handle) => {
        if (handle) cards.current.set(workstream.id, handle);
        else cards.current.delete(workstream.id);
      }}
      workstream={workstream}
      projectPath={project.path}
      previewLines={settings.sessions.previewLines}
      displayStateFor={attention.displayState}
      onSeenSession={attention.markSeen}
      onOpenSession={(sessionId) => handleOpenSession(workstream.id, sessionId)}
      // Gated on the session's own provider: a workstream's provider
      // can be "core", which is absent from GET /api/providers.
      canSessionStartStop={(session) =>
        Boolean(caps[session.provider ?? '']?.sessionStartStop)
      }
      onRestartSession={(sessionId) =>
        handleRestartSession(workstream.id, sessionId)
      }
      onTakeOverSession={(sessionId) =>
        handleTakeOverSession(workstream.id, sessionId)
      }
      canPromptSession={(session) =>
        Boolean(caps[session.provider ?? '']?.oneOffPrompt)
      }
      onStopSession={(sessionId) =>
        runSessionAction(
          sessionAction(project.id, workstream.id, sessionId, '/stop', 'POST'),
        )
      }
      onStartSession={(sessionId) =>
        runSessionAction(
          sessionAction(project.id, workstream.id, sessionId, '/start', 'POST'),
        )
      }
      onRemoveSession={(sessionId) =>
        runSessionAction(
          sessionAction(project.id, workstream.id, sessionId, '', 'DELETE'),
        )
      }
      // Returned, not routed through runSessionAction: a refused
      // rename is about one row and one box, so the row shows it
      // beside itself instead of the view's page-level error line.
      onRenameSession={(sessionId, title) =>
        renameSession(project.id, workstream.id, sessionId, title)
      }
      onPromptSession={(sessionId, text) =>
        runSessionAction(
          promptSession(project.id, workstream.id, sessionId, text),
        )
      }
      onSendKeySession={(sessionId, key) =>
        runSessionAction(
          sendSessionKey(project.id, workstream.id, sessionId, key),
        )
      }
      loadSessionOptions={() => fetchSessionSchemas(project.id, workstream.id)}
      onCreateSession={(data) => handleCreateSession(workstream.id, data)}
      onDelete={(opts) => handleDeleteWorkstream(workstream.id, opts)}
      onRename={(name) => handleRenameWorkstream(workstream.id, name)}
      onOpen={() => handleOpenWorkstream(workstream.id)}
      onOpenWith={(mode) => handleOpenWorkstream(workstream.id, mode)}
      onAction={(actionId) => handleWorkstreamAction(workstream.id, actionId)}
      onDismissStatus={() => handleDismissStatus(workstream.id)}
      skipConfirmation={!settings.workstreams.confirmRemove}
      disabledReason={disabledReason}
      tabStopId={list.tabStopId}
      selectedId={list.selectedId}
    />
  );

  return (
    <div className={`${styles.view} ${loadingRestart ? styles.loading : ''}`}>
      <header className={styles.header} {...mnemonicScope('page')}>
        <div className={styles.heading}>
          <div className={styles.titleRow}>
            {editing ? (
              <input
                ref={inputRef}
                className={`${renameInputClass} ${styles.nameInput}`}
                type="text"
                value={editName}
                onChange={(e) => {
                  setEditName(e.target.value);
                  setRenameError(null);
                }}
                onBlur={commitRename}
                onKeyDown={handleRenameKeyDown}
                aria-label="Project name"
              />
            ) : (
              <h1 {...nameProps}>{project.name}</h1>
            )}
            {project.gitBacked && <Pill tone="accent">git</Pill>}
          </div>
          <CopyablePath path={project.path} className={styles.path} />
          {renameError && (
            <p className={styles.error} role="alert">
              {renameError}
            </p>
          )}
          {error && (
            <p className={styles.error} role="alert">
              {error}
            </p>
          )}
          <p
            className={styles.status}
            role="status"
            {...(loadingRestart && mnemonicScope('modal'))}
          >
            {loadingRestart && 'Loading settings…'}
          </p>
        </div>
        {/* No letter: D is an item's Delete, and once the last item is
            gone it would delete the project instead. */}
        <Button
          icon={<TrashIcon size={16} />}
          onClick={handleRemove}
          disabled={blocked}
          title={disabledReason ?? 'Stop tracking this project'}
          aria-label={`Delete project ${project.name}`}
        />
      </header>
      <div
        className={styles.workstreams}
        role="group"
        aria-label="Workstreams"
        aria-describedby={list.describedBy}
        {...list.listProps}
      >
        <span id={list.describedBy} hidden>
          {LIST_KEYS_HINT}
        </span>
        <section className={styles.section} aria-labelledby={inPlaceHeadingId}>
          <h2 id={inPlaceHeadingId} className={styles.sectionTitle}>
            <FolderOpenIcon size={14} />
            In the project folder
          </h2>
          {inPlace.length > 0 ? (
            inPlace.map(renderCard)
          ) : (
            <div className={styles.emptySection}>
              <p className={styles.emptyText}>
                No workstream in the project folder.
              </p>
              <Button
                ref={workHereRef}
                onClick={handleWorkHere}
                disabled={blocked}
                // Not disabled: that would drop focus to the page meanwhile.
                aria-disabled={creating !== null || undefined}
                aria-busy={creating === WORK_HERE || undefined}
                title={
                  disabledReason ?? 'Create a workstream in the project folder'
                }
              >
                Work here
              </Button>
            </div>
          )}
        </section>
        <section className={styles.section} aria-labelledby={separateHeadingId}>
          <div className={styles.sectionHeader} {...mnemonicScope('page')}>
            <h2 id={separateHeadingId} className={styles.sectionTitle}>
              <FolderTreeIcon size={14} />
              Separate folders{' '}
              {separate.length > 0 && (
                <span className={styles.count}>· {separate.length}</span>
              )}
            </h2>
            {separate.length === 0 &&
              project.untrackedWorktrees.length === 0 && (
                <p className={styles.emptyText}>No separate folders yet.</p>
              )}
            <Button
              ref={newWorkstreamRef}
              className={styles.newWorkstream}
              icon={<PlusIcon size={14} />}
              onClick={create.show}
              disabled={blocked}
              mnemonic={MNEMONICS.newWorkstream}
              title={disabledReason ?? 'Create a workstream in this project'}
            >
              New workstream
            </Button>
          </div>
          {create.open && creatable !== null && (
            <NewWorkstreamForm
              ref={create.formRef}
              providers={creatable}
              onSubmit={handleCreate}
              onCancel={create.cancel}
              defaultProviderId={
                resolveDefault(settings.workstreams.defaultProvider) ||
                undefined
              }
              defaultConfig={workstreamConfigDefaults(settings.workstreams)}
              defaultName={expandNameTemplate(
                settings.workstreams.nameTemplate,
                project,
              )}
              resolveDraft={resolveDraft}
              disabledReason={disabledReason}
            />
          )}
          {separate.map(renderCard)}
          <UntrackedWorktrees
            worktrees={project.untrackedWorktrees}
            tabStopId={list.tabStopId}
            onAdd={handleAddWorktree}
            adding={
              creating !== WORK_HERE ? (creating ?? undefined) : undefined
            }
            disabledReason={
              disabledReason ??
              (creating === WORK_HERE
                ? 'Creating a workstream in the project folder'
                : undefined)
            }
          />
        </section>
      </div>
      {newSessionPicker}
      {confirming && (
        <ConfirmDialog
          title={`Delete "${project.name}"?`}
          onClose={() => setConfirming(false)}
          actions={
            <>
              {/* Backing out is local — it stays available while offline. */}
              <Button
                onClick={() => setConfirming(false)}
                mnemonic={MNEMONICS.cancel}
                data-initial-focus
              >
                Cancel
              </Button>
              <Button
                variant="danger"
                onClick={remove}
                aria-disabled={blocked}
                mnemonic={MNEMONICS.delete}
                title={disabledReason}
              >
                Delete
              </Button>
            </>
          }
        >
          <p>
            HomeShell stops tracking the project. Its folder is left as it is.
          </p>
        </ConfirmDialog>
      )}
      {restarting && (
        <RestartSessionDialog
          sessionTitle={restartingSession?.title ?? ''}
          schema={restarting.schema}
          config={restarting.config}
          continueRefusal={restarting.continueRefusal}
          takeOverWarning={restartingSession?.capabilities?.takeOverWarning}
          onSubmit={handleRestartSubmit}
          onCancel={() => setRestarting(null)}
          disabledReason={disabledReason}
          fallbackFocus={() => tabStopItem(document)}
        />
      )}
      {busyRemoval && (
        <BusyWorkstreamDialog
          workstreamName={
            project.workstreams.find((w) => w.id === busyRemoval.workstreamId)
              ?.name ?? 'this workstream'
          }
          sessions={busyRemoval.sessions}
          onConfirm={handleForceDeleteWorkstream}
          onCancel={() => setBusyRemoval(null)}
        />
      )}
    </div>
  );
}

export default ProjectView;
