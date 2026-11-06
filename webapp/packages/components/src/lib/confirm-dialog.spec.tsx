import { fireEvent, render, screen } from '@testing-library/react';
import type { ReactNode } from 'react';

import { Button } from './button';
import { ConfirmDialog } from './confirm-dialog';

function renderDialog({
  actions = (
    <>
      <Button>Cancel</Button>
      <Button>OK</Button>
    </>
  ),
  children = <p>Body</p>,
  onClose = vi.fn(),
}: { actions?: ReactNode; children?: ReactNode; onClose?: () => void } = {}) {
  render(
    <ConfirmDialog title="Sure?" actions={actions} onClose={onClose}>
      {children}
    </ConfirmDialog>,
  );
  return onClose;
}

const button = (name: string) => screen.getByRole('button', { name });

describe('ConfirmDialog', () => {
  it('focuses the button marked for it', () => {
    renderDialog({
      actions: (
        <>
          <Button>Cancel</Button>
          <Button data-initial-focus>OK</Button>
        </>
      ),
    });
    expect(document.activeElement).toBe(button('OK'));
  });

  it('skips a disabled button marked for it', () => {
    renderDialog({
      actions: (
        <>
          <Button>Cancel</Button>
          <Button data-initial-focus disabled>
            OK
          </Button>
        </>
      ),
    });
    expect(document.activeElement).toBe(button('Cancel'));
  });

  it('otherwise focuses the first enabled button', () => {
    renderDialog({
      actions: (
        <>
          <Button disabled>Cancel</Button>
          <Button>OK</Button>
        </>
      ),
    });
    expect(document.activeElement).toBe(button('OK'));
  });

  it('gives focus back to what had it when it closes', () => {
    const { rerender } = render(<button type="button">Opener</button>);
    button('Opener').focus();
    rerender(
      <>
        <button type="button">Opener</button>
        <ConfirmDialog
          title="Sure?"
          actions={<Button data-initial-focus>OK</Button>}
          onClose={vi.fn()}
        >
          <p>Body</p>
        </ConfirmDialog>
      </>,
    );
    expect(document.activeElement).toBe(button('OK'));
    rerender(<button type="button">Opener</button>);
    expect(document.activeElement).toBe(button('Opener'));
  });

  it('gives focus to its fallback when what had it has gone', () => {
    const { rerender } = render(<button type="button">Opener</button>);
    button('Opener').focus();
    const dialog = (
      <ConfirmDialog
        title="Sure?"
        actions={<Button>OK</Button>}
        onClose={vi.fn()}
        fallbackFocus={() => screen.getByRole('button', { name: 'Item' })}
      >
        <p>Body</p>
      </ConfirmDialog>
    );
    rerender(
      <>
        <button type="button">Item</button>
        {dialog}
      </>,
    );
    rerender(<button type="button">Item</button>);
    expect(document.activeElement).toBe(button('Item'));
  });

  it('closes on Escape, and takes it', () => {
    const onClose = renderDialog();
    expect(fireEvent.keyDown(button('OK'), { key: 'Escape' })).toBe(false);
    expect(onClose).toHaveBeenCalled();
  });

  describe('focus trap', () => {
    const withFields = () =>
      renderDialog({
        children: (
          <>
            <a href="#help">Help</a>
            <select aria-label="Mode" />
            <textarea aria-label="Note" />
          </>
        ),
        actions: (
          <>
            <Button>Cancel</Button>
            <Button>OK</Button>
            <Button disabled>Later</Button>
          </>
        ),
      });

    it('wraps Tab from the last enabled control to the first', () => {
      withFields();
      button('OK').focus();
      fireEvent.keyDown(button('OK'), { key: 'Tab' });
      expect(document.activeElement).toBe(
        screen.getByRole('link', { name: 'Help' }),
      );
    });

    it('wraps Shift+Tab from the first control to the last enabled one', () => {
      withFields();
      const help = screen.getByRole('link', { name: 'Help' });
      help.focus();
      fireEvent.keyDown(help, { key: 'Tab', shiftKey: true });
      expect(document.activeElement).toBe(button('OK'));
    });

    it('lets Tab move normally between the ends', () => {
      withFields();
      const mode = screen.getByRole('combobox', { name: 'Mode' });
      mode.focus();
      expect(fireEvent.keyDown(mode, { key: 'Tab' })).toBe(true);
    });

    it.each([
      [false, 'Help'],
      [true, 'OK'],
    ])('wraps from the overlay itself (Shift: %s)', (shiftKey, name) => {
      withFields();
      const dialog = screen.getByRole('dialog');
      dialog.focus();
      fireEvent.keyDown(dialog, { key: 'Tab', shiftKey });
      expect(document.activeElement).toBe(
        screen.getByRole(name === 'Help' ? 'link' : 'button', { name }),
      );
    });
  });

  describe('footer keys', () => {
    const three = () =>
      renderDialog({
        actions: (
          <>
            <Button>Cancel</Button>
            <Button disabled>Skip</Button>
            <Button>Keep</Button>
            <Button>Delete</Button>
          </>
        ),
      });

    it.each([
      ['ArrowRight', 'Keep', 'Delete'],
      ['ArrowLeft', 'Keep', 'Cancel'],
      ['Home', 'Delete', 'Cancel'],
      ['End', 'Cancel', 'Delete'],
    ])('%s from %s goes to %s, past disabled ones', (key, from, to) => {
      three();
      button(from).focus();
      fireEvent.keyDown(button(from), { key });
      expect(document.activeElement).toBe(button(to));
    });

    it('stops at the ends', () => {
      three();
      button('Delete').focus();
      expect(fireEvent.keyDown(button('Delete'), { key: 'ArrowRight' })).toBe(
        true,
      );
      expect(document.activeElement).toBe(button('Delete'));
    });

    it('leaves the arrows alone in the body', () => {
      renderDialog({ children: <input aria-label="Name" /> });
      const input = screen.getByRole('textbox', { name: 'Name' });
      input.focus();
      expect(fireEvent.keyDown(input, { key: 'ArrowLeft' })).toBe(true);
      expect(document.activeElement).toBe(input);
    });
  });

  it('keeps its keys from the elements it was opened over', () => {
    const onKeyDown = vi.fn();
    render(
      <div onKeyDown={onKeyDown}>
        <ConfirmDialog
          title="Sure?"
          actions={<Button>OK</Button>}
          onClose={vi.fn()}
        >
          <p>Body</p>
        </ConfirmDialog>
      </div>,
    );
    fireEvent.keyDown(button('OK'), { key: 'F2' });
    fireEvent.keyDown(button('OK'), { key: 'Escape' });
    expect(onKeyDown).not.toHaveBeenCalled();
  });
});
