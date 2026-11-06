import {
  offeredActions,
  type QuickPickItem,
  type WorkstreamCardHandle,
} from '@homeshell/components';
import type { Project, Workstream } from '@homeshell/model';
import { useState } from 'react';
import { type Command, useCommands } from './keyboard/commands';
import { PickOne } from './keyboard/pick-one';

/**
 * A command on something named: its kind and quoted name, then the verb. The
 * name is what the palette cuts off when the row runs short, so the verb stays
 * in view.
 */
function on(kind: string, name: string, verb: string) {
  const start = kind.length + 2;
  return {
    label: `${kind} "${name}": ${verb}`,
    elided: [start, start + name.length] as const,
  };
}

export interface ProjectCommandsOptions {
  project: Project;
  /**
   * Leaves out the commands on the project and New session, which ask the
   * daemon for something; what the selection offers says so itself.
   */
  blocked: boolean;
  /** Whether the project's name is being edited, which Rename would start. */
  renaming: boolean;
  onNewWorkstream: () => void;
  onRename: () => void;
  onRemove: () => void;
  /** The workstream list's selection (see useListTabStop). */
  selectedId: string | undefined;
  /** The cards on the page, by workstream ID. */
  cards: ReadonlyMap<string, WorkstreamCardHandle>;
}

/**
 * The commands of a project view: on the project, New session and what the
 * selected card or row offers, as its buttons and its actions menu do. They
 * are listed when asked for, since what a card offers can change without the
 * view rendering. Returns the picker New session asks with, to render.
 */
export function useProjectCommands({
  project,
  blocked,
  renaming,
  onNewWorkstream,
  onRename,
  onRemove,
  selectedId,
  cards,
}: ProjectCommandsOptions) {
  // The workstreams offered to start a session in, while the user picks one.
  const [choices, setChoices] = useState<QuickPickItem[] | null>(null);

  const selectedWorkstream = project.workstreams.find(
    (workstream) =>
      workstream.id === selectedId ||
      workstream.sessions.some((session) => session.id === selectedId),
  );

  const offersNewSession = (workstream: Workstream) =>
    offeredActions(cards.get(workstream.id)?.actions(workstream.id) ?? []).some(
      (action) => action.id === 'newSession',
    );

  // With no workstream selected, there is only a choice to make if there are
  // several that can start one.
  const newSession = (): Command[] => {
    const offering = project.workstreams.filter(offersNewSession);
    const target = selectedWorkstream
      ? offering.find((workstream) => workstream === selectedWorkstream)
      : offering.length === 1
        ? offering[0]
        : undefined;
    if (target) {
      return [
        {
          id: 'newSession',
          ...on('Workstream', target.name, 'New session'),
          run: () => cards.get(target.id)?.newSession(),
        },
      ];
    }
    if (selectedWorkstream || offering.length === 0) return [];
    return [
      {
        id: 'newSession',
        label: 'New session…',
        run: () =>
          setChoices(
            offering.map((workstream) => ({
              id: workstream.id,
              label: workstream.name,
            })),
          ),
      },
    ];
  };

  const selection = (): Command[] => {
    if (!selectedWorkstream || !selectedId) return [];
    const session = selectedWorkstream.sessions.find(
      (candidate) => candidate.id === selectedId,
    );
    const [kind, name] = session
      ? ['Session', session.title]
      : ['Workstream', selectedWorkstream.name];
    const actions = cards.get(selectedWorkstream.id)?.actions(selectedId) ?? [];
    // The newSession command is already the card's New session.
    return offeredActions(actions)
      .filter((action) => action.id !== 'newSession')
      .map((action) => ({
        id: `selection.${action.id}`,
        ...on(kind, name, action.label),
        run: action.run,
      }));
  };

  useCommands(() => [
    ...(blocked
      ? []
      : [
          {
            id: 'newWorkstream',
            label: 'New workstream',
            run: onNewWorkstream,
          },
          ...(renaming
            ? []
            : [
                {
                  id: 'renameProject',
                  ...on('Project', project.name, 'Rename'),
                  run: onRename,
                },
              ]),
          {
            id: 'deleteProject',
            ...on('Project', project.name, 'Delete'),
            run: onRemove,
          },
          ...newSession(),
        ]),
    ...selection(),
  ]);

  return (
    choices && (
      <PickOne
        label="New session in"
        placeholder="Choose the workstream to start a session in"
        items={choices}
        onPick={(workstream) => cards.get(workstream.id)?.newSession()}
        onClose={() => setChoices(null)}
      />
    )
  );
}
