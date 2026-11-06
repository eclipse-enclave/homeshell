import type { SelectOption } from '@homeshell/components';

/**
 * The daemon's log levels, ordered finest to coarsest, as select options. Used
 * by the settings Diagnostics section and the log window's level control; both
 * read and change the daemon's live level through `GET`/`PATCH /api/config`.
 */
export const LOG_LEVEL_OPTIONS: SelectOption[] = [
  { value: 'verbose', label: 'Verbose' },
  { value: 'debug', label: 'Debug' },
  { value: 'info', label: 'Info' },
  { value: 'warn', label: 'Warning' },
  { value: 'error', label: 'Error' },
];

/**
 * Returns the level options, prepending the daemon's current level when it is
 * not one of the known values, so the control can always display it.
 */
export function levelOptions(current?: string): SelectOption[] {
  if (current && !LOG_LEVEL_OPTIONS.some((o) => o.value === current)) {
    return [{ value: current, label: current }, ...LOG_LEVEL_OPTIONS];
  }
  return LOG_LEVEL_OPTIONS;
}
