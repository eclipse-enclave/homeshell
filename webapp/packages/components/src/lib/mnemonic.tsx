import { type ReactNode, useEffect } from 'react';
import { ariaKeys, formatKeys, isMac, isTextEntry } from './keys';
import styles from './mnemonic.module.css';

/**
 * Mnemonics: the letter of a button's label that, with Alt held, presses it —
 * underlined while Alt is down, as on a desktop app's dialogs and menus. A
 * title can carry one too, for renaming it (see useRenamableTitle).
 *
 * Which buttons a letter can reach is decided by **scope**, innermost first, so
 * the same letter means the same thing on every item ("N" is New on a project,
 * a workstream and a session alike) and the one in reach is the one meant:
 *
 *   1. an open modal dialog or menu, or a dialog still loading what it shows,
 *      alone — nothing behind it is in reach;
 *   2. else a form with focus in it that has letters of its own, alone, its
 *      text fields included;
 *   3. else the selected item of the workstream list (a card or a row), while
 *      focus is in the list or nowhere — not on a control elsewhere, or in a
 *      form inside an item, where the user is at work on something else;
 *   4. then the page's own controls, for letters the selection leaves free.
 *
 * A button belongs to the closest scope around it, so a card's buttons are its
 * own and not its rows'. It is all read off the DOM when Alt is pressed, which
 * keeps the components ignorant of each other: a button declares its letter,
 * a container declares a scope, and useMnemonics does the rest.
 */

const MNEMONIC_SCOPE = 'data-mnemonic-scope';
/** A button's letter; one in upper case is pressed with Alt+Shift. */
const MNEMONIC = 'data-mnemonic';
/**
 * Raised, cancelable, on what a letter reaches before it is clicked. Something
 * a click does not activate — a title, which a click selects but Alt+R renames
 * — takes it by cancelling it, and is not clicked.
 */
export const MNEMONIC_EVENT = 'mnemonic';
/** On each button a letter reaches, while Alt is held. */
const SHOWN = 'data-mnemonic-shown';
const MODAL = '[aria-modal="true"]';
/**
 * Takes every letter while open: a modal dialog, a (portalled) menu, or what
 * stands for a dialog until it opens.
 */
const EXCLUSIVE = `${MODAL}, [role="menu"], [${MNEMONIC_SCOPE}="modal"]`;
/**
 * Takes every letter while focus is in it, from its text fields too, if it has
 * letters of its own (see letteredForm).
 */
const FORM = 'form';

/**
 * The letters of the actions every item shares, so they are the same wherever
 * the action is. Rename has no button: its letter is on the title itself.
 */
export const MNEMONICS = {
  open: 'o',
  newChild: 'n',
  newWorkstream: 'w',
  addProject: 'a',
  startStop: 's',
  rename: 'r',
  delete: 'd',
  cancel: 'c',
  create: 'r',
  browse: 'b',
  keepFiles: 'k',
} as const;

/** The letters of the actions on a list item, which its scope always claims. */
export const ITEM_MNEMONICS: readonly string[] = [
  MNEMONICS.open,
  MNEMONICS.newChild,
  MNEMONICS.startStop,
  MNEMONICS.rename,
  MNEMONICS.delete,
];

/**
 * A letter for each of `labels` — its first one neither `taken` nor given to
 * an earlier label — for actions whose labels are not known ahead (the daemon
 * advertises them). Undefined for a label with no letter left.
 */
export function assignMnemonics(
  labels: readonly string[],
  taken: Iterable<string>,
): (string | undefined)[] {
  const used = new Set(taken);
  const letters: (string | undefined)[] = [];
  for (const label of labels) {
    const letter = [...label.toLowerCase()].find(
      (char) => /[a-z0-9]/.test(char) && !used.has(char),
    );
    if (letter) used.add(letter);
    letters.push(letter);
  }
  return letters;
}

/**
 * The props that make a container a scope: an `item` of the workstream list,
 * in reach while it is selected, the `page`'s own controls, always in reach,
 * or a `modal` one, alone in reach — what says a dialog is loading, so a
 * letter meant for it does not reach the page behind it.
 */
