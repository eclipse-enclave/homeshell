/**
 * Asks the daemon to deliver a sample notification (`POST
 * /api/notifications/test`), so picking a delivery style on the settings page
 * shows what that style actually looks like.
 *
 * The daemon does the delivering, not this client: the OS-native channel is a
 * platform command only the daemon can run, and the in-app channel is its own
 * SSE push. A toast faked here would demonstrate the client rather than the
 * setting. The style to demonstrate is passed explicitly because the pick has
 * only just been made — the debounced write of it may not have landed yet.
 *
 * Best-effort, like the sound preview: a failed test is a demonstration that
 * did not happen, never an error the user has to clear.
 */
export async function sendTestNotification(style: string): Promise<void> {
  try {
    await fetch('/api/notifications/test', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ style }),
    });
  } catch {
    // The daemon is unreachable; the settings page already says so.
  }
}
