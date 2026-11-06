import {
  type Command,
  type CommandRegistry,
  useCommandRegistry,
  useCommands,
} from '../app/keyboard/commands';

/** Puts `commands` into the surrounding registry while it is mounted. */
export function Contributes({
  commands,
}: {
  commands: Command[] | (() => Command[]);
}) {
  useCommands(commands);
  return null;
}

/**
 * A component to render inside a CommandsProvider, and the registry it last
 * rendered in, for a spec to list and run what the rest contributes.
 */
export function registryProbe() {
  let registry: CommandRegistry | undefined;
  function Probe() {
    registry = useCommandRegistry();
    return null;
  }
  return {
    Probe,
    registry: () => {
      if (!registry) throw new Error('the probe has not been rendered');
      return registry;
    },
  };
}
