import type { Workstream } from '@homeshell/model';
import { fireEvent, render, screen } from '@testing-library/react';

import { DeleteWorkstreamDialog } from './delete-workstream-dialog';

const workstream = (strategyId: string): Workstream => ({
  id: 'ws-1',
  name: 'feature/x',
  provider: 'core',
  providerLabel: 'Core',
  strategyId,
  strategy: strategyId,
  state: 'idle',
  runtimeState: 'running',
  effectiveState: 'idle',
  sessions: [],
});

describe('DeleteWorkstreamDialog', () => {
  it.each([
    ['in-place', 'Cancel'],
    ['worktree', 'Cancel'],
    ['copy', 'Cancel'],
  ])('opens a %s workstream on %s', (strategyId, name) => {
    render(
      <DeleteWorkstreamDialog
        workstream={workstream(strategyId)}
        onConfirm={vi.fn()}
        onCancel={vi.fn()}
      />,
    );
    expect(document.activeElement).toBe(screen.getByRole('button', { name }));
  });

  it('offers to keep the files of a workstream that has them', () => {
    const onConfirm = vi.fn();
    render(
      <DeleteWorkstreamDialog
        workstream={workstream('worktree')}
        onConfirm={onConfirm}
        onCancel={vi.fn()}
      />,
    );
    fireEvent.click(screen.getByRole('button', { name: 'Keep files' }));
    expect(onConfirm).toHaveBeenCalledWith({ keepFiles: true });
  });

  it.each<[string, Partial<Workstream>]>([
    ['still being made', { effectiveState: 'creating' }],
    ['that failed', { createFailed: true }],
  ])('offers no files to keep for a create %s', (_, state) => {
    render(
      <DeleteWorkstreamDialog
        workstream={{ ...workstream('worktree'), ...state }}
        onConfirm={vi.fn()}
        onCancel={vi.fn()}
      />,
    );
    expect(screen.queryByRole('button', { name: 'Keep files' })).toBeNull();
  });
});
