import { act, render } from '@testing-library/react';
import { vi } from 'vitest';

import { PENDING_FOCUS_MS, usePendingFocus } from './use-pending-focus';

/** Holds `target` pending, settled by `settle`, re-rendered on demand. */
function renderPending(settle: (target: string) => boolean) {
  let setPending: (target: string) => void = () => undefined;
  function Pending({ tick }: { tick: number }) {
    setPending = usePendingFocus(settle);
    return <span>{tick}</span>;
  }
  const { rerender } = render(<Pending tick={0} />);
  let tick = 0;
  return {
    hold: (target: string) => act(() => setPending(target)),
    render: () => rerender(<Pending tick={++tick} />),
  };
}

describe('usePendingFocus', () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it('settles on a later render', () => {
    let listed = false;
    const settle = vi.fn(() => listed);
    const pending = renderPending(settle);
    pending.hold('w1');
    expect(settle).toHaveBeenLastCalledWith('w1');

    listed = true;
    pending.render();
    const calls = settle.mock.calls.length;
    pending.render();
    expect(settle).toHaveBeenCalledTimes(calls);
  });

  it('is dropped after PENDING_FOCUS_MS', () => {
    const settle = vi.fn(() => false);
    const pending = renderPending(settle);
    pending.hold('w1');
    act(() => vi.advanceTimersByTime(PENDING_FOCUS_MS));
    const calls = settle.mock.calls.length;

    pending.render();
    expect(settle).toHaveBeenCalledTimes(calls);
  });

  it('keeps trying until then', () => {
    const settle = vi.fn(() => false);
    const pending = renderPending(settle);
    pending.hold('w1');
    act(() => vi.advanceTimersByTime(PENDING_FOCUS_MS - 1));
    const calls = settle.mock.calls.length;

    pending.render();
    expect(settle).toHaveBeenCalledTimes(calls + 1);
  });
});