export function mnemonicScope(kind: 'item' | 'page' | 'modal') {
  return { [MNEMONIC_SCOPE]: kind };
}

/**
 * `keys`, in the notation of Stroke, and the mnemonic's last: Alt+letter, or
 * Alt+Shift+letter for one in upper case.
 */
function keysWith(keys: readonly string[], mnemonic: string | undefined) {
  if (!mnemonic) return keys;
  const lower = mnemonic.toLowerCase();
  return [...keys, lower === mnemonic ? `alt+${lower}` : `alt+shift+${lower}`];
}

/**
 * The props that give an element the mnemonic `letter`, and name it and the
 * other `keys` that press it to assistive technology.
 */
export function mnemonicProps(
  letter: string | undefined,
  keys: readonly string[] = [],
) {
  const all = keysWith(keys, letter);
  return {
    ...(letter && { [MNEMONIC]: letter }),
    ...(all.length > 0 && {
      'aria-keyshortcuts': all.map((key) => ariaKeys(key)).join(' '),
    }),
  };
}

/**
 * A tooltip naming its keys: `Rename this workstream (F2, Alt+R)`. `keys` are
 * in the notation of Stroke; the mnemonic, if any, is added as Alt+letter.
 */
export function withShortcuts(
  title: string | undefined,
  keys: readonly string[] = [],
  mnemonic?: string,
  mac = isMac(),
): string | undefined {
  const all = keysWith(keys, mnemonic);
  if (all.length === 0) return title;
  const shown = all.map((key) => formatKeys(key, mac)).join(', ');
  return title ? `${title} (${shown})` : shown;
}

/**
 * Where `letter` is marked in `label`: where a word starts with it, else its
 * first occurrence — the D of "Stop and delete", not of "and". -1 if absent.
 */
export function mnemonicIndex(label: string, letter: string): number {
  const lower = label.toLowerCase();
  for (
    let at = lower.indexOf(letter);
    at !== -1;
    at = lower.indexOf(letter, at + 1)
  ) {
    if (at === 0 || !/[a-z0-9]/i.test(lower.charAt(at - 1))) return at;
  }
  return lower.indexOf(letter);
}

/**
 * `label` with `letter` marked (see mnemonicIndex), to be underlined while
 * Alt is held, in one element so a flex container's gap does not split the
 * word. A label without the letter is returned as it is: the key still works,
 * and the tooltip says which it is.
 */
export function MnemonicLabel({
  label,
  letter,
}: {
  label: string;
  letter: string | undefined;
}): ReactNode {
  const at = letter ? mnemonicIndex(label, letter) : -1;
  if (at === -1) return label;
  return (
    <span>
      {label.slice(0, at)}
      <span className={styles.mnemonic}>{label.charAt(at)}</span>
      {label.slice(at + 1)}
    </span>
  );
}

function ownScope(element: Element): Element | null {
  return element.closest(`[${MNEMONIC_SCOPE}], ${EXCLUSIVE}, ${FORM}`);
}

/** The form around `element`, if it has a letter of its own. */
function letteredForm(element: Element): Element | null {
  const form = element.closest(FORM);
  return form &&
    Array.from(form.querySelectorAll(`[${MNEMONIC}]`)).some(
      (lettered) => ownScope(lettered) === form,
    )
    ? form
    : null;
}

function activeScopes(root: ParentNode): Element[] {
  const exclusive = root.querySelectorAll(EXCLUSIVE);
  if (exclusive.length > 0) return [exclusive[exclusive.length - 1]];
  const focused = document.activeElement;
  const form = focused && letteredForm(focused);
  if (form) return [form];
  const inList =
    !focused ||
    focused === document.body ||
    (focused.closest(`[${MNEMONIC_SCOPE}="item"]`) !== null &&
      focused.closest(FORM) === null);
  const selected = inList
    ? root.querySelector(`[${MNEMONIC_SCOPE}="item"][aria-current="true"]`)
    : null;
  return [
    ...(selected ? [selected] : []),
    ...Array.from(root.querySelectorAll(`[${MNEMONIC_SCOPE}="page"]`)),
  ];
}

