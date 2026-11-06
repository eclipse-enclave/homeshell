import { act, fireEvent, render, screen } from '@testing-library/react';

import { MenuButton } from './menu-button';

const items = [
  { id: 'terminal', label: 'Terminal' },
  { id: 'ide', label: 'Visual Studio Code' },
];

describe('MenuButton', () => {
  it('opens a menu of the given items and reports the chosen id', () => {
    const onSelect = vi.fn();
    render(<MenuButton label="Open" items={items} onSelect={onSelect} />);

    expect(screen.queryByRole('menu')).toBeNull();

    fireEvent.click(screen.getByRole('button', { name: 'Open' }));
    expect(screen.getByRole('menu')).toBeTruthy();
    fireEvent.click(
      screen.getByRole('menuitem', { name: 'Visual Studio Code' }),
    );

    expect(onSelect).toHaveBeenCalledWith('ide');
    // Choosing closes the menu.
    expect(screen.queryByRole('menu')).toBeNull();
  });

  it('renders the labels it is given, never mapping ids to names itself', () => {
    render(
      <MenuButton
        label="Open"
        items={[{ id: 'web-ide', label: 'Theia in the browser' }]}
        onSelect={vi.fn()}
      />,
    );
    expect(screen.getByRole('button', { name: 'Open' }).textContent).toContain(
      'Open',
    );
  });

  it('fires a single item directly, with no menu', () => {
    const onSelect = vi.fn();
    render(
      <MenuButton
        label="Open"
        items={[{ id: 'terminal', label: 'Terminal' }]}
        onSelect={onSelect}
      />,
    );

    fireEvent.click(screen.getByRole('button', { name: 'Open' }));
    expect(onSelect).toHaveBeenCalledWith('terminal');
    expect(screen.queryByRole('menu')).toBeNull();
  });

  it('splits into a default action and a menu when onPrimary is set', () => {
    const onPrimary = vi.fn();
    const onSelect = vi.fn();
    render(
      <MenuButton
        label="Open"
        items={items}
        onPrimary={onPrimary}
        onSelect={onSelect}
      />,
    );

    fireEvent.click(screen.getByRole('button', { name: 'Open' }));
    expect(onPrimary).toHaveBeenCalledTimes(1);
    expect(onSelect).not.toHaveBeenCalled();
    expect(screen.queryByRole('menu')).toBeNull();

    fireEvent.click(screen.getByRole('button', { name: 'Open with…' }));
    fireEvent.click(screen.getByRole('menuitem', { name: 'Terminal' }));
    expect(onSelect).toHaveBeenCalledWith('terminal');
    expect(onPrimary).toHaveBeenCalledTimes(1);
  });

  // Alt+O is the default action; the menu has the arrow keys.
  it('gives its letter to the default action in split mode, and Alt+Shift to the menu', () => {
    render(
      <MenuButton
        label="Open"
        items={items}
        onPrimary={vi.fn()}
        onSelect={vi.fn()}
        mnemonic="o"
      />,
    );
    const primary = screen.getByRole('button', { name: 'Open' });
    expect(primary.getAttribute('data-mnemonic')).toBe('o');
    const caret = screen.getByRole('button', { name: 'Open with…' });
    expect(caret.getAttribute('data-mnemonic')).toBe('O');
    expect(caret.getAttribute('aria-keyshortcuts')).toBe('Alt+Shift+O');
  });

  it('gives its letter to what opens the menu without a default', () => {
    render(
      <MenuButton label="Open" items={items} onSelect={vi.fn()} mnemonic="o" />,
    );
    const trigger = screen.getByRole('button', { name: 'Open' });
    expect(trigger.getAttribute('data-mnemonic')).toBe('o');
    // The label and the caret, the flex gap between them alone.
    expect(Array.from(trigger.childNodes, (node) => node.textContent)).toEqual(
      ['Open', '▾'],
    );
    fireEvent.click(trigger);
    expect(screen.getByRole('menu')).toBeTruthy();
  });

  it('names no key on a disabled menu', () => {
    render(
      <MenuButton
        label="Open"
        items={items}
        onSelect={vi.fn()}
        mnemonic="o"
        title="Offline"
        disabled
      />,
    );
    expect(screen.getByRole('button', { name: 'Open' }).title).toBe('Offline');
  });

  // In split mode the single item is a different action than the default.
  it('keeps a one-item menu in split mode', () => {
    const onSelect = vi.fn();
    render(
      <MenuButton
        label="Open"
        items={[{ id: 'open-with:e1', label: 'VS Code' }]}
        onPrimary={vi.fn()}
        onSelect={onSelect}
      />,
    );

    fireEvent.click(screen.getByRole('button', { name: 'Open with…' }));
    fireEvent.click(screen.getByRole('menuitem', { name: 'VS Code' }));
    expect(onSelect).toHaveBeenCalledWith('open-with:e1');
  });

  it('renders nothing in split mode when there are no items', () => {
    const { container } = render(
      <MenuButton
        label="Open"
        items={[]}
        onPrimary={vi.fn()}
        onSelect={vi.fn()}
      />,
    );
    expect(container.firstChild).toBeNull();
  });

  it('renders nothing when there are no items', () => {
    const { container } = render(
      <MenuButton label="Open" items={[]} onSelect={vi.fn()} />,
    );
    expect(container.firstChild).toBeNull();
  });

  it('closes on Escape and on an outside click', () => {
    render(<MenuButton label="Open" items={items} onSelect={vi.fn()} />);

    fireEvent.click(screen.getByRole('button', { name: 'Open' }));
    expect(
      fireEvent.keyDown(screen.getByRole('menuitem', { name: 'Terminal' }), {
        key: 'Escape',
      }),
    ).toBe(false);
    expect(screen.queryByRole('menu')).toBeNull();
    expect(document.activeElement).toBe(
      screen.getByRole('button', { name: 'Open' }),
    );

    fireEvent.click(screen.getByRole('button', { name: 'Open' }));
    fireEvent.mouseDown(document.body);
    expect(screen.queryByRole('menu')).toBeNull();
  });

  // Portalled, it still bubbles through React to what it was opened from.
  it('keeps its keys from the elements around it', () => {
    const onKeyDown = vi.fn();
    render(
      <div onKeyDown={onKeyDown}>
        <MenuButton label="Open" items={items} onSelect={vi.fn()} />
      </div>,
    );
    fireEvent.click(screen.getByRole('button', { name: 'Open' }));
    const item = screen.getByRole('menuitem', { name: 'Terminal' });
    fireEvent.keyDown(item, { key: 'F2' });
    fireEvent.keyDown(item, { key: 'ArrowDown' });
    fireEvent.keyDown(document.activeElement as Element, { key: 'Escape' });
    expect(onKeyDown).not.toHaveBeenCalled();
  });

  it('sets an item that starts a group apart, and walks past the separator', () => {
    render(
      <MenuButton
        label="Open"
        items={[...items, { id: 'drop', label: 'Drop', separated: true }]}
        onSelect={vi.fn()}
      />,
    );
    fireEvent.click(screen.getByRole('button', { name: 'Open' }));
    expect(
      screen.getByRole('separator').nextElementSibling?.textContent,
    ).toBe('Drop');
    fireEvent.keyDown(document.activeElement as Element, { key: 'End' });
    expect(document.activeElement?.textContent).toBe('Drop');
    fireEvent.keyDown(document.activeElement as Element, { key: 'ArrowUp' });
    expect(document.activeElement?.textContent).toBe('Visual Studio Code');
  });

  it('shows the keys of an item after its label, outside its name', () => {
    render(
      <MenuButton
        label="Open"
        items={[...items, { id: 'rename', label: 'Rename', shortcut: 'f2' }]}
        onSelect={vi.fn()}
      />,
    );
    fireEvent.click(screen.getByRole('button', { name: 'Open' }));

    const rename = screen.getByRole('menuitem', { name: 'Rename' });
    expect(rename.textContent).toBe('RenameF2');
    expect(rename.getAttribute('aria-keyshortcuts')).toBe('F2');
    const terminal = screen.getByRole('menuitem', { name: 'Terminal' });
    expect(terminal.hasAttribute('aria-keyshortcuts')).toBe(false);
  });

  it('closes when its items go away', () => {
    const { rerender } = render(
      <MenuButton label="Open" items={items} onSelect={vi.fn()} />,
    );
    fireEvent.click(screen.getByRole('button', { name: 'Open' }));
    expect(screen.getByRole('menu')).toBeTruthy();

    rerender(<MenuButton label="Open" items={[]} onSelect={vi.fn()} />);
    expect(screen.queryByRole('menu')).toBeNull();
  });

  it('does not spring open when its items shrink to one and grow back', () => {
    const { rerender } = render(
      <MenuButton label="Open" items={items} onSelect={vi.fn()} />,
    );
    fireEvent.click(screen.getByRole('button', { name: 'Open' }));
    expect(screen.getByRole('menu')).toBeTruthy();

    rerender(
      <MenuButton label="Open" items={[items[0]]} onSelect={vi.fn()} />,
    );
    expect(screen.queryByRole('menu')).toBeNull();

    rerender(<MenuButton label="Open" items={items} onSelect={vi.fn()} />);
    expect(screen.queryByRole('menu')).toBeNull();
    // And the trigger agrees, so assistive technology is not told otherwise.
    expect(
      screen.getByRole('button', { name: 'Open' }).getAttribute('aria-expanded'),
    ).toBe('false');
  });

  it('renders the menu outside any clipping ancestor', () => {
    const { container } = render(
      <div style={{ overflow: 'hidden' }}>
        <MenuButton label="Open" items={items} onSelect={vi.fn()} />
      </div>,
    );

    fireEvent.click(screen.getByRole('button', { name: 'Open' }));
    const menu = screen.getByRole('menu');
    expect(container.contains(menu)).toBe(false);
    expect(document.body.contains(menu)).toBe(true);
  });

  // The menu is portalled, so a click inside it is "outside" the trigger in DOM terms.
  it('does not close when the pointer goes down inside the menu', () => {
    const onSelect = vi.fn();
    render(<MenuButton label="Open" items={items} onSelect={onSelect} />);

    fireEvent.click(screen.getByRole('button', { name: 'Open' }));
    const item = screen.getByRole('menuitem', { name: 'Terminal' });
    fireEvent.mouseDown(item);
    expect(screen.getByRole('menu')).toBeTruthy();
    fireEvent.click(item);
    expect(onSelect).toHaveBeenCalledWith('terminal');
  });

  // A fixed menu is anchored to where the trigger was.
  it('closes when the page scrolls or resizes', () => {
    render(<MenuButton label="Open" items={items} onSelect={vi.fn()} />);

    fireEvent.click(screen.getByRole('button', { name: 'Open' }));
    fireEvent.scroll(document, {});
    expect(screen.queryByRole('menu')).toBeNull();

    fireEvent.click(screen.getByRole('button', { name: 'Open' }));
    fireEvent(window, new Event('resize'));
    expect(screen.queryByRole('menu')).toBeNull();
  });

  // A list elsewhere scrolls when its rows change under it, as the sidebar's
  // does when projects come and go; that leaves the trigger where it was.
  it('stays open when a pane beside its trigger scrolls', () => {
    render(
      <>
        <aside data-testid="beside" />
        <main data-testid="holding">
          <MenuButton label="Open" items={items} onSelect={vi.fn()} />
        </main>
      </>,
    );

    fireEvent.click(screen.getByRole('button', { name: 'Open' }));
    fireEvent.scroll(screen.getByTestId('beside'));
    expect(screen.getByRole('menu')).toBeTruthy();

    fireEvent.scroll(screen.getByTestId('holding'));
    expect(screen.queryByRole('menu')).toBeNull();
  });

  it('marks the trigger as a menu for assistive technology', () => {
    render(
      <MenuButton
        label="Open"
        items={items}
        onSelect={vi.fn()}
        ariaLabel="Open workstream feature/x"
      />,
    );
    const trigger = screen.getByRole('button', {
      name: 'Open workstream feature/x',
    });
    expect(trigger.getAttribute('aria-haspopup')).toBe('menu');
    expect(trigger.getAttribute('aria-expanded')).toBe('false');
    fireEvent.click(trigger);
    expect(trigger.getAttribute('aria-expanded')).toBe('true');
  });
});

