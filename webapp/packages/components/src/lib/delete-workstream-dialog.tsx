import type { Workstream } from '@homeshell/model';
import { Button } from './button';
import { MNEMONICS } from './mnemonic';
import { ConfirmDialog } from './confirm-dialog';
import styles from './delete-workstream-dialog.module.css';

/** Options passed back to the caller when a deletion is confirmed. */
export interface DeleteWorkstreamOptions {
  keepFiles: boolean;
}

export interface DeleteWorkstreamDialogProps {
  workstream: Workstream;
  onConfirm: (opts: DeleteWorkstreamOptions) => void;
  onCancel: () => void;
}

/** Whether the strategy materializes files outside the project directory. */
export function hasMaterializedFiles(strategyId: string): boolean {
  return strategyId === 'worktree' || strategyId === 'copy';
}

/**
 * Confirmation dialog for deleting a workstream. The options shown depend on
 * the workstream's filesystem strategy:
 *
 * - **in place**: Delete only removes the workstream from HomeShell; no files
 *   are affected. Buttons: [Cancel] [Delete].
 * - **copy / worktree**: The materialized directory can be kept or removed.
 *   Buttons: [Cancel] [Keep files] [Delete].
 *
 * It opens on Cancel, so Enter as it opens deletes nothing.
 */
export function DeleteWorkstreamDialog({
  workstream,
  onConfirm,
  onCancel,
}: DeleteWorkstreamDialogProps) {
  const materialized = hasMaterializedFiles(workstream.strategyId);

  const handleDelete = () => {
    onConfirm({ keepFiles: false });
  };

  const handleKeepFiles = () => {
    onConfirm({ keepFiles: true });
  };

  return (
    <ConfirmDialog
      title={`Delete "${workstream.name}"?`}
      onClose={onCancel}
      actions={
        <>
          <Button onClick={onCancel} mnemonic={MNEMONICS.cancel}>
            Cancel
          </Button>
          {materialized && (
            <Button onClick={handleKeepFiles} mnemonic={MNEMONICS.keepFiles}>
              Keep files
            </Button>
          )}
          <Button
            variant="danger"
            onClick={handleDelete}
            mnemonic={MNEMONICS.delete}
          >
            Delete
          </Button>
        </>
      }
    >
      {materialized ? (
        <p className={styles.description}>
          This workstream has a <strong>{workstream.strategy}</strong> directory
          {workstream.branch ? (
            <>
              {' '}
              on branch <strong>{workstream.branch}</strong>
            </>
          ) : null}
          . You can keep the files on disk or delete everything.
        </p>
      ) : (
        <p className={styles.description}>
          This will delete the workstream from HomeShell. No files are affected.
        </p>
      )}
    </ConfirmDialog>
  );
}

export default DeleteWorkstreamDialog;
