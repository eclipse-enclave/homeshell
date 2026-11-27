import type { Session, SessionState } from '@homeshell/model';
import {
  acceptsImageNow,
  dotStateLabel,
  endedAgo,
  hasSessionRuntime,
  isSessionTerminal,
  sessionCapabilities,
  stateLabel,
} from '@homeshell/model';
import type {
  ClipboardEvent,
  DragEvent,
  FormEvent,
  HTMLAttributes,
  KeyboardEvent,
  MouseEvent as ReactMouseEvent,
  Ref,
} from 'react';
import {
  useCallback,
  useEffect,
  useImperativeHandle,
  useRef,
  useState,
} from 'react';
import { ActionConfirm } from './action-confirm';
import { Button } from './button';
import { useReturnFocus } from './focus-return';
import { PaperclipIcon, PlayIcon, SquareIcon, TerminalIcon } from './icons';
import { type ItemAction, runAction, useActionsMenu } from './item-actions';
import { isDeleteKey, isModified } from './keys';
import { controlTabIndexFor, listItemKeyShortcuts } from './list-navigation';
import {
  MNEMONICS,
  mnemonicProps,
  mnemonicScope,
  useMnemonicHandler,
} from './mnemonic';
import { renameInputClass, useRenamableTitle } from './renamable-title';
import controls from './controls.module.css';
import { cx } from './cx';
import a11y from './a11y.module.css';
import styles from './session-row.module.css';
import { StatusDot } from './status-dot';
import { titleWhenTruncated } from './truncated-title';
import { useKeyboardFocus } from './use-keyboard-focus';
import { useNow } from './use-now';

/** Whether a handler's return value is something to wait on. */
function isThenable(
  value: void | Promise<string | null | void>,
): value is Promise<string | null | void> {
  return typeof (value as Promise<unknown> | undefined)?.then === 'function';
}

/**
 * Runs `run` on a click, skipping a double-click's second: it must not toggle
 * back. Its first cannot be told from a single click
 * without delaying every one, so it acts at once. (`detail` is the click
 * count; `fireEvent.click` leaves it 0.)
 */
const onSingleClick =
  (run: () => void) => (event: ReactMouseEvent<HTMLElement>) => {
    if (event.detail <= 1) run();
  };

/**
 * " · 12 min ago" after an ended session's state, exact on hover. Its own
 * component, so only it renders as the clock moves on. Every one shares that
 * clock, started whenever the first of them was, so it ticks often enough for
 * "just now" to turn "1 min ago" on time.
 */
function EndedAgo({ endedAt }: { endedAt: string }) {
  const ago = endedAgo(endedAt, useNow(10_000));
  if (!ago) return null;
  return (
    <>
      {' · '}
      <time dateTime={endedAt} title={new Date(endedAt).toLocaleString()}>
        {ago}
      </time>
    </>
  );
}

/** How long the handle's expand waits for a send box to put focus in. */
export const ANSWER_FOCUS_MS = 10_000;

/** How long "Image attached" stays in the row before it clears. */
export const ATTACH_STATUS_MS = 5_000;

/** The image types a session takes (`POST {s}/image`); the file picker shows only these. */
const ATTACHABLE_TYPES = ['image/png', 'image/jpeg'];

/** What the row reports when some of the files it was given are not images it can attach. */
const ONLY_IMAGES = 'Only PNG or JPEG images can be attached';

/** Why the row takes no image while it is still sending an earlier batch. */
const STILL_ATTACHING = 'Still attaching the previous images';

/**
 * What the row tells a user whose paste listed files but handed none over.
 * WebKitGTK, the Linux desktop app's webview, does this with every image.
 */
const PASTE_UNREADABLE =
  "Pasting images doesn't work here: drop the image on the row or use Attach image";

/**
 * What a paste into the send box attaches: all its files when one is an image
 * and the clipboard holds no plain text, otherwise none, and the paste stays
 * ordinary text. Why: copying from a web page often puts an image beside the
 * text, and pasting that text must never upload something unexpectedly.
 * 'unreadable' when, with no text, it lists files but hands none over.
 */
function pastedImages(data: DataTransfer | null): File[] | 'unreadable' {
  if (!data || data.getData('text/plain') !== '') return [];
  let files = Array.from(data.files ?? []);
  // Some webviews list a pasted image only among the clipboard's items.
  if (files.length === 0) {
    files = Array.from(data.items ?? [])
      .filter((item) => item.kind === 'file')
      .map((item) => item.getAsFile())
      .filter((file): file is File => file !== null);
  }
  if (files.length === 0 && Array.from(data.types).includes('Files')) {
    return 'unreadable';
  }
  return files.some((file) => file.type.startsWith('image/')) ? files : [];
}

/** Whether a drag carries files, rather than text or a link. */
const carriesFiles = (event: DragEvent) =>
  Array.from(event.dataTransfer.types).includes('Files');

