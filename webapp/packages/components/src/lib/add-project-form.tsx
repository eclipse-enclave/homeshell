import {
  type FormEvent,
  type Ref,
  useEffect,
  useId,
  useRef,
  useState,
} from 'react';
import { Button } from './button';
import { MNEMONICS } from './mnemonic';
import { type FormHandle, cancelOnEscape, useFormFocus } from './create-form';
import { FolderPicker } from './folder-picker';
import {
  type NativeFolderResult,
  pickFolderNatively,
} from './native-folder-picker';
import styles from './add-project-form.module.css';

export interface AddProjectFormProps {
  onSubmit: (data: { path: string; name: string }) => Promise<string | null>;
  /** Called by Cancel and by Escape. */
  onCancel: () => void;
  /**
   * When set, the whole form is inert with this text as the reason: submitting,
   * Browse (which lists the daemon's filesystem) and the two text boxes, since a
   * path typed here has nowhere to go until the daemon is back. What is already
   * typed is kept, and focus stays where it was. Cancel stays live — backing out
   * needs nothing.
   */
  disabledReason?: string;
  /**
   * Opens the host desktop's own folder chooser. Injectable so tests can drive
   * every outcome without a desktop; production uses the daemon-backed one.
   */
  pickNatively?: (
    startPath?: string,
    signal?: AbortSignal,
  ) => Promise<NativeFolderResult>;
  /** Focuses the form again, e.g. when what opened it is asked for once more. */
  ref?: Ref<FormHandle>;
}

/**
 * Inline form for adding a project from a local directory. The path is
 * required; the name is optional and defaults to the directory name on the
 * daemon side. onSubmit returns null on success or an error message. It opens
 * on the path.
 */
export function AddProjectForm({
  onSubmit,
  onCancel,
  disabledReason,
  pickNatively = pickFolderNatively,
  ref,
}: AddProjectFormProps) {
  const [path, setPath] = useState('');
  const [name, setName] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [showPicker, setShowPicker] = useState(false);
  const [browsing, setBrowsing] = useState(false);
  const blocked = Boolean(disabledReason);
  const inert = submitting || blocked;
  const pathRef = useRef<HTMLInputElement>(null);
  const pathId = useId();

  useFormFocus(ref, () => pathRef.current?.focus());

  // The in-flight Browse, so unmounting can close the dialog it opened.
  const browseRef = useRef<AbortController | null>(null);
  useEffect(() => () => browseRef.current?.abort(), []);

  /**
   * Browse asks for the desktop's own folder chooser first, and only falls back
   * to the in-app picker when there isn't one — a host with no dialog tool
   * installed, or a daemon reached over the network, where the dialog would open
   * on a screen the user cannot see. Cancelling the native dialog cancels
   * browsing outright rather than dropping into the in-app picker, which would
   * read as the app ignoring the dismissal.
   *
   * The request is abortable so leaving the form takes the dialog with it: the
   * daemon runs the chooser on the request's context, so dropping the request
   * closes the window. Otherwise Cancel would unmount the form and leave a
   * chooser sitting on the host's screen with nothing listening to it.
   */
  const handleBrowse = async () => {
    const controller = new AbortController();
    browseRef.current = controller;
    setBrowsing(true);
    let result: NativeFolderResult;
    try {
      result = await pickNatively(path.trim(), controller.signal);
    } finally {
      if (browseRef.current === controller) browseRef.current = null;
      setBrowsing(false);
    }
    if (controller.signal.aborted) return;
    if (result.outcome === 'selected') {
      setPath(result.path);
      setError(null);
      pathRef.current?.focus();
    } else if (result.outcome === 'unavailable') {
      setShowPicker(true);
    }
  };

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();
    if (inert) return;
    const trimmed = path.trim();
    if (!trimmed) {
      setError('Path is required.');
      return;
    }
    setError(null);
    setSubmitting(true);
    const result = await onSubmit({ path: trimmed, name: name.trim() });
    setSubmitting(false);
    if (result) {
      setError(result);
    }
  };

  return (
    // Named for the same reason as the new-workstream form: its refusal has to
    // be findable as *this* form's, on a page that has other alerts (a
    // notification toast among them).
    <form
      className={styles.form}
      onSubmit={handleSubmit}
      onKeyDown={cancelOnEscape(onCancel, submitting)}
      aria-label="Add project"
    >
      <div className={styles.label}>
        <label htmlFor={pathId}>Path</label>
        <span className={styles.pathRow}>
          <input
            id={pathId}
            ref={pathRef}
            className={styles.input}
            type="text"
            value={path}
            onChange={(e) => {
              setPath(e.target.value);
              setError(null);
            }}
            placeholder="/path/to/project"
            readOnly={inert}
            aria-disabled={inert}
            title={disabledReason}
          />
          <Button
            type="button"
            onClick={handleBrowse}
            aria-disabled={inert || browsing}
            mnemonic={MNEMONICS.browse}
            title={disabledReason}
          >
            {/* The native dialog is modal on the desktop but not to this
                document, so the button is what shows one is already open. */}
            {browsing ? 'Browsing…' : 'Browse'}
          </Button>
        </span>
      </div>
      {showPicker && (
        <FolderPicker
          initialPath={path.trim()}
          onSelect={(selected) => {
            setPath(selected);
            setError(null);
            setShowPicker(false);
          }}
          onCancel={() => setShowPicker(false)}
        />
      )}
      <label className={styles.label}>
        Name
        <input
          className={styles.input}
          type="text"
          value={name}
          onChange={(e) => {
            setName(e.target.value);
            setError(null);
          }}
          placeholder="defaults to directory name"
          readOnly={inert}
          aria-disabled={inert}
          title={disabledReason}
        />
      </label>
      {error && (
        <p className={styles.error} role="alert">
          {error}
        </p>
      )}
      <div className={styles.actions}>
        <Button
          type="button"
          onClick={onCancel}
          disabled={submitting}
          mnemonic={MNEMONICS.cancel}
        >
          Cancel
        </Button>
        <Button
          type="submit"
          variant="primary"
          aria-disabled={inert}
          mnemonic={MNEMONICS.addProject}
          title={disabledReason}
        >
          Add
        </Button>
      </div>
    </form>
  );
}

export default AddProjectForm;
