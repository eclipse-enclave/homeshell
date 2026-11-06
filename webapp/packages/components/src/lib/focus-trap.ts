import type { KeyboardEvent } from 'react';

const FOCUSABLE = 'button, input, select, textarea, a[href], [tabindex]';

/**
 * Keeps Tab and Shift+Tab inside a modal `container`, wrapping at either end —
 * also from the container itself, which holds focus while nothing inside does.
 */
export function trapTab(event: KeyboardEvent, container: HTMLElement) {
  const focusable = Array.from(
    container.querySelectorAll<HTMLElement>(FOCUSABLE),
  ).filter((element) => !element.matches(':disabled') && element.tabIndex >= 0);
  if (focusable.length === 0) return;
  const first = focusable[0];
  const last = focusable[focusable.length - 1];
  const active = document.activeElement;
  const edge = event.shiftKey ? first : last;
  if (active === edge || active === container) {
    event.preventDefault();
    (event.shiftKey ? last : first).focus();
  }
}

/**
 * The keys every modal answers alike: none pressed inside it goes further, so
 * none acts on what it was opened over; Escape closes it and Tab stays inside
 * `event.currentTarget`. Each key it acts on is taken. Says whether it handled
 * the key. Keys an input method is composing are left alone.
 */
export function handleModalKeys(
  event: KeyboardEvent<HTMLElement>,
  onClose: () => void,
): boolean {
  event.stopPropagation();
  if (event.nativeEvent.isComposing) return true;
  if (event.key === 'Escape') {
    event.preventDefault();
    onClose();
    return true;
  }
  if (event.key === 'Tab') {
    trapTab(event, event.currentTarget);
    return true;
  }
  return false;
}