function isInert(element: HTMLElement): boolean {
  return (
    element.matches(':disabled') ||
    element.getAttribute('aria-disabled') === 'true' ||
    element.closest('[hidden]') !== null
  );
}

/**
 * What each letter presses right now, from the active scopes innermost first:
 * a letter taken by an inner scope is not the page's to give out again.
 */
export function reachableMnemonics(
  root: ParentNode = document,
): Map<string, HTMLElement> {
  const reachable = new Map<string, HTMLElement>();
  // A letter an inner scope has, even on a disabled button, or that a selected
  // item answers to though it lacks the button: one the page must not take, or
  // Alt+R on a card that cannot be renamed would rename the project.
  const claimed = new Set<string>();
  for (const scope of activeScopes(root)) {
    for (const element of Array.from(
      scope.querySelectorAll<HTMLElement>(`[${MNEMONIC}]`),
    )) {
      const letter = element.getAttribute(MNEMONIC) ?? '';
      if (ownScope(element) !== scope || claimed.has(letter)) continue;
      claimed.add(letter);
      if (!isInert(element)) reachable.set(letter, element);
    }
    if (scope.getAttribute(MNEMONIC_SCOPE) === 'item') {
      for (const letter of ITEM_MNEMONICS) claimed.add(letter);
    }
  }
  return reachable;
}

/**
 * The letter or digit a key types, as on its cap in any layout; where Alt makes
 * it type something else (a Mac's Option, a non-Latin layout), the one at its
 * place on a US keyboard.
 */
function letterOf(event: KeyboardEvent): string | undefined {
  if (/^[a-z0-9]$/i.test(event.key)) return event.key.toLowerCase();
  return /^(?:Key|Digit)([A-Z0-9])$/.exec(event.code)?.[1].toLowerCase();
}

/**
 * Answers Alt for the whole page: while it is held, the letters in reach are
 * underlined, and Alt+letter presses the button it reaches — Alt+Shift+letter
 * the one whose letter is in upper case. AltGr (which reports Ctrl as well) is
 * left alone, and so is a text field outside a dialog or a form with letters
 * — and on a Mac inside one too, where Option types characters.
 */
export function useMnemonics() {
  useEffect(() => {
    const hide = () => {
      for (const element of Array.from(
        document.querySelectorAll(`[${SHOWN}]`),
      )) {
        element.removeAttribute(SHOWN);
      }
    };
    const show = () => {
      hide();
      for (const element of reachableMnemonics().values()) {
        element.setAttribute(SHOWN, '');
      }
    };
    const mac = isMac();
    const inReach = (event: KeyboardEvent) => {
      const target = event.target as HTMLElement;
      return (
        !isTextEntry(target) ||
        (!mac &&
          (target.closest(MODAL) !== null || letteredForm(target) !== null))
      );
    };

    const onKeyDown = (event: KeyboardEvent) => {
      const alt = event.altKey && !event.ctrlKey && !event.metaKey;
      if (event.key === 'Alt' || event.key === 'Shift') {
        if (event.repeat) return;
        if (alt && inReach(event)) show();
        else hide();
        return;
      }
      if (!alt || event.repeat || event.isComposing || !inReach(event)) {
        hide();
        return;
      }
      const letter = letterOf(event);
      const target = letter
        ? reachableMnemonics().get(
            event.shiftKey ? letter.toUpperCase() : letter,
          )
        : undefined;
      if (!target) return;
      event.preventDefault();
      event.stopPropagation();
      hide();
      const taken = !target.dispatchEvent(
        new CustomEvent(MNEMONIC_EVENT, { cancelable: true }),
      );
      if (!taken) target.click();
    };
    const onKeyUp = (event: KeyboardEvent) => {
      if (event.key === 'Alt') hide();
    };

    // Capture, so a letter taken here never reaches the focused element too.
    window.addEventListener('keydown', onKeyDown, true);
    window.addEventListener('keyup', onKeyUp, true);
    window.addEventListener('blur', hide);
    return () => {
      window.removeEventListener('keydown', onKeyDown, true);
      window.removeEventListener('keyup', onKeyUp, true);
      window.removeEventListener('blur', hide);
      hide();
    };
  }, []);
}
