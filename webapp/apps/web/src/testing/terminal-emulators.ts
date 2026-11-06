import type { TerminalEmulatorList } from '../app/settings/use-terminal-emulators';

/** Two emulators, as GET /api/terminal-emulators lists them: one installed, and automatic. */
export const TERMINAL_EMULATORS: TerminalEmulatorList = {
  automatic: 'Ghostty',
  emulators: [
    { id: 'ghostty', label: 'Ghostty', installed: true },
    { id: 'kitty', label: 'kitty', installed: false },
  ],
};

/**
 * The daemon's answer to GET /api/terminal-emulators, for a fetch stub:
 * `list`, or a failure when there is none.
 */
export function terminalEmulatorsResponse(
  list: TerminalEmulatorList | null = TERMINAL_EMULATORS,
): Response {
  return (
    list
      ? { ok: true, json: async () => list }
      : { ok: false, json: async () => ({}) }
  ) as Response;
}
