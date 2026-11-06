import type { DaemonConnection } from './use-daemon-state';

/**
 * Why the daemon-backed actions are unavailable right now, or `undefined` when
 * they are fine. Every control that would call `/api` is disabled on this one
 * value, so there is a single answer to "can we act?" instead of each view
 * deciding for itself.
 *
 * Anything but a live connection blocks. `offline` is the obvious case; the
 * initial `connecting` window blocks too, because the daemon has not answered yet
 * and a request fired into it would fail exactly the same way. (`useDaemonState`
 * only reports `connecting` before the first snapshot — a later drop goes to
 * `offline` — so this costs nothing after startup.)
 *
 * The string is the reason *and* the tooltip: the components take a
 * `disabledReason` rather than a transport state, which keeps them
 * transport-agnostic and makes a disabled control unable to exist without an
 * explanation attached.
 */
export function offlineReason(
  connection: DaemonConnection,
): string | undefined {
  switch (connection) {
    case 'connected':
      return undefined;
    case 'connecting':
      return 'Connecting to the daemon…';
    default:
      return 'The daemon is offline — HomeShell cannot act until it reconnects.';
  }
}
