import { useEffect, useLayoutEffect, useRef, useState } from 'react';

/** Where focus can go when it is not to be lost to <body>. */
export type FocusTarget = () => HTMLElement | null | undefined;

/** `origin` while it can still take focus, else what `fallback` returns. */
function returnTarget(origin: Element | null, fallback?: FocusTarget) {
  return origin instanceof HTMLElement &&
    origin !== document.body &&
    origin.isConnected
    ? origin
    : fallback?.();
}

/**
 * A modal's focus: on opening it focuses what `initial` returns, and on closing
 * it gives focus back to what had it before — or to what `fallback` returns
 * when that has left the page meanwhile (a menu item, say, whose menu closed as
 * the modal opened). Both are read once, when it opens.
 *
 * A layout effect, so on close focus is back before whatever the close mounts
 * (a form's autofocus, say) takes it in its own effects, and on open after a
 * child's autoFocus, which would otherwise be taken for the focus to restore.
 */
export function useModalFocus(initial: FocusTarget, fallback?: FocusTarget) {
  useFocusWhileOpen(initial, fallback, false);
}

/**
 * A pane's focus: as a modal's, but closing gives focus back only while it is
 * still in the pane, or lost to <body>. Focus the user took elsewhere meanwhile
 * stays there, as a pane is not modal.
 */
export function usePaneFocus(pane: FocusTarget) {
  useFocusWhileOpen(pane, undefined, true);
}

function useFocusWhileOpen(
  initial: FocusTarget,
  fallback: FocusTarget | undefined,
  onlyFromWithin: boolean,
) {
  const targets = useRef({ initial, fallback });
  useLayoutEffect(() => {
    const origin = document.activeElement;
    const opened = targets.current.initial();
    opened?.focus();
    const { fallback } = targets.current;
    return () => {
      const active = document.activeElement;
      if (onlyFromWithin && active !== document.body && !opened?.contains(active)) {
        return;
      }
      returnTarget(origin, fallback)?.focus();
    };
  }, [onlyFromWithin]);
}

/**
 * Focuses what had focus when `open` turned true — or what `fallback` returns
 * when that has left the page — once it turns false again, if asked to: leaving
 * a rename box or an inline form by keyboard takes the focus inside it away
 * with it, and focus would otherwise fall to <body>. A blur-commit does not
 * ask. Returns the setter for the request.
 */
export function useReturnFocus(open: boolean, fallback: FocusTarget) {
  const origin = useRef<Element | null>(null);
  const [requested, setRequested] = useState(false);

  useLayoutEffect(() => {
    if (open) origin.current = document.activeElement;
  }, [open]);

  // Runs after the element is back in the DOM, covering an async commit.
  useEffect(() => {
    if (open || !requested) return;
    returnTarget(origin.current, fallback)?.focus();
    setRequested(false);
  }, [open, requested, fallback]);

  return setRequested;
}
