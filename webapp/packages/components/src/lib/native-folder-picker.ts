/**
 * Asks the daemon to open the host desktop's own folder chooser.
 *
 * The daemon runs on the same machine as the person clicking Browse in every
 * deployment HomeShell is built for, so its chooser is *their* chooser — with
 * the places sidebar, path typing and keyboard handling the in-app picker
 * (GET /api/browse) cannot reproduce. The daemon refuses on a public bind,
 * where that assumption does not hold, and that refusal arrives here as
 * `unavailable` like any other.
 *
 * Every outcome that is not a path leads back to the in-app picker, so a host
 * with no dialog tool installed loses nothing.
 */
export type NativeFolderResult =
  | { outcome: 'selected'; path: string }
  | { outcome: 'cancelled' }
  | { outcome: 'unavailable' };

/**
 * Opens the native chooser and resolves once the user answers it. The promise
 * stays pending for as long as the dialog is up — the daemon holds the request
 * open — so callers should show the Browse control as busy meanwhile.
 *
 * @param startPath where the dialog should open; a hint the host may ignore.
 * @param signal aborts the request, which closes the dialog: the daemon runs the
 * helper on the request's context, so dropping the request kills it. Without one
 * a form that unmounts mid-dialog leaves the chooser on the host's screen with
 * nobody waiting for it.
 */
export async function pickFolderNatively(
  startPath?: string,
  signal?: AbortSignal,
): Promise<NativeFolderResult> {
  try {
    const response = await fetch('/api/browse/native', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ path: startPath ?? '' }),
      signal,
    });
    if (!response.ok) return { outcome: 'unavailable' };

    const body = (await response.json()) as {
      path?: string;
      cancelled?: boolean;
    };
    if (body.cancelled) return { outcome: 'cancelled' };
    if (typeof body.path === 'string' && body.path) {
      return { outcome: 'selected', path: body.path };
    }
    return { outcome: 'unavailable' };
  } catch {
    // An abort lands here too, and it means the caller is gone — nothing to
    // report. A daemon that went away mid-dialog is not something the user can
    // act on here either; the in-app picker reports that outage in its own words.
    return { outcome: 'unavailable' };
  }
}
