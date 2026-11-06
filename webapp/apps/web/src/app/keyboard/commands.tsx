import {
  createContext,
  useContext,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type ReactNode,
} from 'react';
import { BOUND_COMMANDS } from './keybindings';
import { CONSUMED, createKeybindingResolver } from './keymap';

/** Something the command palette offers, and a keybinding can run by ID. */
export interface Command {
  id: string;
  label: string;
  /** The span of label cut off first in the palette (see QuickPickItem). */
  elided?: readonly [start: number, end: number];
  run: () => void;
}

export interface CommandRegistry {
  /** Adds a source of commands; the returned function takes it away again. */
  add(source: () => readonly Command[]): () => void;
  list(): Command[];
  run(id: string): void;
  /**
   * Calls `listener` before a keybinding runs a command; the returned function
   * stops that again.
   */
  onKeybinding(listener: () => void): () => void;
  /** Runs `id` for its keybinding, telling the onKeybinding listeners first. */
  runKeybinding(id: string): void;
}

export function createCommandRegistry(): CommandRegistry {
  const sources = new Set<() => readonly Command[]>();
  const listeners = new Set<() => void>();
  const list = () => [...sources].flatMap((source) => source());
  const find = (id: string) => list().find((command) => command.id === id);
  return {
    add(source) {
      sources.add(source);
      return () => {
        sources.delete(source);
      };
    },
    list,
    run(id) {
      find(id)?.run();
    },
    onKeybinding(listener) {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
    runKeybinding(id) {
      const command = find(id);
      if (!command) return;
      listeners.forEach((listener) => listener());
      command.run();
    },
  };
}

const CommandRegistryContext = createContext<CommandRegistry | null>(null);

// A shortcut must not open a second modal over one that traps focus, save
// over the command palette, which lets them through (see QuickPick).
const OTHER_MODAL = '[aria-modal="true"]:not([data-pass-shortcuts])';

/**
 * Holds the commands the mounted views contribute and runs them from their
 * keybindings (keybindings.ts), with one window-level keydown listener for the
 * app. It listens in the capture phase so a key it takes never also reaches the
 * focused element's own handler.
 */
export function CommandsProvider({ children }: { children: ReactNode }) {
  const [registry] = useState(createCommandRegistry);

  useEffect(() => {
    const keymap = createKeybindingResolver(BOUND_COMMANDS);
    const onKeyDown = (event: KeyboardEvent) => {
      if (
        event.target instanceof Element &&
        event.target.closest(OTHER_MODAL)
      ) {
        keymap.reset();
        return;
      }
      const resolution = keymap.resolve(event);
      if (resolution === null) return;
      event.preventDefault();
      event.stopPropagation();
      if (resolution !== CONSUMED) registry.runKeybinding(resolution);
    };
    // A chord is finished where it was started, or not at all.
    window.addEventListener('keydown', onKeyDown, true);
    window.addEventListener('blur', keymap.reset);
    window.addEventListener('focusin', keymap.reset);
    return () => {
      window.removeEventListener('keydown', onKeyDown, true);
      window.removeEventListener('blur', keymap.reset);
      window.removeEventListener('focusin', keymap.reset);
    };
  }, [registry]);

  return (
    <CommandRegistryContext.Provider value={registry}>
      {children}
    </CommandRegistryContext.Provider>
  );
}

export function useCommandRegistry(): CommandRegistry {
  const registry = useContext(CommandRegistryContext);
  if (!registry) {
    throw new Error(
      'useCommandRegistry must be used within a CommandsProvider',
    );
  }
  return registry;
}

/**
 * Offers `commands` for as long as the calling component is mounted. Given a
 * function, it is asked for them each time they are listed.
 */
export function useCommands(
  commands: readonly Command[] | (() => readonly Command[]),
) {
  const registry = useCommandRegistry();
  const latest = useRef(commands);
  useLayoutEffect(() => {
    latest.current = commands;
  });
  useEffect(
    () =>
      registry.add(() => {
        const current = latest.current;
        return typeof current === 'function' ? current() : current;
      }),
    [registry],
  );
}

/**
 * Calls `listener` before a keybinding runs a command, for as long as the
 * calling component is mounted.
 */
export function useOnKeybinding(listener: () => void) {
  const registry = useCommandRegistry();
  const latest = useRef(listener);
  useLayoutEffect(() => {
    latest.current = listener;
  });
  useEffect(() => registry.onKeybinding(() => latest.current()), [registry]);
}
