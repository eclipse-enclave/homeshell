import type { ReactNode } from 'react';
import { Button } from './button';
import { ConfirmDialog } from './confirm-dialog';
import { MNEMONICS } from './mnemonic';

/**
 * A confirmation for one of the row's actions. Focus starts on Cancel, so
 * pressing the action's letter and then Enter does nothing by accident.
 */
export function ActionConfirm({
  title,
  confirmLabel,
  mnemonic,
  danger,
  onConfirm,
  onClose,
  children,
}: {
  title: string;
  confirmLabel: string;
  mnemonic?: string;
  danger?: boolean;
  onConfirm: () => void;
  onClose: () => void;
  children: ReactNode;
}) {
  return (
    <ConfirmDialog
      title={title}
      onClose={onClose}
      actions={
        <>
          <Button
            onClick={onClose}
            mnemonic={MNEMONICS.cancel}
            data-initial-focus
          >
            Cancel
          </Button>
          <Button
            variant={danger ? 'danger' : undefined}
            mnemonic={mnemonic}
            onClick={() => {
              onClose();
              onConfirm();
            }}
          >
            {confirmLabel}
          </Button>
        </>
      }
    >
      {children}
    </ConfirmDialog>
  );
}
