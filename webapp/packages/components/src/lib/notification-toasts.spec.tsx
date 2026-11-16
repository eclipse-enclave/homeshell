import { fireEvent, render, screen } from '@testing-library/react';
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
    fireEvent.click(buttons[0]);
    expect(onDismiss).toHaveBeenCalledWith('n1');
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

  it('holds a toast while it is pointed at or focused', () => {
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
    const [first] = screen.getAllByRole('alert');

    fireEvent.pointerEnter(first);
    expect(onHold).toHaveBeenLastCalledWith('n1');
    fireEvent.pointerLeave(first);
    expect(onRelease).toHaveBeenLastCalledWith('n1');

    const [dismiss] = screen.getAllByRole('button', {
      name: 'Dismiss notification',
    });
    fireEvent.focus(dismiss);
    expect(onHold).toHaveBeenCalledTimes(2);
    fireEvent.blur(dismiss);
    expect(onRelease).toHaveBeenCalledTimes(2);
  });

  it('keeps holding a focused toast when the pointer leaves it', () => {
    const onRelease = vi.fn();
    render(
      <NotificationToasts
        items={items}
        onDismiss={vi.fn()}
        onHold={vi.fn()}
        onRelease={onRelease}
      />,
    );
    const [first] = screen.getAllByRole('alert');
    const [dismiss] = screen.getAllByRole('button', {
      name: 'Dismiss notification',
    });

    fireEvent.focus(dismiss);
    fireEvent.pointerEnter(first);
    fireEvent.pointerLeave(first);
    expect(onRelease).not.toHaveBeenCalled();

    fireEvent.blur(dismiss);
    expect(onRelease).toHaveBeenCalledWith('n1');
  });

  it('exposes the stack as a labelled region', () => {
    render(<NotificationToasts items={items} onDismiss={vi.fn()} />);
    expect(screen.getByRole('region', { name: 'Notifications' })).toBeTruthy();
  });
});
