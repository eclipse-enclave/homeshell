/**
 * Copies text to the system clipboard, working in both places the UI runs.
 *
 * The async Clipboard API is not the universal thing it looks like: the desktop
 * shell's system webview (WebKitGTK) does not expose `navigator.clipboard` at
 * all, even on the daemon's loopback origin, which counts as a secure context
 * there. A component that reaches for it directly throws in the desktop app
 * while working perfectly in a browser tab — the copy button simply does
 * nothing, with no error the user can see.
 *
 * So try the modern API, then fall back to a selected off-screen textarea and
 * `document.execCommand('copy')`, which the webview does support. The fallback
 * is also what covers a browser that refuses the API because the copy was not
 * driven by a user gesture it recognises.
 *
 * Returns whether the text made it to the clipboard, so callers can show the
 * copied-confirmation only when there is something to confirm.
 */
export async function copyText(text: string): Promise<boolean> {
  if (typeof navigator !== 'undefined' && navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch {
      // Permission denied, or no gesture the browser trusts. Fall through:
      // execCommand is judged by different rules and often still works.
    }
  }
  return copyWithExecCommand(text);
}

/**
 * The pre-Clipboard-API copy: put the text in a textarea, select it, and let
 * the document copy the selection.
 */
function copyWithExecCommand(text: string): boolean {
  if (typeof document === 'undefined') return false;

  const textarea = document.createElement('textarea');
  textarea.value = text;
  // Off-screen rather than hidden: `display:none` and `visibility:hidden`
  // elements cannot hold a selection, so the copy would silently produce
  // nothing. `readOnly` keeps the mobile keyboard from appearing.
  textarea.setAttribute('readonly', '');
  textarea.style.position = 'fixed';
  textarea.style.top = '-9999px';
  textarea.style.opacity = '0';
  document.body.appendChild(textarea);

  const previousSelection = document.getSelection()?.rangeCount
    ? document.getSelection()?.getRangeAt(0)
    : undefined;

  try {
    textarea.select();
    textarea.setSelectionRange(0, textarea.value.length);
    return document.execCommand('copy');
  } catch {
    return false;
  } finally {
    document.body.removeChild(textarea);
    // Restore whatever the user had selected; copying a path should not clear
    // the log lines they were half-way through selecting.
    if (previousSelection) {
      const selection = document.getSelection();
      selection?.removeAllRanges();
      selection?.addRange(previousSelection);
    }
  }
}
