import {
  Button,
  ConfirmDialog,
  FOLDER_PICKER_KEYS,
  formatKeys,
  Kbd,
  type KeyHelp,
  LIST_KEYS,
  platformKeys,
  tabStopItem,
} from '@homeshell/components';
import { BOUND_COMMANDS } from './keybindings';
import styles from './shortcuts-dialog.module.css';

interface Row {
  label: string;
  keys: string[];
}

function ShortcutTable({ caption, rows }: { caption: string; rows: Row[] }) {
  return (
    <table className={styles.table}>
      <caption className={styles.caption}>{caption}</caption>
      <tbody>
        {rows.map((row) => (
          <tr key={row.label}>
            <th scope="row" className={styles.label}>
              {row.label}
            </th>
            <td className={styles.keys}>
              {row.keys.map((keys) => (
                <Kbd key={keys}>{keys}</Kbd>
              ))}
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

/** A row of the reference: what the keys do, and the keys as they read here. */
function rowOf(help: KeyHelp): Row {
  return {
    label: help.label,
    keys: platformKeys(help).map((keys) => formatKeys(keys)),
  };
}

/** Keys that run no command: Escape, and a leading `>` in Go to. */
const UNBOUND_ROWS: Row[] = [
  { label: 'Close a dialog, palette or menu', keys: [formatKeys('escape')] },
  { label: 'In Go to, list the commands instead', keys: ['>'] },
];

/** The keyboard shortcut reference, built from the keybinding table itself. */
export function ShortcutsDialog({ onClose }: { onClose: () => void }) {
  const anywhere = [...BOUND_COMMANDS.map(rowOf), ...UNBOUND_ROWS];
  const listRows = LIST_KEYS.map(rowOf);
  const pickerRows = FOLDER_PICKER_KEYS.map(rowOf);

  return (
    <ConfirmDialog
      title="Keyboard shortcuts"
      onClose={onClose}
      actions={<Button onClick={onClose}>Close</Button>}
      fallbackFocus={() => tabStopItem(document)}
    >
      <ShortcutTable caption="Anywhere" rows={anywhere} />
      <ShortcutTable caption="In the workstream list" rows={listRows} />
      <ShortcutTable caption="In the folder picker" rows={pickerRows} />
    </ConfirmDialog>
  );
}
