/** Titles `element` with `text` while its text is cut off, else untitles it. */
function syncTitle(element: HTMLElement, text: string) {
  if (element.scrollWidth > element.clientWidth) element.title = text;
  else element.removeAttribute('title');
}

/**
 * Props for an element that ellipsizes `text`: as the pointer reaches it, it
 * titles itself with the text in full, but only while the text is cut off, so
 * one that fits shows no tooltip and a screen reader does not read it twice.
 * Text that changes under the pointer, or under a title it left, is measured
 * again, so the title is never of text no longer shown.
 */
export function titleWhenTruncated(text: string) {
  return {
    ref: (element: HTMLElement | null) => {
      if (
        element &&
        element.title !== text &&
        (element.hasAttribute('title') || element.matches(':hover'))
      ) {
        syncTitle(element, text);
      }
    },
    onPointerEnter: (event: { currentTarget: HTMLElement }) =>
      syncTitle(event.currentTarget, text),
  };
}
