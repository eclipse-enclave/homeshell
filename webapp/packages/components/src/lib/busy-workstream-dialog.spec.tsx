import { fireEvent, render, screen, within } from '@testing-library/react';

import type { BusySession } from '@homeshell/model';

import { BusyWorkstreamDialog } from './busy-workstream-dialog';

const running: BusySession[] = [
  { id: 's1', title: 'Implement self-update', state: 'running' },
];

describe('BusyWorkstreamDialog', () => {
  it('explains that sessions are running and names them', () => {
    render(
      <BusyWorkstreamDialog
        workstreamName="feature/self-update"
        sessions={[...running, { id: 's2', title: 'Verify', state: 'waiting' }]}
        onConfirm={vi.fn()}
        onCancel={vi.fn()}
      />,
    );

    const dialog = screen.getByRole('dialog');
    expect(dialog.textContent).toContain('feature/self-update');
    expect(dialog.textContent).toContain('2 sessions');
    // Each session is named with its state in words, not the raw wire value.
    expect(within(dialog).getByText(/Implement self-update/)).toBeTruthy();
    expect(within(dialog).getByText('(Running)')).toBeTruthy();
    expect(within(dialog).getByText(/Verify/)).toBeTruthy();
    expect(within(dialog).getByText('(Waiting for input)')).toBeTruthy();
  });

  it('reads as a single session when only one is running', () => {
    render(
      <BusyWorkstreamDialog
        workstreamName="ws"
        sessions={running}
        onConfirm={vi.fn()}
        onCancel={vi.fn()}
      />,
    );
    expect(screen.getByRole('dialog').textContent).toContain('a session');
  });

  it('confirming asks for the forced removal', () => {
    const onConfirm = vi.fn();
    const onCancel = vi.fn();
    render(
      <BusyWorkstreamDialog
        workstreamName="ws"
        sessions={running}
        onConfirm={onConfirm}
        onCancel={onCancel}
      />,
    );

    fireEvent.click(screen.getByRole('button', { name: 'Stop and delete' }));
    expect(onConfirm).toHaveBeenCalledTimes(1);
    expect(onCancel).not.toHaveBeenCalled();
  });

  it('cancelling leaves the workstream alone', () => {
    const onConfirm = vi.fn();
    const onCancel = vi.fn();
    render(
      <BusyWorkstreamDialog
        workstreamName="ws"
        sessions={running}
        onConfirm={onConfirm}
        onCancel={onCancel}
      />,
    );

    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(onCancel).toHaveBeenCalledTimes(1);
    expect(onConfirm).not.toHaveBeenCalled();
  });

  // It opens on the daemon's answer, so a key already on its way must not force.
  it('opens on Cancel', () => {
    render(
      <BusyWorkstreamDialog
        workstreamName="ws"
        sessions={running}
        onConfirm={vi.fn()}
        onCancel={vi.fn()}
      />,
    );
    expect(document.activeElement).toBe(
      screen.getByRole('button', { name: 'Cancel' }),
    );
  });

  // A session the daemon reported without a title still has to be identifiable.
  it('falls back to the session id when it has no title', () => {
    render(
      <BusyWorkstreamDialog
        workstreamName="ws"
        sessions={[{ id: 'sess-42', title: '', state: 'running' }]}
        onConfirm={vi.fn()}
        onCancel={vi.fn()}
      />,
    );
    expect(screen.getByText(/sess-42/)).toBeTruthy();
  });
});
