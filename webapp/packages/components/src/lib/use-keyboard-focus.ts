import { type FocusEvent, useRef } from 'react';

/**
 * Tracks whether the keyboard, rather than a click, last focused the element
 * the returned handlers are spread on. A click on a card's blank area or a
 * row's output focuses it too, but draws no focus ring, so its own keys must
 * not then act on something the user cannot see is selected.
 */
export function useKeyboardFocus() {
  const pointerDown = useRef(false);
  const byKeyboard = useRef(false);
  return {
    focusedByKeyboard: () => byKeyboard.current,
    focusHandlers: {
      onPointerDown: () => {
        pointerDown.current = true;
      },
      // A focus that moves into a descendant ends the click's claim too.
      onFocus: (event: FocusEvent) => {
        if (event.target === event.currentTarget) {
          byKeyboard.current = !pointerDown.current;
        }
        pointerDown.current = false;
      },
      // A click on the element while it already had focus focuses nothing.
      onBlur: (event: FocusEvent) => {
        if (event.target === event.currentTarget) pointerDown.current = false;
      },
    },
  };
}