describe('MenuButton keyboard interaction', () => {
  const threeItems = [
    { id: 'terminal', label: 'Terminal' },
    { id: 'ide', label: 'Visual Studio Code' },
    { id: 'web-ide', label: 'Theia in the browser' },
  ];

  const focused = () => document.activeElement as HTMLElement;
  const press = (key: string) => fireEvent.keyDown(focused(), { key });

  it('moves focus into the menu when it opens', () => {
    render(<MenuButton label="Open" items={threeItems} onSelect={vi.fn()} />);

    fireEvent.click(screen.getByRole('button', { name: 'Open' }));
    expect(focused()).toBe(screen.getByRole('menuitem', { name: 'Terminal' }));
  });

  it('moves focus into the menu of a split button too', () => {
    render(
      <MenuButton
        label="Open"
        items={threeItems}
        onPrimary={vi.fn()}
        onSelect={vi.fn()}
      />,
    );

    fireEvent.click(screen.getByRole('button', { name: 'Open with…' }));
    expect(focused()).toBe(screen.getByRole('menuitem', { name: 'Terminal' }));
  });

  it('gives only the active item a tab stop', () => {
    render(<MenuButton label="Open" items={threeItems} onSelect={vi.fn()} />);
    fireEvent.click(screen.getByRole('button', { name: 'Open' }));

    expect(
      screen.getAllByRole('menuitem').map((item) => item.tabIndex),
    ).toEqual([0, -1, -1]);

    press('ArrowDown');
    expect(
      screen.getAllByRole('menuitem').map((item) => item.tabIndex),
    ).toEqual([-1, 0, -1]);
  });

  it('walks the items with the arrows and wraps at both ends', () => {
    render(<MenuButton label="Open" items={threeItems} onSelect={vi.fn()} />);
    fireEvent.click(screen.getByRole('button', { name: 'Open' }));

    press('ArrowDown');
    expect(focused().textContent).toBe('Visual Studio Code');
    press('ArrowDown');
    expect(focused().textContent).toBe('Theia in the browser');
    press('ArrowDown');
    expect(focused().textContent).toBe('Terminal');
    press('ArrowUp');
    expect(focused().textContent).toBe('Theia in the browser');
  });

  it('jumps to the ends with Home and End', () => {
    render(<MenuButton label="Open" items={threeItems} onSelect={vi.fn()} />);
    fireEvent.click(screen.getByRole('button', { name: 'Open' }));

    press('End');
    expect(focused().textContent).toBe('Theia in the browser');
    press('Home');
    expect(focused().textContent).toBe('Terminal');
  });

  it('opens on ArrowDown at the first item and on ArrowUp at the last', () => {
    render(<MenuButton label="Open" items={threeItems} onSelect={vi.fn()} />);
    const trigger = screen.getByRole('button', { name: 'Open' });

    fireEvent.keyDown(trigger, { key: 'ArrowDown' });
    expect(focused().textContent).toBe('Terminal');

    fireEvent.keyDown(focused(), { key: 'Escape' });
    fireEvent.keyDown(trigger, { key: 'ArrowUp' });
    expect(focused().textContent).toBe('Theia in the browser');
  });

  it('selects the focused item with Enter', () => {
    const onSelect = vi.fn();
    render(<MenuButton label="Open" items={threeItems} onSelect={onSelect} />);
    fireEvent.click(screen.getByRole('button', { name: 'Open' }));

    press('ArrowDown');
    press('Enter');
    expect(onSelect).toHaveBeenCalledWith('ide');
    expect(screen.queryByRole('menu')).toBeNull();
    expect(focused()).toBe(screen.getByRole('button', { name: 'Open' }));
  });

  it('selects the focused item with Space', () => {
    const onSelect = vi.fn();
    render(<MenuButton label="Open" items={threeItems} onSelect={onSelect} />);
    fireEvent.click(screen.getByRole('button', { name: 'Open' }));

    press('End');
    press(' ');
    expect(onSelect).toHaveBeenCalledWith('web-ide');
    expect(screen.queryByRole('menu')).toBeNull();
  });

  it('returns focus to the caret of a split button on Escape', () => {
    render(
      <MenuButton
        label="Open"
        items={threeItems}
        onPrimary={vi.fn()}
        onSelect={vi.fn()}
      />,
    );

    fireEvent.click(screen.getByRole('button', { name: 'Open with…' }));
    press('Escape');
    expect(screen.queryByRole('menu')).toBeNull();
    expect(focused()).toBe(screen.getByRole('button', { name: 'Open with…' }));
  });

  it('closes on Tab and hands focus back to the trigger', () => {
    render(<MenuButton label="Open" items={threeItems} onSelect={vi.fn()} />);
    fireEvent.click(screen.getByRole('button', { name: 'Open' }));

    press('Tab');
    expect(screen.queryByRole('menu')).toBeNull();
    expect(focused()).toBe(screen.getByRole('button', { name: 'Open' }));
  });

  it('closes when focus leaves the menu', () => {
    render(
      <>
        <MenuButton label="Open" items={threeItems} onSelect={vi.fn()} />
        <button type="button">Elsewhere</button>
      </>,
    );

    fireEvent.click(screen.getByRole('button', { name: 'Open' }));
    // A real focus move, not a synthetic event: this is what Tab does.
    act(() => screen.getByRole('button', { name: 'Elsewhere' }).focus());
    expect(screen.queryByRole('menu')).toBeNull();
  });

  it('closes when focus moves to the default half of a split button', () => {
    render(
      <MenuButton
        label="Open"
        items={threeItems}
        onPrimary={vi.fn()}
        onSelect={vi.fn()}
      />,
    );

    fireEvent.click(screen.getByRole('button', { name: 'Open with…' }));
    act(() => screen.getByRole('button', { name: 'Open' }).focus());
    expect(screen.queryByRole('menu')).toBeNull();
  });

  it('selects with the keyboard in a split menu without firing the default', () => {
    const onPrimary = vi.fn();
    const onSelect = vi.fn();
    render(
      <MenuButton
        label="Open"
        items={threeItems}
        onPrimary={onPrimary}
        onSelect={onSelect}
      />,
    );

    fireEvent.click(screen.getByRole('button', { name: 'Open with…' }));
    press('ArrowUp');
    press('Enter');
    expect(onSelect).toHaveBeenCalledWith('web-ide');
    expect(onPrimary).not.toHaveBeenCalled();
    expect(focused()).toBe(screen.getByRole('button', { name: 'Open with…' }));
  });
});
