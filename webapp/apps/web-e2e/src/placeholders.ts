/**
 * Recognizing the placeholders the daemon projects while a create is in flight:
 * `creating-` for a workstream or session, `starting-` for a session coming up.
 */

/** Whether a workstream ID is a creation placeholder. */
export function isPlaceholderWorkstream(id: string): boolean {
  return String(id).startsWith('creating-');
}

/** Whether a session ID is a placeholder rather than a session that exists. */
export function isPlaceholderSession(id: string): boolean {
  return (
    String(id).startsWith('creating-sess-') ||
    String(id).startsWith('starting-')
  );
}

/** Whether a session ID is the starting placeholder specifically. */
export function isStartingSession(id: string): boolean {
  return String(id).startsWith('starting-');
}
