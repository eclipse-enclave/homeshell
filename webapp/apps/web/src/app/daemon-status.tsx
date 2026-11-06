import styles from './daemon-status.module.css';
import type { DaemonConnection } from './use-daemon-state';
import { useDaemonVersion } from './use-daemon-version';

export interface DaemonStatusProps {
  /** Connection state of the SSE stream (see useDaemonState). */
  connection: DaemonConnection;
}

/**
 * Live daemon connection indicator: liveness comes from the event stream, the
 * version from GET /api/version (re-read on every reconnect). When a newer
 * release is available it offers an update button that triggers the self-update
 * flow; the daemon restarts and the SSE stream reconnects to the new version.
 */
export function DaemonStatus({ connection }: DaemonStatusProps) {
  // Re-read the version whenever the connection state changes so a post-update
  // reconnect surfaces the new version automatically.
  const { info, phase, update } = useDaemonVersion(connection);
  const version = info?.current ?? null;

  const label = {
    connecting: 'Connecting to daemon',
    connected: 'Daemon connected',
    offline: 'Daemon unreachable',
  }[connection];

  return (
    <span className={styles.status}>
      <span
        className={`${styles.dot} ${styles[connection]}`}
        role="img"
        aria-label={label}
        title={label}
      />
      <span className={styles.text}>
        {connection === 'connected' && `daemon ${version ?? '…'}`}
        {connection === 'offline' && 'daemon offline'}
        {connection === 'connecting' && 'connecting…'}
      </span>
      {connection === 'connected' && info?.updateAvailable && (
        <button
          type="button"
          className={styles.update}
          onClick={update}
          disabled={phase === 'updating'}
          title={`Update to ${info.latest}`}
        >
          {phase === 'updating'
            ? 'Updating…'
            : phase === 'error'
              ? 'Update failed — retry'
              : `Update to ${info.latest}`}
        </button>
      )}
    </span>
  );
}

export default DaemonStatus;
