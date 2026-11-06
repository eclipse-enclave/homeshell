import {
  QuickPick,
  type QuickPickItem,
  fuzzyFilter,
  fuzzyScore,
  tabStopItem,
} from '@homeshell/components';
import type { Project } from '@homeshell/model';
import { useState } from 'react';
import { useCommandRegistry, useOnKeybinding } from './commands';
import { keysFor } from './keybindings';

export interface CommandPaletteProps {
  /** The text it opens with: a leading `>` lists commands, anything else places to go. */
  initialQuery: string;
  projects: readonly Project[];
  /** Shows a project and, when given, focuses one of its workstreams or sessions. */
  onGoTo: (projectId: string, itemId?: string) => void;
  onClose: () => void;
}

interface Entry extends QuickPickItem {
  run: () => void;
}

// Above any score a location match can reach, so every name match ranks first.
const NAME_MATCH = 10_000;

/**
 * A place is found by its name first. Only when the name does not match does
 * where it lives count, so a path or project name shared by everything under
 * it does not bury the item that is actually called that.
 */
function placeScore(query: string, place: Entry): number | null {
  const byName = fuzzyScore(query, place.label);
  if (byName !== null) return NAME_MATCH + byName;
  return fuzzyScore(query, `${place.label} ${place.detail}`);
}

function places(
  projects: readonly Project[],
  onGoTo: CommandPaletteProps['onGoTo'],
): Entry[] {
  return projects.flatMap((project) => [
    {
      id: project.id,
      label: project.name,
      detail: project.path,
      run: () => onGoTo(project.id),
    },
    ...project.workstreams.flatMap((workstream) => [
      {
        id: workstream.id,
        label: workstream.name,
        detail: `Workstream · ${project.name}`,
        run: () => onGoTo(project.id, workstream.id),
      },
      ...workstream.sessions.map((session) => ({
        id: session.id,
        label: session.title,
        detail: `Session · ${project.name} › ${workstream.name}`,
        run: () => onGoTo(project.id, session.id),
      })),
    ]),
  ]);
}

/**
 * VS Code's quick open and command palette in one box, told apart by a leading
 * `>` just as there: typing it switches to commands, deleting it back to going
 * to a project, workstream or session.
 */
export function CommandPalette({
  initialQuery,
  projects,
  onGoTo,
  onClose,
}: CommandPaletteProps) {
  const registry = useCommandRegistry();
  // What was on offer when it opened: the list does not reshuffle under the
  // user while they are choosing.
  const [commands] = useState(() =>
    registry
      .list()
      .sort((a, b) => a.label.localeCompare(b.label))
      .map((command) => ({ ...command, shortcut: keysFor(command.id)[0] })),
  );
  const [query, setQuery] = useState(initialQuery);
  // A key it lets through runs its command with the palette closed, or the
  // palette would cover what the command shows. One that opens it opens it anew.
  useOnKeybinding(onClose);
  const commandMode = query.startsWith('>');
  const items = commandMode
    ? fuzzyFilter(commands, (command) =>
        fuzzyScore(query.slice(1), command.label),
      )
    : fuzzyFilter(places(projects, onGoTo), (place) =>
        placeScore(query, place),
      );

  return (
    <QuickPick
      label={commandMode ? 'Command palette' : 'Go to'}
      value={query}
      onValueChange={setQuery}
      items={items}
      onSelect={(item) => {
        onClose();
        item.run();
      }}
      onClose={onClose}
      placeholder={
        commandMode
          ? 'Type the name of a command'
          : 'Go to a project, workstream or session (type > for commands)'
      }
      emptyText={commandMode ? 'No matching commands' : 'No matching items'}
      fallbackFocus={() => tabStopItem(document)}
      passShortcuts
    />
  );
}