export interface SessionRowProps {
  session: Session;
  /**
   * Display state overriding the daemon-reported one (e.g. the UI-computed
   * `done` = idle & unseen). Defaults to session.state. Actions still key off
   * the raw session.state.
   */
  displayState?: SessionState;
  /** Opens the session (e.g. a local terminal). When omitted, Open is disabled. */
  onOpen?: () => void;
  /**
   * Asks for new settings to restart the session with (see
   * RestartSessionDialog). Offered in the row's actions menu, and so as a
   * command, when set: the card sets it only for sessions the daemon marks
   * restartable.
   */
  onRestartWithSettings?: () => void;
  /**
   * Stops a session with a runtime behind it; offered only when set. A session
   * still starting stops through onRemove instead.
   */
  onStop?: () => void;
  /**
   * Restarts, in HomeShell, a session running outside it, ending it where it
   * runs. Offered as Restart in HomeShell, after a confirmation, for a
   * restartable session whose capabilities allow a take-over; busy until the
   * returned promise settles.
   */
  onTakeOver?: () => void | Promise<void>;
  /**
   * Continues an ended session (stopped, or done by the daemon). Shown, as
   * Continue, only when set.
   */
  onStart?: () => void;
  /**
   * Removes the session. Offered in every state — the provider tears down the
   * runtime regardless of whether it is running, and the daemon handles its own
   * placeholder rows itself (removing a starting one cancels the build behind
   * it) — so removal is the user's choice, not a capability the daemon gates.
   * A confirmation dialog guards against accidental removal, except on a failed
   * row, which has no runtime to lose. Shown only when set.
   */
  onRemove?: () => void;
  /**
   * Renames the session. Called with the name the user typed, or with the empty
   * string when they cleared the box — which means "give me the default back":
   * a session's title otherwise belongs to its provider (the agent's name, or the
   * live one it reads off the terminal), so clearing the custom name is a real
   * operation and not a no-op.
   *
   * The gesture is a **double-click on the name** (or F2 with the row focused), not
   * a button: the name is the thing being changed, and the action cluster keeps one
   * fewer control. Offered only when set, and never for a starting placeholder,
   * which stands for a session that does not exist yet.
   *
   * Returning a message means the rename was **refused** (the daemon rejects a
   * title a sibling row already shows). The row then keeps the box open on what was
   * typed and warns beside itself, rather than the refusal surfacing somewhere else
   * on the page: the thing that went wrong is this row, and the value the user has
   * to change is in this box. Returning nothing is success.
   */
  onRename?: (title: string) => void | Promise<string | null | void>;
  /**
   * Marks the session as seen (clears the `done` attention overlay). The row's
   * info area becomes a "mark seen" button only while the session is actually
   * showing `done` — on any other state clicking it would be a no-op, so the
   * affordance is not rendered. Omitted when no attention tracking applies.
   */
  onSeen?: () => void;
  /**
   * Sends a line of text to the session. When set (the provider supports
   * one-off prompts) and the session is not in a terminal state, the expanded
   * panel shows a text input + Send. A stopped or done session has no live
   * terminal, so the input is hidden even when the handler is present.
   */
  onPrompt?: (text: string) => void;
  /**
   * Sends a single named key event (e.g. 'up'/'down'/'enter'/'escape') to the
   * session, for navigating an agent's interactive menu. Rides the same
   * capability as onPrompt. When set, the (empty) send-text box captures the
   * navigation keys and forwards them live rather than editing text.
   */
  onSendKey?: (key: string) => void;
  /**
   * Attaches one image to the session, resolving to null once it is attached or
   * to why it was not. Offered only when set and the session declares
   * `attachImage`: as an Attach image button beside Send, as a paste of an
   * image (with no text) into the send box, and as a drop of files on the
   * row. The row owns the rules:
   * it drops files that are not PNG or JPEG, and sends several one after
   * another, in order, stopping at the first that fails.
   */
  onAttachImage?: (file: File) => Promise<string | null>;
  /**
   * Shows why an attach failed: the refusal onAttachImage resolved to, or the
   * row's own when it dropped files that are not images. Its home is the
   * page-level error line every other session action uses, which the row does
   * not own.
   */
  onAttachError?: (message: string) => void;
  /**
   * How many trailing lines of a **live** session's terminal capture the expanded
   * panel makes available. The daemon streams up to its capture height (~24); this
   * caps how much of that screen is kept. Defaults to 20 when unset. The inline
   * teaser is always the single last line.
   *
   * It deliberately does not apply to a session that is *starting*, or to one
   * whose create *failed*: that preview is the provider's accumulated startup
   * log, and the panel is a scrollback over all of it. Either way the box shows
   * ten lines at a time and scrolls.
   */
  previewLines?: number;
  /**
   * When set, everything on this row that needs the daemon is disabled and this
   * text says why (it becomes the tooltip). One prop rather than a boolean plus a
   * message, so a disabled control can never be missing its reason.
   *
   * That includes the row's **text boxes**, not only its buttons: the send-text box
   * and the rename box both exist to feed a request, so an enabled one invites
   * typing that has nowhere to go. Their contents are kept, so nothing is
   * discarded — it simply cannot be added to until the daemon is back. The
   * send-text box is read-only rather than disabled, so it keeps focus.
   *
   * Deliberately a reason and not a transport state: this package is
   * transport-agnostic, so the app decides what "cannot reach the daemon" means
   * and supplies the words.
   */
  disabledReason?: string;
  /**
   * Makes the row itself a keyboard-navigable item of its list, which owns the
   * roving tab stop and passes 0 to the one row Tab lands on, -1 to the rest.
   * With the row focused, Enter opens the session (or shows an ended one's
   * details), F2 renames it, Delete (or
   * Cmd+Backspace on a Mac) removes it and Right/Left expand and collapse its
   * details. Unset, the row is not focusable.
   */
  tabIndex?: number;
  /** Whether it is its list's selection (see WorkstreamCard.selectedId). */
  selected?: boolean;
  ref?: Ref<SessionRowHandle>;
}

export interface SessionRowHandle {
  /** What the row offers, as its buttons and its actions menu do. */
  actions: () => ItemAction[];
  /**
   * Opens the details panel and puts focus in its send box, to answer the
   * session. Where the session takes no input, focus is left where it is.
   */
  expand: () => void;
}

/**
 * One agent conversation inside a workstream, or the placeholder standing for
 * one the daemon is still bringing up. Open hands off to the session (the local
 * provider launches a terminal); Stop/Continue are gated by the provider's
 * capability and the session state. An ended session looks recessed and says
 * when it ended, exactly on hover. Delete, in the More actions menu, is
 * offered in every state and confirms first, except on a failed row. A failed
 * one also carries the provider's reason on the row itself — that is all the
 * row is for.
 */
