import { fireEvent, render, screen, within } from '@testing-library/react';
import type { LogRecord } from '@homeshell/model';

import { LogViewer, type LogColumn } from './log-viewer';

const records: LogRecord[] = [
  { seq: 1, time: '2026-07-09T10:00:00.000Z', level: 'info', msg: 'started' },
  {
    seq: 2,
    time: '2026-07-09T10:00:01.000Z',
    level: 'warn',
    msg: 'api request',
    attrs: {
      method: 'GET',
      path: '/api/projects',
      status: 200,
      bytes: 19,
      duration: 12584,
    },
  },
];

const NONE: ReadonlySet<number> = new Set();

describe('LogViewer', () => {
  it('renders one row per record', () => {
    render(<LogViewer records={records} selected={NONE} onSelect={vi.fn()} />);
    const log = screen.getByRole('log', { name: 'Log entries' });
    expect(within(log).getAllByRole('button')).toHaveLength(2);
    expect(within(log).getByText('started')).toBeTruthy();
    expect(within(log).getByText('api request')).toBeTruthy();
  });

  it('titles a message in full on hover while it is cut off', () => {
    render(<LogViewer records={records} selected={NONE} onSelect={vi.fn()} />);
    const message = screen.getByText('api request');
    Object.defineProperty(message, 'scrollWidth', { value: 500 });
    fireEvent.pointerEnter(message);
    expect(message.getAttribute('title')).toBe('api request');
  });

  it('shows a placeholder when there are no records', () => {
    render(<LogViewer records={[]} selected={NONE} onSelect={vi.fn()} />);
    expect(screen.getByText(/Waiting for log activity/)).toBeTruthy();
  });

  it('renders a leading notice above the records when given', () => {
    render(
      <LogViewer
        records={records}
        selected={NONE}
        onSelect={vi.fn()}
        leadingNotice="Earlier entries were dropped."
      />
    );
    expect(screen.getByText('Earlier entries were dropped.')).toBeTruthy();
  });

  it('omits the leading notice when there are no records', () => {
    render(
      <LogViewer
        records={[]}
        selected={NONE}
        onSelect={vi.fn()}
        leadingNotice="should not show"
      />
    );
    expect(screen.queryByText('should not show')).toBeNull();
  });

  it('shows a caller-supplied empty message', () => {
    render(
      <LogViewer
        records={[]}
        selected={NONE}
        onSelect={vi.fn()}
        emptyMessage="Nothing to see here"
      />
    );
    expect(screen.getByText('Nothing to see here')).toBeTruthy();
  });

  it('renders caller-supplied columns between level and message', () => {
    const columns: LogColumn[] = [
      { key: 'method', header: 'Method', render: (r) => String(r.attrs?.method ?? '') },
      { key: 'path', header: 'Path', render: (r) => String(r.attrs?.path ?? '') },
    ];
    render(
      <LogViewer
        records={records}
        selected={NONE}
        onSelect={vi.fn()}
        columns={columns}
      />
    );
    const log = screen.getByRole('log', { name: 'Log entries' });
    expect(within(log).getByText('Method')).toBeTruthy();
    expect(within(log).getByText('GET')).toBeTruthy();
    expect(within(log).getByText('/api/projects')).toBeTruthy();
  });

  it('renders no extra columns by default', () => {
    render(<LogViewer records={records} selected={NONE} onSelect={vi.fn()} />);
    const log = screen.getByRole('log', { name: 'Log entries' });
    // With no columns configured the request attributes are not shown as cells.
    expect(within(log).queryByText('GET')).toBeNull();
    expect(within(log).queryByText('/api/projects')).toBeNull();
  });

  it('reports the clicked row and its modifier keys', () => {
    const onSelect = vi.fn();
    render(<LogViewer records={records} selected={NONE} onSelect={onSelect} />);
    fireEvent.click(screen.getByText('api request'), { metaKey: true });
    expect(onSelect).toHaveBeenCalledWith(2, { toggle: true, range: false });
  });

  it('marks selected rows as pressed', () => {
    render(
      <LogViewer records={records} selected={new Set([2])} onSelect={vi.fn()} />
    );
    const rows = screen.getByRole('log').querySelectorAll('button');
    expect(rows[0].getAttribute('aria-pressed')).toBe('false');
    expect(rows[1].getAttribute('aria-pressed')).toBe('true');
  });

  it('shows the focused record properties in the details pane', () => {
    render(
      <LogViewer
        records={records}
        selected={new Set([2])}
        focusedSeq={2}
        onSelect={vi.fn()}
      />
    );
    const details = screen.getByLabelText('Log entry details');
    expect(within(details).getByText('api request')).toBeTruthy();
    expect(within(details).getByText('method')).toBeTruthy();
    expect(within(details).getByText('/api/projects')).toBeTruthy();
  });

  it('prompts to select an entry when none is focused', () => {
    render(<LogViewer records={records} selected={NONE} onSelect={vi.fn()} />);
    expect(screen.getByText(/Select an entry/)).toBeTruthy();
  });
});
