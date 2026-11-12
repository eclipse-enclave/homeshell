import { fireEvent, render, renderHook, screen } from '@testing-library/react';
import { useEffect, useRef, useState } from 'react';

import { useMnemonics } from './mnemonic';
import {
  type RenamableTitleOptions,
  useRenamableTitle,
} from './renamable-title';

function Title(props: RenamableTitleOptions) {
  const titleProps = useRenamableTitle<HTMLHeadingElement>(props);
  return <h1 {...titleProps}>{props.name}</h1>;
}

/** A title that turns into a focused box when renamed, as each real one does. */
function EditableTitle() {
  const [editing, setEditing] = useState(false);
  const inputRef = useRef<HTMLInputElement>(null);
  const titleProps = useRenamableTitle<HTMLHeadingElement>({
    name: 'homeshell',
    onRename: () => setEditing(true),
  });
  // A browser's select() focuses the box too; jsdom's does not.
  useEffect(() => {
    if (!editing) return;
    inputRef.current?.focus();
    inputRef.current?.select();
  }, [editing]);
  return editing ? (
    <input ref={inputRef} aria-label="Name" defaultValue="homeshell" />
  ) : (
    <h1 {...titleProps}>homeshell</h1>
  );
}

function renderTitle(props: Partial<RenamableTitleOptions> = {}) {
  const onRename = vi.fn();
  const onClick = vi.fn();
  render(
    <header data-mnemonic-scope="page" onClick={onClick}>
      <Title name="homeshell" onRename={onRename} {...props} />
    </header>,
  );
  renderHook(() => useMnemonics());
  return { onRename, onClick, title: screen.getByRole('heading') };
}

describe('useRenamableTitle', () => {
  it('renames on a double-click', () => {
    const { onRename, title } = renderTitle();
    fireEvent.doubleClick(title);
    expect(onRename).toHaveBeenCalledTimes(1);
  });

  // A click on a title selects what it is on; Alt+R must rename instead.
  it('renames on Alt+R, without a click', () => {
    const { onRename, onClick } = renderTitle();
    fireEvent.keyDown(document.body, { key: 'r', code: 'KeyR', altKey: true });
    expect(onRename).toHaveBeenCalledTimes(1);
    expect(onClick).not.toHaveBeenCalled();
  });

  // Keys typed straight after Alt+R go to whatever has focus: the box must be
  // there, and focused, before the next key arrives. So no act() or await
  // between the key and the check — fireEvent would wrap it in act(), which
  // renders whatever is pending and would hide a rename that starts late.
  it('starts the rename on Alt+R synchronously, with the box focused', () => {
    render(
      <header data-mnemonic-scope="page">
        <EditableTitle />
      </header>,
    );
    renderHook(() => useMnemonics());
    const actEnvironment = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };
    const wasActEnvironment = actEnvironment.IS_REACT_ACT_ENVIRONMENT;
    actEnvironment.IS_REACT_ACT_ENVIRONMENT = false;
    try {
      document.body.dispatchEvent(
        new KeyboardEvent('keydown', {
          key: 'r',
          code: 'KeyR',
          altKey: true,
          bubbles: true,
          cancelable: true,
        }),
      );
      const box = screen.queryByRole('textbox', { name: 'Name' });
      expect(box).not.toBeNull();
      expect(document.activeElement).toBe(box);
    } finally {
      actEnvironment.IS_REACT_ACT_ENVIRONMENT = wasActEnvironment;
    }
  });

  it('carries the R mnemonic and says so to a screen reader', () => {
    const { title } = renderTitle();
    expect(title.getAttribute('data-mnemonic')).toBe('r');
    expect(title.getAttribute('aria-keyshortcuts')).toBe('Alt+R');
  });

  it("carries the title's own class with the tint's", () => {
    const { title } = renderTitle({ className: 'name' });
    expect(title.classList).toContain('name');
    expect(title.classList.length).toBe(2);
  });

  it.each([
    [false, 'homeshell\n\nDouble-click to rename'],
    [true, 'homeshell\n\nDouble-click to rename (or press F2)'],
  ])('with F2 %s, says how to rename in its tooltip', (f2, tooltip) => {
    const { title } = renderTitle({ f2 });
    expect(title.getAttribute('title')).toBe(tooltip);
  });

  describe('while it cannot be renamed', () => {
    it('says why, with neither the tint nor the letter', () => {
      const { title } = renderTitle({
        disabledReason: 'Offline',
        className: 'name',
      });
      expect(title.getAttribute('title')).toBe('homeshell\n\nOffline');
      expect(title.className).toBe('name');
      expect(title.hasAttribute('data-mnemonic')).toBe(false);
      expect(title.hasAttribute('aria-keyshortcuts')).toBe(false);
    });

    it('ignores a double-click and Alt+R', () => {
      const { onRename, onClick, title } = renderTitle({
        disabledReason: 'Offline',
      });
      fireEvent.doubleClick(title);
      fireEvent.keyDown(document.body, {
        key: 'r',
        code: 'KeyR',
        altKey: true,
      });
      expect(onRename).not.toHaveBeenCalled();
      expect(onClick).not.toHaveBeenCalled();
    });
  });
});
