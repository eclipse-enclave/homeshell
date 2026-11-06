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

  it('exposes the stack as a labelled region', () => {
    render(<NotificationToasts items={items} onDismiss={vi.fn()} />);
    expect(screen.getByRole('region', { name: 'Notifications' })).toBeTruthy();
  });
});
