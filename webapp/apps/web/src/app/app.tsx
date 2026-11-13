import {
  AddProjectForm,
  Button,
  EmptyState,
  MNEMONICS,
  mnemonicScope,
  NotificationToasts,
  ProjectList,
  useCreateForm,
  useMnemonics,
} from '@homeshell/components';
import type { Project } from '@homeshell/model';
import { useEffect, useMemo, useRef, useState } from 'react';
import styles from './app.module.css';
import { DaemonStatus } from './daemon-status';
import { CommandPalette } from './keyboard/command-palette';
import { type Command, useCommands } from './keyboard/commands';
import { labelOf } from './keyboard/keybindings';
import { ShortcutsDialog } from './keyboard/shortcuts-dialog';
import { usePendingFocus } from './keyboard/use-pending-focus';
import { LogPane } from './logs/logs-view';
import { offlineReason } from './offline-reason';
import { DEFAULT_MAX_RECORDS, MIN_RECORDS, useLogStream } from './logs/use-log-stream';
import { ProjectView, type ProjectViewHandle } from './project-view';
import { SettingsButton } from './settings-button';
import { SettingsView } from './settings/settings-view';
import { ThemeToggle } from './theme-toggle';
import { useSettings } from './settings/use-settings';
import { useDaemonConfig } from './use-daemon-config';
import { useDaemonState } from './use-daemon-state';
import { useNotifications } from './use-notifications';
import { useProviders } from './use-providers';
import { toggledTheme, useTheme } from './use-theme';
import { useHashRoute } from './router/use-hash-route';

/**
 * UI sketch of the HomeShell control plane: projects on the left, the
 * selected project's workstreams (with their sessions) in the middle. All
 * data — including every derived state — arrives from the daemon over its
 * SSE stream; mutating actions are intentionally non-functional.
 */
async function addProject(data: {
  path: string;
  name: string;
}): Promise<Project | string> {
  try {
    const res = await fetch('/api/projects', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(data),
    });
    if (!res.ok) {
      const body = await res.json().catch(() => null);
      return (body as { error?: string } | null)?.error ?? 'Request failed.';
    }
    return (await res.json()) as Project;
  } catch {
    return 'Could not reach the daemon.';
  }
}

// hs-last-project holds the last-selected project id. It is a per-browser UI
// preference (which project this browser last had open), so it lives in
// localStorage, not in the daemon-shared settings.
const LAST_PROJECT_KEY = 'hs-last-project';

// hs-log-buffer is the log ring-buffer size. Like the theme, it is a per-device
// UI/memory preference (how much this browser retains), so it is browser-local
// rather than daemon-shared. Minimum MIN_RECORDS, no maximum.
const LOG_BUFFER_KEY = 'hs-log-buffer';

function loadLogBufferSize(): number {
  const stored = Number(localStorage.getItem(LOG_BUFFER_KEY));
  return Number.isFinite(stored) && stored >= MIN_RECORDS
    ? Math.round(stored)
    : DEFAULT_MAX_RECORDS;
}

