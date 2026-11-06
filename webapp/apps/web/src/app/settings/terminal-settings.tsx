import { useEffect, useState } from 'react';
import { Select, TextField, type SelectOption } from '@homeshell/components';
import { useSettings } from './use-settings';
import type { Settings } from './settings-types';
import { useTerminalEmulators } from './use-terminal-emulators';

type TerminalChoice = Settings['terminal'];

const AUTO = 'auto';
const CUSTOM = 'custom';
const COMMAND_PLACEHOLDER = '{command}';

/** Whether the daemon would accept this choice: a custom template must say where the command goes. */
function isSavable(choice: TerminalChoice): boolean {
  return choice.emulator !== CUSTOM || choice.command.includes(COMMAND_PLACEHOLDER);
}

export interface TerminalSettingsProps {
  /** The daemon's rejection of the emulator, if the last save failed on it. */
  emulatorError?: string;
  /** The daemon's rejection of the custom command, if the last save failed on it. */
  commandError?: string;
}

/**
 * The "Terminal" settings category: which emulator the daemon opens terminal
 * windows in. A custom choice stays local until its template is complete, so a
 * half-typed one is never sent to be rejected and rolled back.
 */
export function TerminalSettings({ emulatorError, commandError }: TerminalSettingsProps) {
  const { settings, setSection } = useSettings();
  const { value: list, failed } = useTerminalEmulators();
  const emulators = list?.emulators ?? [];
  const noneAutomatic = list !== null && !list.automatic;
  const { emulator, command } = settings.terminal;

  const [draft, setDraft] = useState<TerminalChoice>({ emulator, command });
  // The daemon stores the template trimmed; echoing that back mid-typing must
  // not strip the space just typed.
  useEffect(
    () =>
      setDraft((d) =>
        d.emulator === emulator && d.command.trim() === command ? d : { emulator, command }
      ),
    [emulator, command]
  );

  const updateDraft = (patch: Partial<TerminalChoice>) => {
    const next = { ...draft, ...patch };
    setDraft(next);
    if (isSavable(next)) setSection('terminal', next);
  };

  const options: SelectOption[] = [
    {
      value: AUTO,
      label: list?.automatic
        ? `Automatic (${list.automatic})`
        : noneAutomatic
          ? 'Automatic (none installed)'
          : 'Automatic',
    },
    ...emulators.map((e) => ({
      value: e.id,
      label: e.installed ? e.label : `${e.label} (not installed)`,
    })),
    { value: CUSTOM, label: 'Custom command' },
  ];
  // Until the list arrives (or when it cannot), the stored choice still has to
  // read as itself rather than as the first option.
  if (!options.some((option) => option.value === draft.emulator)) {
    options.splice(1, 0, { value: draft.emulator, label: draft.emulator });
  }

  return (
    <>
      <Select
        label="Terminal emulator"
        value={draft.emulator}
        options={options}
        onChange={(value) => updateDraft({ emulator: value })}
        hint={
          noneAutomatic
            ? 'None of the emulators listed here is installed, and $TERMINAL names none on the PATH, so Automatic has nothing to open. Install one, or choose Custom command.'
            : 'Automatic uses $TERMINAL if it names an installed emulator, else the first installed one listed here; on macOS, Terminal.'
        }
        error={
          emulatorError ??
          (failed
            ? 'Could not load the emulators from the daemon, so only Automatic and Custom are listed.'
            : undefined)
        }
      />
      {draft.emulator === CUSTOM && (
        <TextField
          label="Custom terminal command"
          value={draft.command}
          onChange={(value) => updateDraft({ command: value })}
          placeholder={`e.g. alacritty -e ${COMMAND_PLACEHOLDER}`}
          hint={`A shell line. ${COMMAND_PLACEHOLDER} expands to the command to run, already quoted. Do not wrap it in quotes. The window is untitled.`}
          error={
            isSavable(draft)
              ? commandError
              : `Not saved until it includes ${COMMAND_PLACEHOLDER}.`
          }
        />
      )}
    </>
  );
}
