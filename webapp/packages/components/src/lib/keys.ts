import type { KeyboardEvent } from 'react';

/** Whether the browser runs on macOS (or iOS), whose keys differ. */
export function isMac(): boolean {
  return /Mac|iPhone|iPad/.test(navigator.userAgent);
}

/**
 * Keys are written in VS Code's notation, the one every shortcut of the app is
 * given in: one or more space-separated strokes (`mod+k mod+s` is a chord), a
 * stroke being `+`-joined modifiers and a key named as KeyboardEvent.key names
 * it, lowercased. `mod` is Cmd on macOS and Ctrl everywhere else; `ctrl` is
 * Control everywhere.
 */
export interface Stroke {
  mod: boolean;
  ctrl: boolean;
  shift: boolean;
  alt: boolean;
  key: string;
}

function parseStroke(stroke: string): Stroke {
  const parts = stroke.split('+');
  return {
    mod: parts.includes('mod'),
    ctrl: parts.includes('ctrl'),
    shift: parts.includes('shift'),
    alt: parts.includes('alt'),
    key: parts[parts.length - 1],
  };
}

/** The strokes of `keys` (see Stroke). */
export function parseKeys(keys: string): Stroke[] {
  return keys.split(' ').map(parseStroke);
}

const KEY_LABELS: Record<string, string> = {
  arrowup: '↑',
  arrowdown: '↓',
  arrowleft: '←',
  arrowright: '→',
  escape: 'Esc',
};

const MAC_KEY_LABELS: Record<string, string> = { backspace: '⌫' };

/** A key as KeyboardEvent.key names it: `arrowup` is `ArrowUp`. */
function keyName(key: string): string {
  const arrow = /^arrow(\w+)$/.exec(key);
  if (arrow)
    return `Arrow${arrow[1].charAt(0).toUpperCase()}${arrow[1].slice(1)}`;
  return key.charAt(0).toUpperCase() + key.slice(1);
}

function formatStroke(stroke: Stroke, mac: boolean): string {
  const key =
    (mac ? MAC_KEY_LABELS[stroke.key] : undefined) ??
    KEY_LABELS[stroke.key] ??
    keyName(stroke.key);
  if (mac) {
    return `${stroke.ctrl ? '⌃' : ''}${stroke.alt ? '⌥' : ''}${stroke.shift ? '⇧' : ''}${stroke.mod ? '⌘' : ''}${key}`;
  }
  return [
    (stroke.mod || stroke.ctrl) && 'Ctrl',
    stroke.shift && 'Shift',
    stroke.alt && 'Alt',
    key,
  ]
    .filter(Boolean)
    .join('+');
}

/** How keys read on the platform: `Ctrl+Shift+P` elsewhere, `⇧⌘P` on a Mac. */
export function formatKeys(keys: string, mac = isMac()): string {
  return parseKeys(keys)
    .map((stroke) => formatStroke(stroke, mac))
    .join(' ');
}

/** Keys of one stroke as an aria-keyshortcuts value: `mod+backspace` is `Meta+Backspace` on a Mac. */
export function ariaKeys(keys: string, mac = isMac()): string {
  const [stroke] = parseKeys(keys);
  return [
    ((stroke.mod && !mac) || stroke.ctrl) && 'Control',
    stroke.mod && mac && 'Meta',
    stroke.alt && 'Alt',
    stroke.shift && 'Shift',
    keyName(stroke.key),
  ]
    .filter(Boolean)
    .join('+');
}

/**
 * Whether a keypress asks to delete the focused item: Delete, or on a Mac
 * Cmd+Backspace, which is what its keyboard offers without a Delete key (as in
 * Finder and VS Code's explorer).
 */
export function isDeleteKey(event: KeyboardEvent, mac = isMac()): boolean {
  return (
    event.key === 'Delete' ||
    (mac && event.key === 'Backspace' && event.metaKey)
  );
}

/**
 * Whether a keypress carries a modifier, and so is not one of the plain keys
 * the lists, pills and dialogs move and act by: Ctrl+Enter or Shift+F2 stay
 * free for the browser and the command keybindings. Cmd does not count on a
 * Mac's Backspace, where it makes Delete.
 */
export function isModified(event: KeyboardEvent, mac = isMac()): boolean {
  return (
    event.altKey ||
    event.ctrlKey ||
    event.shiftKey ||
    (event.metaKey && !(mac && event.key === 'Backspace'))
  );
}

/**
 * Whether a key goes to a field that takes text: an input, text area, select
 * or editable element, which the app's keys leave the keys it types.
 */
export function isTextEntry(target: EventTarget | null): boolean {
  return (
    target instanceof HTMLElement &&
    (target.isContentEditable ||
      ['INPUT', 'TEXTAREA', 'SELECT'].includes(target.tagName))
  );
}

export interface KeyMoveOptions {
  /** Whether a step past one end lands on the other; else it goes nowhere. */
  wrap?: boolean;
  /** Which arrows step: Left/Right, Up/Down, or both. */
  orientation?: 'horizontal' | 'vertical' | 'both';
}

const STEPS = {
  horizontal: { ArrowLeft: -1, ArrowRight: 1 },
  vertical: { ArrowUp: -1, ArrowDown: 1 },
  both: { ArrowLeft: -1, ArrowRight: 1, ArrowUp: -1, ArrowDown: 1 },
} as const satisfies Record<string, Record<string, number>>;

/**
 * Where `key` moves the focus of a row or column of `count` items from
 * `index`: an arrow steps, Home and End go to either end. Undefined for any
 * other key, and for a step past an end that does not wrap.
 */
export function nextIndex(
  key: string,
  index: number,
  count: number,
  { wrap = false, orientation = 'vertical' }: KeyMoveOptions = {},
): number | undefined {
  if (count === 0) return undefined;
  if (key === 'Home') return 0;
  if (key === 'End') return count - 1;
  const step = (STEPS[orientation] as Record<string, number>)[key];
  if (step === undefined) return undefined;
  const next = index + step;
  if (wrap) return (next + count) % count;
  return next >= 0 && next < count ? next : undefined;
}

/**
 * Moves focus among `elements` as `nextIndex` says, from the one the key was
 * pressed on, and takes the key. Returns the index focused, if any.
 */
export function moveFocus(
  event: KeyboardEvent,
  elements: readonly HTMLElement[],
  options?: KeyMoveOptions,
): number | undefined {
  const index = elements.indexOf(event.target as HTMLElement);
  if (index === -1) return undefined;
  const next = nextIndex(event.key, index, elements.length, options);
  if (next === undefined) return undefined;
  event.preventDefault();
  elements[next].focus();
  return next;
}

/** Keys that do one thing, and what, for the user. */
export interface KeyHelp {
  /** Each in the notation of Stroke. */
  keys: readonly string[];
  /** Replaces `keys` on macOS, where the system takes some of them. */
  mac?: readonly string[];
  label: string;
}

/** The keys on the platform, macOS when `mac` is set. */
export function platformKeys(
  help: Pick<KeyHelp, 'keys' | 'mac'>,
  mac = isMac(),
): readonly string[] {
  return (mac && help.mac) || help.keys;
}
