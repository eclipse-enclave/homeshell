import { formatKeys, isMac, type KeyHelp, platformKeys } from '@homeshell/components';
import type { Keybinding } from './keymap';

/**
 * Every keyboard shortcut HomeShell answers, in one place: the keymap, the
 * command palette and the shortcut reference all read them from here. A
 * command's label is fixed, unlike the palette's, which can name what the
 * command acts on.
 */
export const BOUND_COMMANDS = [
  {
    command: 'showCommands',
    label: 'Show all commands',
    keys: ['mod+shift+p', 'f1'],
  },
  {
    command: 'quickOpen',
    label: 'Go to a project, workstream or session',
    keys: ['mod+p'],
  },
  {
    command: 'openSettings',
    label: 'Open settings',
    keys: ['mod+,'],
  },
  {
    command: 'showShortcuts',
    label: 'Show keyboard shortcuts',
    keys: ['mod+k mod+s', '?'],
  },
  {
    command: 'newSession',
    label: 'New session in the selected workstream',
    // ⇧⌘` cycles a Mac's windows, so it is ⌃⇧` there.
    keys: ['mod+shift+`'],
    mac: ['ctrl+shift+`'],
  },
] as const satisfies readonly (KeyHelp & Keybinding)[];

/** The ID of a command that has keys. */
export type BoundCommandId = (typeof BOUND_COMMANDS)[number]['command'];

/** The fixed label of the bound command `id`. */
export function labelOf(id: BoundCommandId): string {
  return BOUND_COMMANDS.find((bound) => bound.command === id)?.label ?? id;
}

/** The keys `bindings` bind to `commandId`, each formatted for the platform. */
export function keysFor(
  commandId: string,
  mac = isMac(),
  bindings: readonly Keybinding[] = BOUND_COMMANDS,
): string[] {
  return bindings
    .filter((binding) => binding.command === commandId)
    .flatMap((binding) => platformKeys(binding, mac))
    .map((keys) => formatKeys(keys, mac));
}
