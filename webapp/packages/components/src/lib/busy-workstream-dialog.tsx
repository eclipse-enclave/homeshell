import type { BusySession } from '@homeshell/model';
import { stateLabel } from '@homeshell/model';
import { Button } from './button';
import { MNEMONICS } from './mnemonic';
import styles from './busy-workstream-dialog.module.css';
import { ConfirmDialog } from './confirm-dialog';

export interface BusyWorkstreamDialogProps {
  /** Workstream the user tried to remove, named in the title. */
  workstreamName: string;
  /** The busy sessions, exactly as the daemon reported them. */
  sessions: BusySession[];
  /** Re-issues the removal with force, stopping those sessions. */
  onConfirm: () => void;
  onCancel: () => void;
}

/**
 * Second-stage confirmation for removing a workstream the daemon refused to
 * remove because sessions are still busy.
 *
 * It exists to turn a daemon **rejection** into a choice, and is deliberately
 * distinct from `DeleteWorkstreamDialog` (which asks about *files*, before
 * anything is sent, and can be switched off with the `confirmRemove` setting):
 * this one is not a preference, it is the only path past a guard that no client
 * can wave away — the removal is re-issued with force or not at all.
 */
export function BusyWorkstreamDialog({
  workstreamName,
  sessions,
  onConfirm,
  onCancel,
}: BusyWorkstreamDialogProps) {
  const count = sessions.length;
  return (
    <ConfirmDialog
      title={`"${workstreamName}" has ${count === 1 ? 'a session' : `${count} sessions`} still busy`}
      onClose={onCancel}
      actions={
        <>
          {/* It opens whenever the daemon answers, so a key already on its way
              must not stop the sessions. */}
          <Button
            onClick={onCancel}
            mnemonic={MNEMONICS.cancel}
            data-initial-focus
          >
            Cancel
          </Button>
          <Button
            variant="danger"
            onClick={onConfirm}
            mnemonic={MNEMONICS.delete}
          >
            Stop and delete
          </Button>
        </>
      }
    >
      <p className={styles.description}>
        Deleting the workstream will stop{' '}
        {count === 1 ? 'this session' : 'these sessions'} and lose any work that
        is not saved:
      </p>
      <ul className={styles.sessions}>
        {sessions.map((session) => (
          <li key={session.id} className={styles.session}>
            {session.title || session.id}{' '}
            <span className={styles.state}>({stateLabel(session.state)})</span>
          </li>
        ))}
      </ul>
    </ConfirmDialog>
  );
}

export default BusyWorkstreamDialog;
