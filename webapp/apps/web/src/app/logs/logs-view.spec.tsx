import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import type { LogRecord } from '@homeshell/model';

import { LogPane } from './logs-view';
import type { DaemonConnection } from '../use-daemon-state';

const requestRecord: LogRecord = {
  seq: 1,
  time: 't',
  level: 'debug',
  msg: 'api request',
  attrs: { method: 'GET', path: '/api/projects', status: 200, bytes: 19, duration: 12584 },
};

interface Props {
  onClose?: () => void;
  logLevel?: string;
  onLogLevelChange?: (level: string) => Promise<string | null>;
  records?: readonly LogRecord[];
  connection?: DaemonConnection;
  overflowed?: boolean;
  bufferSize?: number;
  disabledReason?: string;
}

function renderPane(props: Props = {}) {
  return render(
    <LogPane
      onClose={props.onClose ?? vi.fn()}
      logLevel={props.logLevel ?? 'debug'}
      onLogLevelChange={props.onLogLevelChange ?? vi.fn().mockResolvedValue(null)}
      records={props.records ?? []}
      connection={props.connection ?? 'connected'}
      overflowed={props.overflowed ?? false}
      bufferSize={props.bufferSize ?? 1000}
      disabledReason={props.disabledReason}
    />
  );
}

describe('LogPane', () => {
  it('takes focus when it opens and gives it back when it closes', () => {
    const opener = document.createElement('button');
    document.body.append(opener);
    opener.focus();
    const { unmount } = renderPane();
    expect(document.activeElement).toBe(screen.getByRole('region', { name: 'Logs' }));

    unmount();
    expect(document.activeElement).toBe(opener);
    opener.remove();
  });

  it('leaves focus alone when it has gone elsewhere by the time it closes', () => {
    const opener = document.createElement('button');
    const elsewhere = document.createElement('button');
    document.body.append(opener, elsewhere);
    opener.focus();
    const { unmount } = renderPane();
    elsewhere.focus();

    unmount();
    expect(document.activeElement).toBe(elsewhere);
    opener.remove();
    elsewhere.remove();
  });

  it('reflects the passed log level in the control', () => {
    renderPane({ logLevel: 'warn' });
    expect((screen.getByLabelText('Level') as HTMLSelectElement).value).toBe('warn');
  });

  it('changes the daemon level through the control', () => {
    const onLogLevelChange = vi.fn().mockResolvedValue(null);
    renderPane({ onLogLevelChange });
    fireEvent.change(screen.getByLabelText('Level'), { target: { value: 'warn' } });
    expect(onLogLevelChange).toHaveBeenCalledWith('warn');
  });

  it('surfaces a rejected log-level change', async () => {
    const onLogLevelChange = vi.fn().mockResolvedValue('Level rejected.');
    renderPane({ onLogLevelChange });
    fireEvent.change(screen.getByLabelText('Level'), { target: { value: 'warn' } });
    await waitFor(() =>
      expect(screen.getByRole('alert').textContent).toContain('Level rejected.')
    );
  });

  it('closes via the toolbar Close button', () => {
    const onClose = vi.fn();
    renderPane({ onClose });
    fireEvent.click(screen.getByRole('button', { name: 'Close logs pane' }));
    expect(onClose).toHaveBeenCalled();
  });

  it('hints that request logs need the debug level while empty', () => {
    renderPane({ records: [] });
    expect(screen.getByText(/emitted at the debug level/)).toBeTruthy();
  });

  it('renders the API access-log columns from record attributes', () => {
    renderPane({ records: [requestRecord] });
    const log = screen.getByRole('log', { name: 'Log entries' });
    expect(within(log).getByText('GET')).toBeTruthy();
    expect(within(log).getByText('/api/projects')).toBeTruthy();
    expect(within(log).getByText('200')).toBeTruthy();
    expect(within(log).getByText('19')).toBeTruthy();
    // 12584 ns is rendered compactly in microseconds.
    expect(within(log).getByText('12.6µs')).toBeTruthy();
  });

  it('shows the overflow placeholder when the ring buffer has dropped entries', () => {
    renderPane({ records: [requestRecord], overflowed: true });
    expect(screen.getByText(/Earlier entries were dropped/)).toBeTruthy();
  });

  it('copies all records when none are selected', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
    renderPane({
      records: [
        { seq: 1, time: 't1', level: 'info', msg: 'one' },
        { seq: 2, time: 't2', level: 'warn', msg: 'two' },
      ],
    });
    fireEvent.click(screen.getByRole('button', { name: 'Copy all' }));
    await waitFor(() => expect(writeText).toHaveBeenCalled());
    const text = writeText.mock.calls[0][0] as string;
    expect(text).toContain('one');
    expect(text).toContain('two');
  });

  it('copies only the selected records', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
    renderPane({
      records: [
        { seq: 1, time: 't1', level: 'info', msg: 'one' },
        { seq: 2, time: 't2', level: 'warn', msg: 'two' },
      ],
    });
    fireEvent.click(screen.getByText('two'));
    fireEvent.click(screen.getByRole('button', { name: 'Copy 1 selected' }));
    await waitFor(() => expect(writeText).toHaveBeenCalled());
    const text = writeText.mock.calls[0][0] as string;
    expect(text).toContain('two');
    expect(text).not.toContain('one');
  });

  // Offline: the log pane is where someone looks *because* the daemon is gone,
  // so almost all of it stays live. Changing the level is the one thing here that
  // calls the daemon, and it is the one thing that goes.
  describe('when the daemon is unreachable', () => {
    const REASON = 'The daemon is offline';

    it('disables the level control and says why', () => {
      renderPane({ disabledReason: REASON });
      const level = screen.getByLabelText('Level') as HTMLSelectElement;
      expect(level.disabled).toBe(true);
      expect(screen.getByText(REASON)).toBeTruthy();
    });

    it('still reads, selects and copies', async () => {
      const writeText = vi.fn().mockResolvedValue(undefined);
      Object.defineProperty(navigator, 'clipboard', {
        value: { writeText },
        configurable: true,
      });
      renderPane({
        disabledReason: REASON,
        records: [{ seq: 1, time: 't1', level: 'info', msg: 'one' }],
      });
      expect(screen.getByText('one')).toBeTruthy();
      fireEvent.click(screen.getByText('one'));
      fireEvent.click(screen.getByRole('button', { name: 'Copy 1 selected' }));
      await waitFor(() => expect(writeText).toHaveBeenCalled());
    });

    it('still closes', () => {
      const onClose = vi.fn();
      renderPane({ disabledReason: REASON, onClose });
      fireEvent.click(screen.getByRole('button', { name: 'Close logs pane' }));
      expect(onClose).toHaveBeenCalled();
    });
  });
});

// The log pane is where a user looks when the daemon has gone away, so only the
// one control that calls it — the live level — is taken away.
describe('LogPane when the daemon is unreachable', () => {
  const REASON = 'The daemon is offline';

  it('disables the level control and says why', () => {
    renderPane({ disabledReason: REASON });
    const level = screen.getByLabelText('Level') as HTMLSelectElement;
    expect(level.disabled).toBe(true);
    expect(screen.getByText(REASON)).toBeTruthy();
  });

  it('keeps reading, copying and closing available', () => {
    const onClose = vi.fn();
    renderPane({
      onClose,
      disabledReason: REASON,
      records: [
        { seq: 1, time: '2026-08-12T10:00:00Z', level: 'warn', msg: 'kept' },
      ],
    });
    expect(screen.getByText('kept')).toBeTruthy();
    expect(
      (screen.getByRole('button', { name: /^Copy/ }) as HTMLButtonElement)
        .disabled
    ).toBe(false);
    fireEvent.click(screen.getByRole('button', { name: 'Close logs pane' }));
    expect(onClose).toHaveBeenCalled();
  });
});
