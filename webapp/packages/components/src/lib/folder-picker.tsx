import {
  type KeyboardEvent,
  useCallback,
  useEffect,
  useRef,
  useState,
} from 'react';
import { Button } from './button';
import styles from './folder-picker.module.css';
import { useModalFocus } from './focus-return';
import { handleModalKeys } from './focus-trap';
import { Kbd } from './kbd';
import {
  formatKeys,
  isMac,
  isModified,
  type KeyHelp,
  moveFocus,
  platformKeys,
} from './keys';
import { titleWhenTruncated } from './truncated-title';

export interface BrowseEntry {
  name: string;
  path: string;
  isDir: boolean;
}

export interface BrowseResponse {
  path: string;
  parent: string;
  entries: BrowseEntry[];
}

export interface FolderPickerProps {
  /** The folder to open on; home when unset or when the daemon refuses it. */
  initialPath?: string;
  onSelect: (path: string) => void;
  onCancel: () => void;
  fetchBrowse?: (
    path?: string,
    showHidden?: boolean,
  ) => Promise<BrowseResponse>;
}

async function defaultFetchBrowse(
  path?: string,
  showHidden?: boolean,
): Promise<BrowseResponse> {
  const params = new URLSearchParams();
  if (path) params.set('path', path);
  if (showHidden) params.set('showHidden', 'true');
  const query = params.toString();
  const url = query ? `/api/browse?${query}` : '/api/browse';
  const response = await fetch(url);
  if (!response.ok) {
    const body = await response.json().catch(() => null);
    throw new Error(
      (body as { error?: string } | null)?.error ?? 'Failed to browse',
    );
  }
  return response.json();
}

/**
 * The picker's keys beyond moving between entries, each with the word its
 * hint line gives it.
 */
export const FOLDER_PICKER_KEYS: readonly (KeyHelp & { hint: string })[] = [
  {
    keys: ['enter', 'arrowright'],
    label: 'Open the focused folder',
    hint: 'open',
  },
  {
    keys: ['backspace', 'alt+arrowup'],
    label: 'Go to the parent folder',
    hint: 'up',
  },
  { keys: ['mod+enter'], label: 'Select the folder shown', hint: 'select' },
];

/**
 * A modal for picking a folder on the daemon's host. Each folder opened puts
 * focus on its first entry; Up/Down and Home/End move between entries, Enter
 * or Right opens one, Backspace or Alt+Up goes to the parent, and Ctrl/Cmd+Enter
 * selects the folder shown.
 */
