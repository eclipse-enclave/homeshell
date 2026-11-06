import { useFetchedJson } from '../use-fetched-json';

/** A terminal emulator the daemon's platform can open windows in. */
export interface TerminalEmulator {
  id: string;
  label: string;
  /** Whether the daemon found it on its host. */
  installed: boolean;
}

/** The daemon's answer to `GET /api/terminal-emulators`. */
export interface TerminalEmulatorList {
  /**
   * The label of what Automatic opens windows in right now — a listed emulator
   * or whatever else $TERMINAL names — or "" when it has nothing to open.
   */
  automatic: string;
  emulators: TerminalEmulator[];
}

function isList(body: unknown): body is TerminalEmulatorList {
  return (
    typeof body === 'object' &&
    body !== null &&
    Array.isArray((body as TerminalEmulatorList).emulators)
  );
}

/** Fetches the daemon's terminal emulators from `GET /api/terminal-emulators` once on mount. */
export function useTerminalEmulators() {
  return useFetchedJson('/api/terminal-emulators', isList);
}
