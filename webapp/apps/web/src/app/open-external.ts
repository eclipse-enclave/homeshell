/**
 * Opens a URL outside the app — today, the web IDE the daemon resolves for a
 * workstream — in whichever way this client can.
 *
 * `window.open` alone is not enough, and the two ways it fails look identical
 * from here: nothing happens.
 *
 * - The desktop shell's system webview blocks it outright. There is no second
 *   window for it to open and no hook in webview_go to give it one, so it
 *   returns null every time.
 * - A real browser blocks it whenever the call is not attributed to a user
 *   gesture — which is the case here, because the URL is only known *after*
 *   awaiting the daemon's open response.
 *
 * So when `window.open` comes back empty, ask the daemon to hand the URL to the
 * host's default browser (POST /api/open-url, the same os/exec hand-off it
 * already uses for terminals, editors and the workstream note). The desktop
 * shell and the browser then behave the same, which is the point.
 *
 * Returns an error message for the caller to show, or null when the URL was
 * opened.
 */
export async function openExternal(url: string): Promise<string | null> {
  // Deliberately no `noopener` in the feature string. Per spec it makes
  // `window.open` return null *even when the popup opens*, which turns the check
  // below into dead code: every successful open would fall through to the daemon
  // and launch the same URL a second time in the host's browser. Detecting the
  // failure is the whole job here, so the return value has to mean something —
  // and what `noopener` protects against is undone a line later by clearing
  // `opener` on the window we got back, before the new page can run.
  const opened = window.open(url, '_blank');
  if (opened) {
    opened.opener = null;
    return null;
  }

  try {
    const response = await fetch('/api/open-url', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ url }),
    });
    if (response.ok) return null;
    const body = (await response.json().catch(() => null)) as {
      error?: string;
    } | null;
    return body?.error ?? `Could not open ${url}.`;
  } catch {
    return 'Could not reach the daemon.';
  }
}