export function FolderPicker({
  initialPath,
  onSelect,
  onCancel,
  fetchBrowse = defaultFetchBrowse,
}: FolderPickerProps) {
  const [browseData, setBrowseData] = useState<BrowseResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [showHidden, setShowHidden] = useState(false);
  // The entry holding the list's one tab stop.
  const [activeIndex, setActiveIndex] = useState(0);
  // Whether the listing being loaded takes focus: one opened by moving through
  // folders does, one re-listed by Show hidden leaves focus on the checkbox.
  const takeFocusRef = useRef(true);
  const listRef = useRef<HTMLUListElement>(null);

  const requestIdRef = useRef(0);
  const showHiddenRef = useRef(showHidden);
  showHiddenRef.current = showHidden;

  const navigate = useCallback(
    async (path?: string, hidden?: boolean, orHome = false) => {
      const id = ++requestIdRef.current;
      setLoading(true);
      setError(null);
      const showing = hidden ?? showHiddenRef.current;
      try {
        const data = await fetchBrowse(path, showing).catch((err) => {
          if (orHome && path) return fetchBrowse(undefined, showing);
          throw err;
        });
        if (id !== requestIdRef.current) return;
        setBrowseData(data);
        setActiveIndex(0);
      } catch (err) {
        if (id !== requestIdRef.current) return;
        setError(err instanceof Error ? err.message : 'Failed to browse');
      } finally {
        if (id === requestIdRef.current) setLoading(false);
      }
    },
    [fetchBrowse],
  );

  useEffect(() => {
    navigate(initialPath || undefined, undefined, true);
  }, [navigate, initialPath]);

  // The list holds focus while it loads: the entry that had it goes with the
  // old listing.
  const open = (path: string) => {
    takeFocusRef.current = true;
    listRef.current?.focus();
    navigate(path);
  };

  useEffect(() => {
    if (loading || !takeFocusRef.current) return;
    takeFocusRef.current = false;
    const list = listRef.current;
    (list?.querySelector<HTMLElement>('[tabindex="0"]') ?? list)?.focus();
  }, [loading]);

  const overlayRef = useRef<HTMLDivElement>(null);
  useModalFocus(() => overlayRef.current);

  const handleKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    if (handleModalKeys(event, onCancel)) return;
    if (
      event.key === 'Enter' &&
      (isMac() ? event.metaKey : event.ctrlKey) &&
      browseData
    ) {
      event.preventDefault();
      onSelect(browseData.path);
    } else if (
      (event.key === 'Backspace' ||
        (event.key === 'ArrowUp' && event.altKey)) &&
      browseData?.parent
    ) {
      event.preventDefault();
      open(browseData.parent);
    }
  };

  const handleListKeyDown = (event: KeyboardEvent<HTMLUListElement>) => {
    if (!browseData || isModified(event)) return;
    const entries = Array.from(
      event.currentTarget.querySelectorAll<HTMLElement>('[role="button"]'),
    );
    const index = entries.indexOf(event.target as HTMLElement);
    if (index === -1) return;
    if (['Enter', ' ', 'ArrowRight'].includes(event.key)) {
      event.preventDefault();
      open(browseData.entries[index].path);
      return;
    }
    const next = moveFocus(event, entries);
    if (next !== undefined) setActiveIndex(next);
  };

  return (
    <div
      ref={overlayRef}
      className={styles.overlay}
      onKeyDown={handleKeyDown}
      onClick={(event) => {
        if (event.target === event.currentTarget) onCancel();
      }}
      role="dialog"
      aria-modal="true"
      aria-label="Select folder"
      tabIndex={-1}
    >
      <div className={styles.dialog}>
        <div className={styles.header}>
          <h3 className={styles.title}>Select folder</h3>
        </div>
        {browseData && (
          <div className={styles.pathBar}>
            {browseData.parent && (
              <button
                className={styles.upButton}
                type="button"
                onClick={() => open(browseData.parent)}
                aria-label="Go to parent folder"
              >
                ↑
              </button>
            )}
            <span className={styles.currentPath} data-testid="current-path">
              {browseData.path}
            </span>
          </div>
        )}
        <label className={styles.showHidden}>
          <input
            type="checkbox"
            checked={showHidden}
            onChange={(event) => {
              const checked = event.target.checked;
              setShowHidden(checked);
              navigate(browseData?.path, checked);
            }}
          />
          Show hidden
        </label>
        <ul
          ref={listRef}
          className={styles.list}
          aria-label="Folders"
          tabIndex={-1}
          onKeyDown={handleListKeyDown}
        >
          {loading && <li className={styles.loading}>Loading…</li>}
          {error && <li className={styles.error}>{error}</li>}
          {!loading && !error && browseData && (
            <>
              {browseData.entries.length === 0 && (
                <li className={styles.empty}>No subdirectories</li>
              )}
              {browseData.entries.map((entry, index) => (
                <li
                  key={entry.path}
                  className={styles.entry}
                  role="button"
                  tabIndex={index === activeIndex ? 0 : -1}
                  onClick={() => open(entry.path)}
                >
                  <span className={styles.folderIcon}>📁</span>
                  <span
                    className={styles.entryName}
                    {...titleWhenTruncated(entry.name)}
                  >
                    {entry.name}
                  </span>
                </li>
              ))}
            </>
          )}
        </ul>
        <div className={styles.footer}>
          <p className={styles.hint}>
            {FOLDER_PICKER_KEYS.map((key) => (
              <span key={key.hint}>
                <Kbd>{formatKeys(platformKeys(key)[0])}</Kbd> {key.hint}
              </span>
            ))}
          </p>
          <Button type="button" onClick={onCancel}>
            Cancel
          </Button>
          <Button
            type="button"
            variant="primary"
            disabled={!browseData}
            onClick={() => browseData && onSelect(browseData.path)}
          >
            Select
          </Button>
        </div>
      </div>
    </div>
  );
}

export default FolderPicker;
