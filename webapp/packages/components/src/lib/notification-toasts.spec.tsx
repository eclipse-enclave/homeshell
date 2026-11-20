import { fireEvent, render, screen, within } from '@testing-library/react';
import { vi } from 'vitest';

import { NotificationToasts } from './notification-toasts';

const items = [
  { id: 'n1', title: 'Implement is waiting for input', body: 'alpha · feature/x' },
  { id: 'n2', title: 'Verify is waiting for input', body: 'alpha · feature/y' },
];

describe('NotificationToasts', () => {
  it('renders nothing when there are no items', () => {
    const { container } = render(
      <NotificationToasts items={[]} onDismiss={vi.fn()} />,
    );
    expect(container.firstChild).toBeNull();
  });

  it('renders one alert per item with its title and body', () => {
    render(<NotificationToasts items={items} onDismiss={vi.fn()} />);
    const alerts = screen.getAllByRole('alert');
    expect(alerts).toHaveLength(2);
    expect(screen.getByText('Implement is waiting for input')).toBeTruthy();
    expect(screen.getByText('alpha · feature/x')).toBeTruthy();
  });

  it('calls onDismiss with the item id when dismissed', () => {
    const onDismiss = vi.fn();
    render(<NotificationToasts items={items} onDismiss={onDismiss} />);
    const buttons = screen.getAllByRole('button', {
      name: 'Dismiss notification',
    });
    // The newest, on top.
    fireEvent.click(buttons[0]);
    expect(onDismiss).toHaveBeenCalledWith('n2');
  });

  it('calls onOpen with the item id when an openable toast is clicked', () => {
    const onOpen = vi.fn();
    render(
      <NotificationToasts
        items={[{ ...items[0], openable: true }]}
        onDismiss={vi.fn()}
        onOpen={onOpen}
      />,
    );
    fireEvent.click(
      screen.getByRole('button', { name: /Implement is waiting for input/ }),
    );
    expect(onOpen).toHaveBeenCalledWith('n1');
  });

  it('offers nothing to open on a toast that is not openable', () => {
    render(
      <NotificationToasts items={items} onDismiss={vi.fn()} onOpen={vi.fn()} />,
    );
    expect(
      screen.queryByRole('button', { name: /waiting for input/ }),
    ).toBeNull();
  });

  it('shows the newest toast on top, so a new one leaves the rest in place', () => {
    render(<NotificationToasts items={items} onDismiss={vi.fn()} />);
    expect(
      screen.getAllByRole('alert').map((alert) => alert.textContent),
    ).toEqual([
      expect.stringContaining('Verify'),
      expect.stringContaining('Implement'),
    ]);
  });

  it('holds every toast while the pointer is over the stack', () => {
    const onHold = vi.fn();
    const onRelease = vi.fn();
    const { rerender } = render(
      <NotificationToasts
        items={items}
        onDismiss={vi.fn()}
        onHold={onHold}
        onRelease={onRelease}
      />,
    );
    const region = screen.getByRole('region', { name: 'Notifications' });

    fireEvent.pointerEnter(region);
    expect(onHold.mock.calls.flat().sort()).toEqual(['n1', 'n2']);

    // One that arrives under the pointer is held too.
    const third = { id: 'n3', title: 'Review finished', body: '' };
    rerender(
      <NotificationToasts
        items={[...items, third]}
        onDismiss={vi.fn()}
        onHold={onHold}
        onRelease={onRelease}
      />,
    );
    expect(onHold).toHaveBeenLastCalledWith('n3');
    expect(onRelease).not.toHaveBeenCalled();

    fireEvent.pointerLeave(region);
    expect(onRelease.mock.calls.flat().sort()).toEqual(['n1', 'n2', 'n3']);
  });

  it('holds just the toast that has focus', () => {
    const onHold = vi.fn();
    const onRelease = vi.fn();
    render(
      <NotificationToasts
        items={items}
        onDismiss={vi.fn()}
        onHold={onHold}
        onRelease={onRelease}
      />,
    );
    const dismiss = within(
      screen.getAllByRole('alert')[1],
    ).getByRole('button', { name: 'Dismiss notification' });

    fireEvent.focus(dismiss);
    expect(onHold.mock.calls).toEqual([['n1']]);
    fireEvent.blur(dismiss);
    expect(onRelease.mock.calls).toEqual([['n1']]);
  });

  it('keeps holding a focused toast when the pointer leaves the stack', () => {
    const onRelease = vi.fn();
    render(
      <NotificationToasts
        items={items}
        onDismiss={vi.fn()}
        onHold={vi.fn()}
        onRelease={onRelease}
      />,
    );
    const region = screen.getByRole('region', { name: 'Notifications' });
    const dismiss = within(
      screen.getAllByRole('alert')[1],
    ).getByRole('button', { name: 'Dismiss notification' });

    fireEvent.focus(dismiss);
    fireEvent.pointerEnter(region);
    fireEvent.pointerLeave(region);
    expect(onRelease.mock.calls).toEqual([['n2']]);

    fireEvent.blur(dismiss);
    expect(onRelease).toHaveBeenLastCalledWith('n1');
  });

  it('forgets the pointer once the stack empties, so a new toast is not held', () => {
    const onHold = vi.fn();
    const { rerender } = render(
      <NotificationToasts items={items} onDismiss={vi.fn()} onHold={onHold} />,
    );
    fireEvent.pointerEnter(screen.getByRole('region', { name: 'Notifications' }));
    rerender(<NotificationToasts items={[]} onDismiss={vi.fn()} onHold={onHold} />);
    onHold.mockClear();

    rerender(
      <NotificationToasts items={[items[0]]} onDismiss={vi.fn()} onHold={onHold} />,
    );

    expect(onHold).not.toHaveBeenCalled();
  });

  it('exposes the stack as a labelled region', () => {
    render(<NotificationToasts items={items} onDismiss={vi.fn()} />);
    expect(screen.getByRole('region', { name: 'Notifications' })).toBeTruthy();
  });
});