export function App() {
  const notifications = useNotifications();
  const { projects, connection } = useDaemonState(notifications.present);
  const { settings, loaded } = useSettings();
  const { route, category, navigate } = useHashRoute();
  const providers = useProviders();
  const { config, setLogLevel } = useDaemonConfig();
  // The log stream lives here, not inside the pane, so its ring buffer keeps
  // accumulating (and is never flushed) while the pane is closed. `logBufferSize`
  // holds the raw edited value (so the NumberField shows what is typed and clamps
  // only on blur); the buffer itself is capped by the clamped `effectiveBufferSize`
  // so a transient mid-type value (e.g. 2 while typing 200) can't shrink it.
  const [logBufferSize, setLogBufferSize] = useState(loadLogBufferSize);
  const effectiveBufferSize = Math.max(
    MIN_RECORDS,
    Math.round(logBufferSize) || DEFAULT_MAX_RECORDS
  );
  const logStream = useLogStream(effectiveBufferSize);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [showLogs, setShowLogs] = useState(false);
  // The palette's query while it is open. `seq` is bumped on every open, so
  // opening it again remounts it, even from itself with the same query.
  const [palette, setPalette] = useState<{
    query: string | null;
    seq: number;
  }>({ query: null, seq: 0 });
  const [showShortcuts, setShowShortcuts] = useState(false);
  const { theme, setTheme } = useTheme();
  const viewRef = useRef<ProjectViewHandle>(null);
  const sidebarRef = useRef<HTMLElement>(null);
  const addButtonRef = useRef<HTMLButtonElement>(null);
  const addForm = useCreateForm(() => addButtonRef.current);
  useMnemonics();

  // One answer to "can we act?", shared by every view: while the daemon is not
  // connected, everything that would call it is disabled with this as the reason.
  const disabledReason = offlineReason(connection);

  const changeLogBufferSize = (size: number) => {
    setLogBufferSize(size);
    localStorage.setItem(LOG_BUFFER_KEY, String(size));
  };

  // Restore the last-selected project once settings have loaded and only when
  // the preference is enabled. Settings load asynchronously from the daemon, so
  // this cannot be an initializer.
  useEffect(() => {
    if (!loaded || !settings.projects.rememberLastSelected) return;
    const remembered = localStorage.getItem(LAST_PROJECT_KEY);
    if (remembered) setSelectedId(remembered);
  }, [loaded, settings.projects.rememberLastSelected]);

  const sortedProjects = useMemo(() => {
    if (!projects) return projects;
    if (settings.projects.sidebarSort === 'name') {
      return [...projects].sort((a, b) => a.name.localeCompare(b.name));
    }
    return projects;
  }, [projects, settings.projects.sidebarSort]);

  const selected =
    projects?.find((p) => p.id === selectedId) ?? sortedProjects?.[0] ?? null;

  const handleSelect = (id: string) => {
    setSelectedId(id);
    if (settings.projects.rememberLastSelected) {
      localStorage.setItem(LAST_PROJECT_KEY, id);
    }
  };

  // Something in the project view to focus once the render showing it has
  // committed (see ProjectViewHandle).
  const focusInView = usePendingFocus((target: { itemId?: string }) => {
    if (!viewRef.current) return false;
    viewRef.current.focus(target.itemId);
    return true;
  });

  // The selected project's sidebar entry, focused once a project just added
  // is listed and selected, or one just removed has left the list: Add
  // project, with none left.
  const focusInSidebar = usePendingFocus(
    (target: { added: string } | { removed: string }) => {
      const settled =
        'added' in target
          ? selected?.id === target.added
          : !projects?.some((project) => project.id === target.removed);
      if (!settled) return false;
      (
        sidebarRef.current?.querySelector<HTMLElement>(
          '[aria-current="true"]',
        ) ?? addButtonRef.current
      )?.focus();
      return true;
    },
  );

  const firstWorkstreamId = (projectId: string) =>
    projects?.find((project) => project.id === projectId)?.workstreams[0]?.id;

  const goTo = (projectId: string, itemId?: string) => {
    navigate('home');
    handleSelect(projectId);
    focusInView({ itemId: itemId ?? firstWorkstreamId(projectId) });
  };

  const openPalette = (query: string) =>
    setPalette(({ seq }) => ({ query, seq: seq + 1 }));
  const closePalette = () => setPalette(({ seq }) => ({ query: null, seq }));

  const commands: Command[] = [
    {
      id: 'showCommands',
      label: labelOf('showCommands'),
      run: () => openPalette('>'),
    },
    {
      id: 'quickOpen',
      // It asks where, so it says so.
      label: `${labelOf('quickOpen')}…`,
      run: () => openPalette(''),
    },
    {
      id: 'showShortcuts',
      label: labelOf('showShortcuts'),
      run: () => setShowShortcuts(true),
    },
    {
      id: 'openSettings',
      label: labelOf('openSettings'),
      run: () => navigate('settings'),
    },
    {
      id: 'toggleLogs',
      label: 'Toggle logs',
      run: () => setShowLogs((open) => !open),
    },
    {
      id: 'toggleTheme',
      label: 'Toggle light/dark theme',
      run: () => setTheme(toggledTheme(theme)),
    },
    ...(disabledReason
      ? []
      : [
          {
            id: 'addProject',
            label: 'Add project',
            run: () => {
              navigate('home');
              addForm.show();
            },
          },
        ]),
  ];
  useCommands(commands);

  const handleAddProject = async (data: { path: string; name: string }) => {
    const result = await addProject(data);
    if (typeof result === 'string') return result;
    addForm.close();
    handleSelect(result.id);
    focusInSidebar({ added: result.id });
    return null;
  };

  return (
    <div className={styles.shell}>
      <header className={styles.topBar}>
        <span className={styles.brand}>HomeShell</span>
        <span className={styles.topBarRight}>
          <DaemonStatus connection={connection} />
          <ThemeToggle />
          <SettingsButton
            onClick={() => navigate(route === 'settings' ? 'home' : 'settings')}
          />
        </span>
      </header>
      <div className={styles.content}>
        {route === 'settings' ? (
          <main className={styles.main}>
            <SettingsView
              onClose={() => {
                navigate('home');
                focusInView({});
              }}
              category={category}
              onSelectCategory={(id) => navigate('settings', id)}
              providers={providers}
              logLevel={config?.logLevel}
              onLogLevelChange={setLogLevel}
              logBufferSize={logBufferSize}
              onLogBufferSizeChange={changeLogBufferSize}
              disabledReason={disabledReason}
            />
          </main>
        ) : (
          <div className={styles.body}>
            <aside className={styles.sidebar}>
              <div className={styles.sidebarHeader} {...mnemonicScope('page')}>
                <h2 className={styles.sidebarTitle}>Projects</h2>
                <Button
                  ref={addButtonRef}
                  onClick={addForm.show}
                  disabled={Boolean(disabledReason)}
                  mnemonic={MNEMONICS.addProject}
                  title={disabledReason ?? 'Add a folder as a project'}
                >
                  Add project
                </Button>
              </div>
              {addForm.open && (
                <AddProjectForm
                  ref={addForm.formRef}
                  onSubmit={handleAddProject}
                  onCancel={addForm.cancel}
                  disabledReason={disabledReason}
                />
              )}
              <nav ref={sidebarRef} aria-label="Projects">
                {sortedProjects ? (
                  <ProjectList
                    projects={sortedProjects}
                    selectedId={selected?.id ?? null}
                    onSelect={handleSelect}
                  />
                ) : (
                  <p className={styles.loading}>Waiting for the daemon…</p>
                )}
              </nav>
            </aside>
            <main className={styles.main}>
              {selected ? (
                <ProjectView
                  ref={viewRef}
                  project={selected}
                  disabledReason={disabledReason}
                  onRemoved={(projectId) =>
                    focusInSidebar({ removed: projectId })
                  }
                />
              ) : projects ? (
                <EmptyState
                  title="No projects yet"
                  hint="Add a project to get started."
                />
              ) : (
                <EmptyState
                  title="Connecting to the daemon…"
                  hint="Projects appear as soon as the daemon answers."
                />
              )}
            </main>
          </div>
        )}
        {showLogs && (
          <LogPane
            onClose={() => setShowLogs(false)}
            logLevel={config?.logLevel}
            onLogLevelChange={setLogLevel}
            records={logStream.records}
            connection={logStream.connection}
            disabledReason={disabledReason}
            overflowed={logStream.overflowed}
            bufferSize={effectiveBufferSize}
          />
        )}
      </div>
      {showShortcuts && (
        <ShortcutsDialog onClose={() => setShowShortcuts(false)} />
      )}
      {palette.query !== null && (
        <CommandPalette
          key={palette.seq}
          initialQuery={palette.query}
          projects={projects ?? []}
          onGoTo={goTo}
          onClose={closePalette}
        />
      )}
      <NotificationToasts
        items={notifications.toasts}
        onDismiss={notifications.dismiss}
      />
    </div>
  );
}

export default App;
