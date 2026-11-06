import { fireEvent, render, screen } from '@testing-library/react';
import { useState } from 'react';

import { QuickPick, type QuickPickItem } from './quick-pick';

const items: QuickPickItem[] = [
  { id: 'settings', label: 'Open Settings', shortcut: 'Ctrl+,' },
  { id: 'logs', label: 'Toggle Logs' },
  { id: 'alpha', label: 'alpha', detail: '/tmp/alpha' },
];

function Harness({
  onSelect = vi.fn(),
  onClose = vi.fn(),
  fallbackFocus,
}: {
  onSelect?: (item: QuickPickItem) => void;
  onClose?: () => void;
  fallbackFocus?: () => HTMLElement | null;
}) {
  const [value, setValue] = useState('');
  return (
    <QuickPick
      label="Command palette"
      value={value}
      onValueChange={setValue}
      items={items.filter((item) => item.label.includes(value))}
      onSelect={onSelect}
      onClose={onClose}
      emptyText="No matching commands"
      fallbackFocus={fallbackFocus}
    />
  );
}

const input = () => screen.getByRole('combobox', { name: 'Command palette' });
const selected = () =>
  screen
    .getAllByRole('option')
    .find((option) => option.getAttribute('aria-selected') === 'true');

describe('QuickPick', () => {
  it('is a modal dialog around a combobox that owns a listbox', () => {
    render(<Harness />);
    expect(
      screen
        .getByRole('dialog', { name: 'Command palette' })
        .getAttribute('aria-modal'),
    ).toBe('true');
    expect(input().getAttribute('aria-controls')).toBe(
      screen.getByRole('listbox').id,
    );
    expect(screen.getAllByRole('option')).toHaveLength(3);
    expect(input().getAttribute('aria-expanded')).toBe('true');
  });

  // Keyed by item, not position, so a screen reader's pointer does not jump to
  // another item that happens to land in the same slot as the list filters.
  it('points at the highlighted option by the item’s own ID', () => {
    render(<Harness />);
    const logs = screen.getByRole('option', { name: /Toggle Logs/ });
    fireEvent.change(input(), { target: { value: 'Logs' } });
    expect(screen.getByRole('option', { name: /Toggle Logs/ }).id).toBe(
      logs.id,
    );
    expect(input().getAttribute('aria-activedescendant')).toBe(logs.id);
  });

  it('takes focus, and gives it back when it closes', () => {
    render(<button type="button">Before</button>);
    const before = screen.getByRole('button', { name: 'Before' });
    before.focus();
    const { unmount } = render(<Harness />);
    expect(document.activeElement).toBe(input());
    unmount();
    expect(document.activeElement).toBe(before);
  });

  // The detail is cut off with an ellipsis when long; hovering shows all of it.
  it('titles the detail in full on hover while it is cut off', () => {
    render(<Harness />);
    const detail = screen.getByText('/tmp/alpha');
    Object.defineProperty(detail, 'scrollWidth', { value: 500 });
    fireEvent.pointerEnter(detail);
    expect(detail.getAttribute('title')).toBe('/tmp/alpha');
  });

  // Only the elided span gives way, and its tooltip is the whole label.
  it('cuts off only the elided span of a label, titling it in full', () => {
    const label = 'Session "Session": Open';
    render(
      <QuickPick
        label="Command palette"
        value=""
        onValueChange={vi.fn()}
        items={[{ id: 'open', label, elided: [9, 16] }]}
        onSelect={vi.fn()}
        onClose={vi.fn()}
        emptyText="No matching commands"
      />,
    );
    const option = screen.getByRole('option', { name: label });
    const elided = screen.getByText('Session', { selector: 'span span' });
    expect(option.textContent).toBe(label);
    expect(elided.previousSibling?.textContent).toBe('Session "');
    expect(elided.nextSibling?.textContent).toBe('": Open');
    Object.defineProperty(elided, 'scrollWidth', { value: 500 });
    fireEvent.pointerEnter(elided);
    expect(elided.getAttribute('title')).toBe(label);
  });

  it('falls back when the element it came from has gone', () => {
    const { unmount: removeBefore } = render(
      <button type="button">Before</button>,
    );
    render(<button type="button">Fallback</button>);
    const fallback = screen.getByRole('button', { name: 'Fallback' });
    screen.getByRole('button', { name: 'Before' }).focus();
    const { unmount } = render(<Harness fallbackFocus={() => fallback} />);
    removeBefore();
    unmount();
    expect(document.activeElement).toBe(fallback);
  });

  it('moves the highlight with the arrows, wrapping at the ends', () => {
    render(<Harness />);
    expect(selected()?.textContent).toContain('Open Settings');
    expect(input().getAttribute('aria-activedescendant')).toBe(selected()?.id);
    fireEvent.keyDown(input(), { key: 'ArrowDown' });
    expect(selected()?.textContent).toContain('Toggle Logs');
    fireEvent.keyDown(input(), { key: 'ArrowUp' });
    fireEvent.keyDown(input(), { key: 'ArrowUp' });
    expect(selected()?.textContent).toContain('alpha');
  });

  it('picks the highlighted item on Enter', () => {
    const onSelect = vi.fn();
    render(<Harness onSelect={onSelect} />);
    fireEvent.keyDown(input(), { key: 'ArrowDown' });
    fireEvent.keyDown(input(), { key: 'Enter' });
    expect(onSelect).toHaveBeenCalledWith(items[1]);
  });

  it('picks an item on click', () => {
    const onSelect = vi.fn();
    render(<Harness onSelect={onSelect} />);
    fireEvent.click(screen.getByRole('option', { name: /alpha/ }));
    expect(onSelect).toHaveBeenCalledWith(items[2]);
  });

  it('highlights the best match again as the text changes', () => {
    render(<Harness />);
    fireEvent.keyDown(input(), { key: 'ArrowDown' });
    fireEvent.change(input(), { target: { value: 'l' } });
    expect(selected()?.textContent).toContain('Toggle Logs');
  });

  it('says so when nothing matches, and Enter picks nothing', () => {
    const onSelect = vi.fn();
    render(<Harness onSelect={onSelect} />);
    fireEvent.change(input(), { target: { value: 'zzz' } });
    expect(screen.getByRole('status').textContent).toBe('No matching commands');
    expect(input().getAttribute('aria-expanded')).toBe('false');
    expect(input().hasAttribute('aria-activedescendant')).toBe(false);
    fireEvent.keyDown(input(), { key: 'Enter' });
    expect(onSelect).not.toHaveBeenCalled();
  });

  it.each([
    ['Escape', () => fireEvent.keyDown(input(), { key: 'Escape' })],
    [
      'a click outside',
      () =>
        fireEvent.click(
          screen.getByRole('dialog', { name: 'Command palette' }),
        ),
    ],
  ])('closes on %s', (_how, close) => {
    const onClose = vi.fn();
    render(<Harness onClose={onClose} />);
    close();
    expect(onClose).toHaveBeenCalled();
  });

  it('takes Escape, and keeps every key from the elements around it', () => {
    const onKeyDown = vi.fn();
    render(
      <div onKeyDown={onKeyDown}>
        <Harness />
      </div>,
    );
    expect(fireEvent.keyDown(input(), { key: 'Escape' })).toBe(false);
    fireEvent.keyDown(input(), { key: 'F2' });
    expect(onKeyDown).not.toHaveBeenCalled();
  });

  it('leaves Escape to an input method that is composing', () => {
    const onClose = vi.fn();
    render(<Harness onClose={onClose} />);
    fireEvent.keyDown(input(), { key: 'Escape', isComposing: true });
    expect(onClose).not.toHaveBeenCalled();
  });

  it('leaves Enter to an input method that is composing', () => {
    const onSelect = vi.fn();
    render(<Harness onSelect={onSelect} />);
    fireEvent.keyDown(input(), { key: 'Enter', isComposing: true });
    expect(onSelect).not.toHaveBeenCalled();
  });

  it('keeps its status line empty while something matches', () => {
    render(<Harness />);
    expect(screen.getByRole('status').textContent).toBe('');
  });

  it('keeps Tab inside', () => {
    render(<Harness />);
    expect(fireEvent.keyDown(input(), { key: 'Tab' })).toBe(false);
  });

  it('shows each item’s detail and shortcut', () => {
    render(<Harness />);
    expect(screen.getByText('/tmp/alpha')).toBeTruthy();
    expect(screen.getByText('Ctrl+,').tagName).toBe('KBD');
  });
});