export function SessionRow({
  session,
  displayState,
  onOpen,
  onStop,
  onTakeOver,
  onStart,
  onRestartWithSettings,
  onRemove,
  onRename,
  onSeen,
  onPrompt,
  onSendKey,
  onAttachImage,
  onAttachError,
  previewLines = 20,
  disabledReason,
  tabIndex,
  selected,
  ref,
}: SessionRowProps) {
  const [confirming, setConfirming] = useState<
    'remove' | 'stop' | 'takeOver' | null
  >(null);
  // Whether Restart in HomeShell is under way, so a second click cannot post it again.
  const [takingOver, setTakingOver] = useState(false);
  const [renaming, setRenaming] = useState(false);
  // Why a rename was refused, shown against this row. Cleared the moment the user
  // starts editing again — a warning about a value they have since changed is
  // worse than none.
  const [renameWarning, setRenameWarning] = useState<string | null>(null);
  const [nameDraft, setNameDraft] = useState(session.title);
  const nameRef = useRef<HTMLInputElement>(null);
  const rowRef = useRef<HTMLLIElement>(null);
  const lineRef = useRef<HTMLDivElement>(null);
  const setRestoreFocus = useReturnFocus(renaming, () => rowRef.current);
  const { focusedByKeyboard, focusHandlers } = useKeyboardFocus();
  const controlTabIndex = controlTabIndexFor(tabIndex);
  const outputRef = useRef<HTMLPreElement>(null);
  // Whether the output box is pinned to its newest line. A ref, not state: it is
  // read and written from a scroll handler and an effect, and changing it must not
  // re-render (a render per scroll event while a build streams).
  const following = useRef(true);
  // A starting session opens expanded, because its output *is* the reason it is
  // on screen: the user is waiting on a build and the panel is where they watch
  // it. Only the initial value is forced, so their own toggle still wins from
  // then on — and the panel is not yanked shut when the session goes live.
  const starting = session.state === 'starting';
  // A create that failed. The row stays where the placeholder was, so the failure
  // is explained where the user was already watching for it.
  const failed = session.state === 'error';
  const [expanded, setExpanded] = useState(starting);
  // Whether the panel was open before the latest single click on the header
  // toggled it, for a double-click that turns out to be a rename to put back.
  const expandedBeforeClick = useRef<boolean | null>(null);
  // The handle's expand asks for focus in the send box. The box can show up a
  // render or more after the panel opens, so the ask waits for it, and lapses
  // once focus has gone somewhere other than the row: a late box must not pull
  // focus from wherever the user went, or once the user toggles the panel, or
  // after ANSWER_FOCUS_MS, for a box that takes that long or never comes. Held
  // as the deadline, or null with no ask. The count renders a repeat ask while
  // already expanded.
  const answerPending = useRef<number | null>(null);
  const [, setAnswerRequest] = useState(0);
  const promptInputRef = useRef<HTMLInputElement>(null);
  useEffect(() => {
    if (answerPending.current === null) return;
    // Not lapsed on a collapsed panel: an effect of a render from before the
    // expand can run between the ask and the render that opens the panel.
    const active = document.activeElement;
    const focusHere =
      !active || active === document.body || rowRef.current?.contains(active);
    if (!focusHere || Date.now() >= answerPending.current) {
      answerPending.current = null;
    } else if (promptInputRef.current) {
      answerPending.current = null;
      promptInputRef.current.focus();
    }
  });
  const [draft, setDraft] = useState('');
  const shown = displayState ?? session.state;
  const hasRuntime = hasSessionRuntime(session.state);
  // Stopped, or done by the daemon: what is left is to continue it. Not the
  // UI's own Done (idle, unseen), which is live, nor an error, which needs a
  // look first.
  const ended = session.ended ?? false;
  const stateWord = ended
    ? session.state === 'stopped'
      ? stateLabel('stopped')
      : dotStateLabel('ended')
    : stateLabel(shown);
  // Running and Idle say no more than the dot does, so only screen readers get
  // the word.
  const quietState = !ended && (shown === 'running' || shown === 'idle');
  // Every action here is a daemon call, so one flag gates them all. They stay
  // *visible* and go disabled rather than disappearing: a row that loses its
  // buttons reads as a broken row, and the user should still see what they will
  // be able to do again.
  const blocked = Boolean(disabledReason);
  // "Mark seen" only matters while the done overlay is showing; on any other
  // state the click is a no-op, so don't render the button affordance.
  const seenable = onSeen && shown === 'done';
  // A placeholder still starting stops by cancelling its build, which is
  // removing it: no provider has heard of it yet, and nothing is left to
  // continue. Offered where Stop will be, once it runs. Not a session starting
  // because it restarts: deleting that one would delete the session.
  const cancellable = Boolean(
    starting && session.placeholder && onStop && onRemove,
  );
  const stoppable = Boolean(hasRuntime && onStop) || cancellable;
  // A session HomeShell neither shows nor types into, and what stopping it costs.
  const {
    live,
    stopWarning,
    takeOver: canTakeOver,
    takeOverWarning,
    note,
    attachImage,
  } = sessionCapabilities(session);
  const takeOverable = onTakeOver && canTakeOver && session.restartable;
  // The warning is about ending a running session; an ended one has nothing to end.
  const liveWarning = isSessionTerminal(session.state)
    ? undefined
    : stopWarning;
  const promptable = live && onPrompt && hasRuntime;
  // Whether the session takes images at all. Whether it takes one now is its
  // state's call (acceptsImageNow); outside those states the button stays,
  // disabled, so it does not flicker as the agent works.
  const attachable = Boolean(onAttachImage && attachImage);
  const attachDisabledReason =
    disabledReason ??
    (acceptsImageNow(session.state)
      ? undefined
      : 'Images can be attached while the session is idle or running');
  const startable = ended && onStart;
  // Removal is offered in every state, the two placeholder ones included: the
  // daemon recognizes its own bookkeeping rows and never routes them to a
  // provider that has not heard of them. Removing a starting row is how a build
  // in flight is cancelled, and removing a failed one is the only disposal it
  // has — nothing else will ever clear it.
  const removable = Boolean(onRemove);
  // A failed row has no runtime to lose, so it goes on one click. Every other
  // state confirms, a build in flight included: cancelling it throws away work.
  const confirmsRemoval = !failed;
  // Renaming a starting placeholder would name a session that does not exist yet
  // — the name would go with the placeholder when the real session replaces it.
  const renamable = Boolean(onRename) && !starting;
  // Renaming is a gesture on the name itself — double-click, F2 on the focused
  // row, or Alt+R — rather than a button in the action cluster, so the row
  // carries one fewer control and the name is what you act on. A tint on hover
  // and the name's tooltip say so.
  const canRename = renamable && !blocked && !renaming;
  const openable = onOpen && hasRuntime;
  const preview = session.preview ?? '';
  // The tail teaser is only its last line; the full capture lives in the panel.
  const allPreviewLines = preview.split('\n');
  const lastPreviewLine = allPreviewLines[allPreviewLines.length - 1];
  // What the expanded panel renders. The two kinds of preview are different
  // kinds of thing:
  //
  //   - a **startup log** — the provider's output, accumulated by the daemon —
  //     is rendered whole, so the box becomes a scrollback the user can read
  //     upwards through. Capping it here would throw away history the daemon
  //     deliberately kept.
  //   - a **live session's** preview is a *screen capture*, and `previewLines`
  //     is the user's choice of how much of that screen to show.
  //
  // A row keeps its startup log after a create *fails*, which is when it matters
  // most: the daemon holds the output that explains the failure on the same row
  // ("keeping the startup output that led to it"), so deciding this on `starting`
  // alone would truncate the log at the one moment someone needs to read it.
  //
  // Either way the box itself is ten lines tall and scrolls (see the stylesheet),
  // so this decides how much there is to scroll through, not how much is visible.
  const startupLog = starting || failed;
  const shownPreview = startupLog
    ? preview
    : allPreviewLines.slice(-Math.max(1, previewLines)).join('\n');

  useEffect(() => {
    if (renaming) nameRef.current?.select();
  }, [renaming]);

  // Reopening the panel starts following again: it is a fresh element scrolled to
  // the top, and the newest output is what the user came back for.
  useEffect(() => {
    if (expanded) following.current = true;
  }, [expanded]);

  // Keep the newest line in view as output arrives — the box holds ten lines and a
  // build produces hundreds, so without this the panel would sit on the oldest ten
  // and the live end would scroll away below the fold. Skipped while the user is
  // reading history, so following never yanks them back down mid-scroll.
  useEffect(() => {
    const box = outputRef.current;
    if (!box || !following.current) return;
    box.scrollTop = box.scrollHeight;
  }, [expanded, shownPreview]);

  /**
   * Follows the tail only while the box is *at* the tail. Scrolling up opts out and
   * scrolling back to the bottom opts in again, which is the behaviour of every
   * log viewer and needs no button.
   */
  const handleOutputScroll = () => {
    const box = outputRef.current;
    if (!box) return;
    // A few pixels of slack: fractional line heights mean an exact comparison can
    // never be true, and the panel would stop following after one line.
    const distanceFromBottom =
      box.scrollHeight - box.scrollTop - box.clientHeight;
    following.current = distanceFromBottom <= 4;
  };

  const startRenaming = () => {
    setNameDraft(session.title);
    setRenameWarning(null);
    setRenaming(true);
  };

  /**
   * Leaves rename mode, asking for the change only when there is one. An emptied
   * box is a change: it means "back to the provider's own name", which the daemon
   * treats as its own operation.
   *
   * Nothing is asked while blocked. The box is disabled the moment the daemon goes
   * away, and disabling a focused input blurs it — so without this, losing the
   * connection mid-rename would fire the very request that cannot succeed. The edit
   * is dropped rather than queued: the row goes back to the name the daemon last
   * reported, which is the truth as far as anyone knows it.
   */
  const commitRename = () => {
    const next = nameDraft.trim();
    if (!onRename || blocked || next === session.title) {
      setRenaming(false);
      return;
    }
    const settle = (refusal: string | null | void) => {
      if (typeof refusal === 'string' && refusal) {
        // Refused: stay in rename mode so the rejected value is still there to
        // edit — closing the box would make the user retype it to try again —
        // and say why beside the row. Focus is deliberately not stolen back:
        // this can arrive from a blur, and the user may already be elsewhere.
        setRenameWarning(refusal);
        setRestoreFocus(false);
        return;
      }
      setRenameWarning(null);
      setRenaming(false);
    };
    const outcome = onRename(next);
    // A handler that answers synchronously settles in this tick. Awaiting it
    // regardless would defer the close to a microtask, leaving the box on screen
    // for a frame after a rename that never had anything to wait for.
    if (!isThenable(outcome)) {
      settle(undefined);
      return;
    }
    void outcome.then(settle);
  };

  const handleNameKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key === 'Enter') {
      event.preventDefault();
      setRestoreFocus(true);
      commitRename();
    } else if (event.key === 'Escape') {
      event.preventDefault();
      setRestoreFocus(true);
      setRenaming(false);
      setRenameWarning(null);
      setNameDraft(session.title);
    }
  };

  const submitPrompt = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const text = draft.trim();
    if (!text || !onPrompt || blocked) return;
    onPrompt(text);
    setDraft('');
  };

  const attachInputRef = useRef<HTMLInputElement>(null);
  // Whether a batch is still uploading, so a second pick cannot interleave with it.
  const [attaching, setAttaching] = useState(false);
  // The same, read synchronously: two drops or pastes can arrive before the
  // row re-renders with `attaching` set.
  const attachingRef = useRef(false);
  // The last image attached, named in the row until ATTACH_STATUS_MS passes.
  const [attached, setAttached] = useState<string | null>(null);
  useEffect(() => {
    if (!attached) return;
    const timer = setTimeout(() => setAttached(null), ATTACH_STATUS_MS);
    return () => clearTimeout(timer);
  }, [attached]);

  // Why the row takes no image right now, if it doesn't: the button's
  // disabled reason, or an earlier batch still on its way.
  const attachBlocked =
    attachDisabledReason ?? (attaching ? STILL_ATTACHING : undefined);

  const attachFiles = async (files: File[]) => {
    if (!onAttachImage) return;
    const images = files.filter((file) => ATTACHABLE_TYPES.includes(file.type));
    if (images.length < files.length) onAttachError?.(ONLY_IMAGES);
    if (images.length === 0) return;
    if (attachingRef.current) {
      onAttachError?.(STILL_ATTACHING);
      return;
    }
    attachingRef.current = true;
    setAttaching(true);
    setAttached(null);
    try {
      // One at a time, in the order given: the agent receives them in the
      // order the user meant, and a failure leaves no later image delivered.
      for (const image of images) {
        const refusal = await onAttachImage(image);
        if (refusal) {
          onAttachError?.(refusal);
          return;
        }
        setAttached(image.name);
      }
    } finally {
      attachingRef.current = false;
      setAttaching(false);
    }
  };

  const handlePromptPaste = (event: ClipboardEvent) => {
    if (!attachable) return;
    const pasted = pastedImages(event.clipboardData);
    if (pasted === 'unreadable') {
      event.preventDefault();
      onAttachError?.(PASTE_UNREADABLE);
      return;
    }
    if (pasted.length === 0) return;
    event.preventDefault();
    if (attachBlocked) {
      onAttachError?.(attachBlocked);
      return;
    }
    void attachFiles(pasted);
  };

  // Files dragged over the row: 'ready' to attach them, 'refused' when the
  // session takes no image right now. Enter and leave fire for every child the
  // drag crosses, so a depth count tells when it really left the row.
  const [filesOver, setFilesOver] = useState(false);
  const dragDepth = useRef(0);
  const endDrag = useCallback(() => {
    dragDepth.current = 0;
    setFilesOver(false);
  }, []);
  // A refused drag never drops, and some engines miss a leave, which would
  // leave the count, and the highlight, behind. The drag moving on anywhere
  // else in the page, or ending, clears it.
  useEffect(() => {
    if (!filesOver) return;
    const onPageDragOver = (event: Event) => {
      const row = rowRef.current;
      if (row && !row.contains(event.target as Node)) endDrag();
    };
    window.addEventListener('dragover', onPageDragOver);
    window.addEventListener('drop', endDrag);
    window.addEventListener('dragend', endDrag);
    return () => {
      window.removeEventListener('dragover', onPageDragOver);
      window.removeEventListener('drop', endDrag);
      window.removeEventListener('dragend', endDrag);
    };
  }, [filesOver, endDrag]);
  const dropState =
    !attachable || !filesOver ? undefined : attachBlocked ? 'refused' : 'ready';
  const dropHandlers: HTMLAttributes<HTMLLIElement> = attachable
    ? {
        onDragEnter: (event) => {
          if (!carriesFiles(event)) return;
          event.preventDefault();
          dragDepth.current += 1;
          setFilesOver(true);
        },
        onDragOver: (event) => {
          if (!carriesFiles(event)) return;
          // Taken either way, so the browser does not open a refused file in
          // place of the app; 'none' shows the user it will not land.
          event.preventDefault();
          event.dataTransfer.dropEffect = attachBlocked ? 'none' : 'copy';
        },
        onDragLeave: (event) => {
          if (!carriesFiles(event)) return;
          dragDepth.current = Math.max(0, dragDepth.current - 1);
          if (dragDepth.current === 0) setFilesOver(false);
        },
        onDrop: (event) => {
          if (!carriesFiles(event)) return;
          event.preventDefault();
          endDrag();
          if (attachBlocked) {
            onAttachError?.(attachBlocked);
            return;
          }
          void attachFiles(Array.from(event.dataTransfer.files));
        },
      }
    : {};

  // When the box is empty it acts as a navigation surface: arrow keys, Enter
  // and Escape are forwarded to the session as key events (for driving an
  // agent's arrow-key menu) instead of editing text. Once the user types
  // anything, it is a normal text field again and Send/Enter submits the line.
  const navKeys: Record<string, string> = {
    ArrowUp: 'up',
    ArrowDown: 'down',
    ArrowLeft: 'left',
    ArrowRight: 'right',
    Escape: 'escape',
  };
  const handlePromptKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    // The box is only read-only while blocked, so that it keeps focus, and these
    // keys are requests.
    if (!onSendKey || draft.length !== 0 || blocked) return;
    const navKey = navKeys[event.key];
    if (navKey) {
      event.preventDefault();
      onSendKey(navKey);
      return;
    }
    if (event.key === 'Enter') {
      event.preventDefault();
      onSendKey('enter');
    }
  };

  // Its tooltip also reads a truncated title in full.
  const renamableTitle = useRenamableTitle<HTMLSpanElement>({
    name: session.title,
    onRename: startRenaming,
    disabledReason,
    f2: true,
    className: styles.title,
  });
  const renameOnDoubleClick = renamableTitle.onDoubleClick;
  const titleProps = renamable
    ? {
        ...renamableTitle,
        // Puts back the panel its first click toggled (see toggleDetails).
        onDoubleClick:
          renameOnDoubleClick &&
          (() => {
            const before = expandedBeforeClick.current;
            expandedBeforeClick.current = null;
            if (before !== null) setExpanded(before);
            renameOnDoubleClick();
          }),
      }
    : { className: styles.title, title: session.title };

  // The dot sits in a slot of fixed width, so the larger "!" of an error does
  // not push its title out of line with the rows around it.
  const dot = (
    <span className={styles.dotSlot}>
      <StatusDot state={ended ? 'ended' : shown} />
    </span>
  );
  const status = (
    <span className={styles.status}>
      <span
        className={cx(
          !ended && styles[shown],
          quietState && a11y.visuallyHidden,
        )}
        data-testid="session-state"
      >
        {stateWord}
      </span>
      {ended && session.endedAt && <EndedAgo endedAt={session.endedAt} />}
    </span>
  );
  // The top row keeps its single-line shape (dot · title · agent · state ·
  // actions); the tail teaser and expanded panel sit on their own full width
  // rows below, so the state and buttons never drift out of alignment.
  const info = (
    <>
      {dot}
      <span {...titleProps} data-testid="session-title">
        {session.title}
      </span>
      <span className={styles.agent}>{session.agent}</span>
      {status}
    </>
  );
  const detailsId = `session-details-${session.id}`;
  // The output box is named by the caption above it rather than by its own
  // aria-label, so the name is not announced twice now that it is on screen.
  const outputLabelId = `session-output-label-${session.id}`;
  // Clicking the row header toggles the details panel (the whole title area acts
  // as the disclosure). For a done session it also clears the attention
  // overlay — looking at the session counts as seeing it.
  const toggleExpanded = () => {
    if (seenable) onSeen?.();
    answerPending.current = null;
    setExpanded((open) => !open);
  };
  // Remembers what the panel was, for a double-click's rename to put back.
  const toggleDetails = () => {
    expandedBeforeClick.current = expanded;
    toggleExpanded();
  };
  const infoProps = {
    onClick: onSingleClick(toggleDetails),
    'aria-expanded': expanded,
    'aria-controls': detailsId,
    title: seenable
      ? 'Mark this session as seen and toggle details'
      : 'Toggle session details',
  };

  const requestRemove = () =>
    confirmsRemoval ? setConfirming('remove') : onRemove?.();

  // The one account of what the row offers: its buttons, its keys and its
  // actions menu all read it.
  const open: ItemAction = {
    id: 'open',
    label: 'Open',
    run: () => onOpen?.(),
    disabled: !openable || blocked,
  };
  // Cancelling a start asks first, as Delete does on a starting row: the build
  // may have run for minutes.
  const requestStop = () =>
    cancellable || stopWarning ? setConfirming('stop') : onStop?.();
  const takeOverNow = async () => {
    setTakingOver(true);
    try {
      await onTakeOver?.();
    } finally {
      setTakingOver(false);
    }
  };
  const startStop: (ItemAction & { title: string }) | undefined = startable
    ? {
        id: 'startStop',
        label: 'Continue',
        title: 'Continue this session',
        run: () => onStart?.(),
        disabled: blocked,
      }
    : stoppable
      ? {
          id: 'startStop',
          label: 'Stop',
          title: cancellable
            ? 'Cancel starting this session'
            : 'Stop this session',
          run: requestStop,
          disabled: blocked,
        }
      : undefined;
  const rename: ItemAction | undefined = renamable
    ? {
        id: 'rename',
        label: 'Rename',
        run: startRenaming,
        disabled: !canRename,
        shortcut: 'f2',
      }
    : undefined;
  // Delete acts on the row only where it can take focus.
  const remove: ItemAction | undefined = removable
    ? {
        id: 'delete',
        label: 'Delete',
        run: requestRemove,
        disabled: blocked,
        destructive: true,
        shortcut: tabIndex === undefined ? undefined : 'delete',
      }
    : undefined;
  const restart: ItemAction | undefined = onRestartWithSettings
    ? {
        id: 'restartWithSettings',
        label: 'Restart with settings…',
        run: onRestartWithSettings,
        disabled: blocked,
      }
    : undefined;
  const takeOver: ItemAction | undefined = takeOverable
    ? {
        id: 'takeOver',
        label: 'Restart in HomeShell',
        run: () => setConfirming('takeOver'),
        disabled: blocked || takingOver,
      }
    : undefined;
  const details: ItemAction = {
    id: 'details',
    label: expanded ? 'Hide details' : 'Show details',
    run: toggleExpanded,
  };
  const actions = [
    open,
    startStop,
    takeOver,
    restart,
    rename,
    details,
    remove,
  ].filter((entry) => entry !== undefined);
  useImperativeHandle(ref, () => ({
    actions: () => actions,
    expand: () => {
      if (seenable) onSeen?.();
      setExpanded(true);
      answerPending.current = Date.now() + ANSWER_FOCUS_MS;
      setAnswerRequest((count) => count + 1);
    },
  }));
  const actionsMenu = useActionsMenu(
    `Actions for session ${session.title}`,
    actions,
  );

  // Delete has no button of its own, so its letter sits on the row's line,
  // which takes the key as a Delete rather than as a click.
  const deleteByLetter = remove && !remove.disabled ? remove.run : undefined;
  const takeDeleteLetter = useMnemonicHandler<HTMLDivElement>(deleteByLetter);
  const setLine = useCallback(
    (element: HTMLDivElement | null) => {
      lineRef.current = element;
      const release = takeDeleteLetter(element);
      return () => {
        lineRef.current = null;
        release?.();
      };
    },
    [takeDeleteLetter],
  );

  // F2 renames the row. Double-click is a mouse-only gesture, and it is the only
  // pointer route in — without this there would be no keyboard way to rename at
  // all. F2 is what file managers, IDEs and spreadsheets use.
  //
  // Handled on the row rather than on one control inside it, so it fires wherever
  // focus happens to be — the row has two focusable halves (the title area and the
  // caret) plus the action buttons, and requiring the right one would be a rule
  // nobody could see.
  //
  // The other keys act only on the focused row itself: inside it, Enter and the
  // arrows already belong to the control that has focus. On the row, none acts
  // unless the keyboard put focus there.
  const handleRowKeyDown = (event: KeyboardEvent<HTMLElement>) => {
    const onRow = event.target === event.currentTarget;
    if (onRow && !focusedByKeyboard()) return;
    if (onRow && actionsMenu.openOn(event, lineRef.current)) return;
    if (isModified(event)) return;
    if (event.key === 'F2') {
      runAction(event, actions, 'rename');
      return;
    }
    if (!onRow) return;
    if (event.key === 'Enter' && !event.repeat && ended) {
      event.preventDefault();
      toggleExpanded();
    } else if (event.key === 'Enter' && !event.repeat) {
      runAction(event, actions, 'open');
    } else if (isDeleteKey(event) && !event.repeat) {
      runAction(event, actions, 'delete');
    } else if (
      (event.key === 'ArrowRight' && !expanded) ||
      (event.key === 'ArrowLeft' && expanded)
    ) {
      event.preventDefault();
      toggleExpanded();
    }
  };
  // Under the send box when it shows; on the row otherwise, so an image
  // dropped on a collapsed row is still confirmed. While a drag the row refuses
  // is over it, it says why: a tooltip does not show during a drag.
  const attachStatus = (onRow: boolean) => (
    <p
      className={
        onRow
          ? `${styles.attachStatus} ${styles.attachStatusOnRow}`
          : styles.attachStatus
      }
      role="status"
    >
      {dropState === 'refused'
        ? attachBlocked
        : attached && `Image attached: ${attached}`}
    </p>
  );
  return (
    <li
      ref={rowRef}
      className={styles.item}
      tabIndex={tabIndex}
      data-nav-id={session.id}
      data-ended={ended || undefined}
      data-drop={dropState}
      {...mnemonicScope('item')}
      onContextMenu={(event) =>
        actionsMenu.openOnContextMenu(event, lineRef.current)
      }
      aria-current={selected || undefined}
      aria-keyshortcuts={
        tabIndex === undefined ? undefined : listItemKeyShortcuts('session')
      }
      onKeyDown={handleRowKeyDown}
      {...focusHandlers}
      {...dropHandlers}
    >
      <div
        ref={setLine}
        className={styles.row}
        {...mnemonicProps(deleteByLetter && MNEMONICS.delete)}
      >
        {renaming ? (
          // A plain container while editing: the info area is otherwise a button,
          // and a text field inside a button cannot be typed into. The status dot
          // and state word stay either side of the box — the row keeps its shape,
          // and a session being renamed does not stop saying what it is doing. The
          // agent gives up its space to the box.
          <div className={styles.info}>
            {dot}
            <input
              ref={nameRef}
              type="text"
              className={`${renameInputClass} ${styles.nameInput}`}
              value={nameDraft}
              onChange={(event) => {
                setNameDraft(event.target.value);
                setRenameWarning(null);
              }}
              onBlur={commitRename}
              onKeyDown={handleNameKeyDown}
              aria-label="Session name"
              placeholder="Clear to use the default name"
              disabled={blocked}
              title={disabledReason}
            />
            {status}
          </div>
        ) : (
          <button
            type="button"
            className={styles.info}
            tabIndex={controlTabIndex}
            aria-keyshortcuts={canRename ? 'F2' : undefined}
            {...infoProps}
          >
            {info}
          </button>
        )}
        <span className={styles.actions}>
          {/* An ended session has nothing to open; Continue stands in its place. */}
          {!ended && (
            <Button
              className={controls.compact}
              icon={<TerminalIcon size={13} />}
              onClick={open.run}
              disabled={open.disabled}
              tabIndex={controlTabIndex}
              mnemonic={MNEMONICS.open}
              shortcuts={tabIndex === undefined ? undefined : ['enter']}
              title={
                disabledReason ??
                (!onOpen
                  ? 'Not yet implemented'
                  : openable
                    ? 'Open a terminal attached to this session'
                    : 'Session is not running')
              }
            >
              Open
            </Button>
          )}
          {/* One button, so that the one pressed keeps focus as the session
              flips between running and stopped — and so a held Enter must not
              press it again once it has flipped. */}
          {startStop && (
            <Button
              className={cx(controls.compact, startable && styles.continue)}
              icon={
                startable ? <PlayIcon size={13} /> : <SquareIcon size={13} />
              }
              onClick={startStop.run}
              tabIndex={controlTabIndex}
              mnemonic={MNEMONICS.startStop}
              onKeyDown={(event) => {
                if (event.key === 'Enter' && event.repeat) {
                  event.preventDefault();
                }
              }}
              aria-disabled={startStop.disabled}
              aria-label={startStop.label}
              title={disabledReason ?? startStop.title}
            />
          )}
          {takeOver && (
            <Button
              className={controls.compact}
              onClick={takeOver.run}
              tabIndex={controlTabIndex}
              aria-disabled={takeOver.disabled}
              title={
                disabledReason ??
                'End this session where it runs and continue it in HomeShell'
              }
            >
              {takeOver.label}
            </Button>
          )}
          {actionsMenu.moreActionsButton({
            tabIndex: controlTabIndex,
            compact: true,
          })}
          <button
            type="button"
            className={cx(controls.bareButton, controls.compact)}
            tabIndex={controlTabIndex}
            onClick={onSingleClick(toggleDetails)}
            aria-expanded={expanded}
            aria-controls={detailsId}
            aria-label="Toggle session details"
            title="Toggle session details"
          >
            <span
              className={cx(styles.caret, expanded && styles.caretOpen)}
              aria-hidden="true"
            >
              ›
            </span>
          </button>
        </span>
      </div>
      {actionsMenu.menu}
      {confirming === 'stop' && onStop && (
        <ActionConfirm
          title={
            cancellable
              ? `Stop starting "${session.title}"?`
              : `Stop "${session.title}"?`
          }
          confirmLabel="Stop"
          danger
          mnemonic={MNEMONICS.startStop}
          onConfirm={cancellable ? () => onRemove?.() : onStop}
          onClose={() => setConfirming(null)}
        >
          <p>
            {cancellable
              ? 'Its startup is cancelled, and the session goes.'
              : stopWarning}
          </p>
        </ActionConfirm>
      )}
      {confirming === 'takeOver' && onTakeOver && (
        <ActionConfirm
          title={`Restart "${session.title}" in HomeShell?`}
          confirmLabel="Restart in HomeShell"
          onConfirm={takeOverNow}
          onClose={() => setConfirming(null)}
        >
          {takeOverWarning && <p>{takeOverWarning}</p>}
        </ActionConfirm>
      )}
      {confirming === 'remove' && onRemove && (
        <ActionConfirm
          title={`Delete "${session.title}"?`}
          confirmLabel="Delete"
          danger
          mnemonic={MNEMONICS.delete}
          onConfirm={onRemove}
          onClose={() => setConfirming(null)}
        >
          <p>This will delete the session from the workstream.</p>
          {liveWarning && <p>{liveWarning}</p>}
        </ActionConfirm>
      )}
      {renameWarning && (
        <p
          className={styles.rowWarning}
          role="alert"
          data-testid="session-warning"
        >
          {renameWarning}
        </p>
      )}
      {/*
       * A failed session's whole account of itself, in the provider's words. It
       * sits on the row rather than inside the details panel because it is the
       * reason the row is there at all — a user who has to expand something to
       * find out why is being asked to hunt for it.
       */}
      {failed && session.error && (
        <p
          className={styles.errorMessage}
          data-testid="session-error"
          role="alert"
        >
          {session.error}
        </p>
      )}
      {note && (
        <p className={styles.rowNote} data-testid="session-note">
          {note}
        </p>
      )}
      {session.outdatedSettings?.length ? (
        <p className={styles.rowNote} data-testid="session-outdated">
          Settings changed since it started:{' '}
          {session.outdatedSettings.join(', ')}.
          {onRestartWithSettings && ' Restart it with settings to apply them.'}
        </p>
      ) : null}
      {attachable && !(expanded && promptable) && attachStatus(true)}
      {/* An ended session's last words are history, kept for its details. */}
      {preview && !expanded && !ended && (
        <div
          className={styles.preview}
          {...titleWhenTruncated(lastPreviewLine)}
          data-testid="session-preview"
        >
          {lastPreviewLine}
        </div>
      )}
      {expanded && (
        <div className={styles.expanded} id={detailsId}>
          {preview ? (
            <>
              {/*
               * The visible counterpart of the box's aria-label. The panel holds
               * two surfaces that look alike until they are named — the session's
               * own output above, HomeShell's send box below — and the reader who
               * expanded the row is here to read the text closely, which is
               * exactly when "is this the agent or the app?" has to be settled.
               */}
              <p className={styles.outputLabel} id={outputLabelId}>
                Session output
              </p>
              <pre
                ref={outputRef}
                onScroll={handleOutputScroll}
                className={styles.previewFull}
                data-testid="session-preview-full"
                tabIndex={controlTabIndex ?? 0}
                aria-labelledby={outputLabelId}
              >
                {shownPreview}
              </pre>
            </>
          ) : (
            <p className={styles.previewEmpty}>
              {starting ? 'Starting…' : 'No output captured yet.'}
            </p>
          )}
          {/*
           * No send box while starting: there is no agent listening yet, so the
           * daemon would refuse the text (and the arrow keys behind it).
           */}
          {promptable && (
            <>
              <form className={styles.promptForm} onSubmit={submitPrompt}>
                <input
                  ref={promptInputRef}
                  type="text"
                  className={styles.promptInput}
                  placeholder="Send text to the session…"
                  aria-label="Send text to session"
                  value={draft}
                  onChange={(event) => setDraft(event.target.value)}
                  onKeyDown={handlePromptKeyDown}
                  onPaste={handlePromptPaste}
                  tabIndex={controlTabIndex}
                  readOnly={blocked}
                  aria-disabled={blocked}
                  title={disabledReason}
                />
                <Button
                  type="submit"
                  variant="primary"
                  aria-disabled={blocked}
                  tabIndex={controlTabIndex}
                  title={disabledReason}
                >
                  Send
                </Button>
                {attachable && (
                  <>
                    <Button
                      icon={<PaperclipIcon size={13} />}
                      aria-label="Attach image"
                      aria-disabled={Boolean(attachDisabledReason) || attaching}
                      tabIndex={controlTabIndex}
                      title={
                        attachDisabledReason ??
                        (attaching ? 'Attaching images…' : 'Attach image')
                      }
                      onClick={() => attachInputRef.current?.click()}
                    />
                    {/* The picker the button opens; it is the button the user sees. */}
                    <input
                      ref={attachInputRef}
                      type="file"
                      accept={ATTACHABLE_TYPES.join(',')}
                      multiple
                      hidden
                      tabIndex={-1}
                      onChange={(event) => {
                        const files = Array.from(event.target.files ?? []);
                        // Cleared, so picking the same file again still changes it.
                        event.target.value = '';
                        void attachFiles(files);
                      }}
                    />
                  </>
                )}
              </form>
              {attachable && attachStatus(false)}
              {/* The hint promises key navigation, which is off while blocked. */}
              {onSendKey && !blocked && (
                <p className={styles.promptHint}>
                  Empty box: ↑ ↓ navigate · Enter selects · Esc cancels
                </p>
              )}
            </>
          )}
        </div>
      )}
    </li>
  );
}

export default SessionRow;
