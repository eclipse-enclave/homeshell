import { Button, copyText } from '@homeshell/components';
import { useId, useRef, useState } from 'react';

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
  const { info, phase, failure, update } = useDaemonVersion(connection);
  const detailsId = useId();
  const reasonId = `${detailsId}-reason`;
  const toggleRef = useRef<HTMLButtonElement>(null);
  const [detailsOpen, setDetailsOpen] = useState(false);
  const [copied, setCopied] = useState(false);
  // Each new failure opens its reason (state adjusted during render, not in an effect).
  const [shownFailure, setShownFailure] = useState(failure);
  if (failure !== shownFailure) {
    setShownFailure(failure);
    setDetailsOpen(failure !== null);
    setCopied(false);
  }
  const version = info?.current ?? null;
  // A rolling build is a commit, which reads as a typo without the channel.
  const channel = info?.channel === 'rolling' ? ' (rolling)' : '';

  const label = {
    connecting: 'Connecting to daemon',
    connected: 'Daemon connected',
    offline: 'Daemon unreachable',
  }[connection];

  const shownError =
    connection === 'connected' && phase === 'error' ? failure : null;
  const showDetails = shownError !== null && detailsOpen;

  const closeDetails = () => {
    setDetailsOpen(false);
    toggleRef.current?.focus();
  };

  return (
    <span className={styles.status}>
      <span
        className={`${styles.dot} ${styles[connection]}`}
        role="img"
        aria-label={label}
        title={label}
      />
      <span className={styles.text}>
        {connection === 'connected' && `daemon ${version ?? '…'}${channel}`}
        {connection === 'offline' && 'daemon offline'}
        {connection === 'connecting' && 'connecting…'}
      </span>
      {connection === 'connected' &&
        info?.updateAvailable &&
        shownError?.retryable !== false && (
          <button
            type="button"
            className={styles.update}
            onClick={update}
            disabled={phase === 'updating'}
            title={`Update to ${info.latest}`}
            aria-describedby={showDetails ? reasonId : undefined}
          >
            {phase === 'updating'
              ? 'Updating…'
              : phase === 'error'
                ? 'Update failed — retry'
                : `Update to ${info.latest}`}
          </button>
        )}
      {shownError && (
        <button
          ref={toggleRef}
          type="button"
          className={styles.update}
          aria-label={
            shownError.retryable ? 'Update failure details' : undefined
          }
          aria-expanded={detailsOpen}
          aria-controls={showDetails ? detailsId : undefined}
          onClick={() => setDetailsOpen((open) => !open)}
        >
          {shownError.retryable ? 'Details' : 'Update needs manual steps'}
        </button>
      )}
      {showDetails && (
        <div id={detailsId} className={styles.details} role="alert">
          <p id={reasonId} className={styles.reason}>
            {shownError.reason}
          </p>
          <div className={styles.actions}>
            <Button
              onClick={() =>
                copyText(shownError.reason).then((ok) => setCopied(ok))
              }
            >
              {copied ? 'Copied' : 'Copy'}
            </Button>
            <Button onClick={closeDetails}>Close</Button>
          </div>
        </div>
      )}
    </span>
  );
}

export default DaemonStatus;
