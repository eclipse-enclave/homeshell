import {
  act,
  fireEvent,
  render,
  renderHook,
  screen,
} from '@testing-library/react';

import {
  MNEMONIC_EVENT,
  MNEMONICS,
  MnemonicLabel,
  mnemonicIndex,
  assignMnemonics,
  mnemonicScope,
  reachableMnemonics,
  useMnemonics,
  withShortcuts,
} from './mnemonic';

describe('assignMnemonics', () => {
  it('gives each label the first letter left free', () => {
    expect(assignMnemonics(['Notes'], ['o', 'n', 'r', 'd'])).toEqual(['t']);
    expect(assignMnemonics(['Archive'], [])).toEqual(['a']);
  });

  it('keeps a letter given to one label from the next', () => {
    expect(assignMnemonics(['Notes', 'Tests'], ['n', 'o'])).toEqual(['t', 'e']);
  });

  it('skips what is not a letter or digit', () => {
    expect(assignMnemonics(['…  2nd'], [])).toEqual(['2']);
  });

  it('gives none when every letter is taken', () => {
    expect(assignMnemonics(['Do'], ['d', 'o'])).toEqual([undefined]);
  });
});

describe('mnemonicIndex', () => {
  // The D of "Stop and delete", not the one ending "and".
  it('prefers a letter that starts a word', () => {
    expect(mnemonicIndex('Stop and delete', 'd')).toBe(9);
    expect(mnemonicIndex('New session', 'n')).toBe(0);
  });

  it('falls back to the first occurrence, or -1', () => {
    expect(mnemonicIndex('Notes', 't')).toBe(2);
    expect(mnemonicIndex('Notes', 'x')).toBe(-1);
  });
});

describe('withShortcuts', () => {
  it('names the keys after the title, the mnemonic last', () => {
    expect(withShortcuts('Rename', ['f2'], 'r', false)).toBe(
      'Rename (F2, Alt+R)',
    );
  });

  it('writes them as a Mac does there', () => {
    expect(withShortcuts('Open', [], 'o', true)).toBe('Open (⌥O)');
  });

  it('leaves a title without keys alone, and keys without a title bare', () => {
    expect(withShortcuts('Open', [], undefined, false)).toBe('Open');
    expect(withShortcuts(undefined, [], 'o', false)).toBe('Alt+O');
  });
});

describe('MnemonicLabel', () => {
  // One element: as pieces, a flex button's gap split "Open" into "O pen".
  it('marks the letter, and only it, in one element', () => {
    const { container } = render(<MnemonicLabel label="Open" letter="o" />);
    expect(container.childNodes).toHaveLength(1);
    expect(container.textContent).toBe('Open');
    expect(container.querySelectorAll('span span')).toHaveLength(1);
    expect(container.querySelector('span span')?.textContent).toBe('O');
  });

  it('leaves a label without the letter as it is', () => {
    const { container } = render(<MnemonicLabel label="Open" letter="x" />);
    expect(container.innerHTML).toBe('Open');
  });
});

/** A page with a header, a list item holding a nested one, and a free button. */
function renderPage({
  selected = 'card',
  dialog = false,
  menu = false,
  loading = false,
}: {
  selected?: 'card' | 'row' | 'none';
  dialog?: boolean;
  menu?: boolean;
  loading?: boolean;
} = {}) {
  const onClick = {
    pageNew: vi.fn(),
    pageDelete: vi.fn(),
    cardOpen: vi.fn(),
    cardDelete: vi.fn(),
    rowStop: vi.fn(),
    formCancel: vi.fn(),
    dialogDelete: vi.fn(),
  };
  render(
    <>
      <header data-mnemonic-scope="page">
        <button data-mnemonic="w" onClick={onClick.pageNew}>
          New workstream
        </button>
        <button data-mnemonic="d" onClick={onClick.pageDelete}>
          Delete project
        </button>
      </header>
      <section
        data-mnemonic-scope="item"
        aria-current={selected === 'card' || undefined}
        aria-label="card"
      >
        <button data-mnemonic="o" onClick={onClick.cardOpen}>
          Open card
        </button>
        <button data-mnemonic="n" disabled>
          New session
        </button>
        <button data-mnemonic="d" onClick={onClick.cardDelete}>
          Delete card
        </button>
        <form aria-label="New session">
          <input aria-label="Title" />
          <button type="button" data-mnemonic="c" onClick={onClick.formCancel}>
            Cancel
          </button>
          <button type="submit">Create</button>
        </form>
        <ul>
          <li
            data-mnemonic-scope="item"
            aria-current={selected === 'row' || undefined}
          >
            <button data-mnemonic="s" onClick={onClick.rowStop}>
              Stop row
            </button>
            <button data-mnemonic="o" aria-disabled="true">
              Open row
            </button>
            <form aria-label="Prompt">
              <input aria-label="Prompt text" />
              <button type="submit">Send</button>
            </form>
          </li>
        </ul>
      </section>
      <button data-mnemonic="x">Outside any scope</button>
      <input aria-label="Search" />
      {loading && <p data-mnemonic-scope="modal">Loading settings…</p>}
      {menu && (
        <div role="menu">
          <button role="menuitem">Open in terminal</button>
        </div>
      )}
      {dialog && (
        <div role="dialog" aria-modal="true">
          <input aria-label="Reason" />
          <button data-mnemonic="d" onClick={onClick.dialogDelete}>
            Delete for good
          </button>
        </div>
      )}
    </>,
  );
  return onClick;
}

const reachableNames = () =>
  Object.fromEntries(
    [...reachableMnemonics().entries()].map(([letter, element]) => [
      letter,
      element.textContent,
    ]),
  );

describe('reachableMnemonics', () => {
  it('reaches the selection first, then the page for letters it leaves', () => {
    renderPage();
    expect(reachableNames()).toEqual({
      o: 'Open card',
      d: 'Delete card',
      w: 'New workstream',
    });
  });

  // A card's buttons are its own; its rows' belong to the rows.
  it('leaves out what a nested scope owns', () => {
    renderPage();
    expect(reachableNames()).not.toHaveProperty('s');
  });

  // The row has no Delete, but D is still an item's letter: the project's
  // Delete is not what the user meant.
  it('reaches a selected row, none of its card, and no item letter of the page', () => {
    renderPage({ selected: 'row' });
    expect(reachableNames()).toEqual({
      s: 'Stop row',
      w: 'New workstream',
    });
  });

  it('keeps a letter of a disabled button from the page', () => {
    render(
      <>
        <header data-mnemonic-scope="page">
          <button data-mnemonic="k">Page</button>
        </header>
        <section data-mnemonic-scope="item" aria-current="true">
          <button data-mnemonic="k" disabled>
            Item
          </button>
        </section>
      </>,
    );
    expect(reachableNames()).toEqual({});
  });

  it("keeps an item's letters from the page though it lacks their buttons", () => {
    render(
      <>
        <header {...mnemonicScope('page')}>
          <button data-mnemonic={MNEMONICS.rename}>Rename project</button>
          <button data-mnemonic="w">New workstream</button>
        </header>
        <section {...mnemonicScope('item')} aria-current="true" />
      </>,
    );
    expect(reachableNames()).toEqual({ w: 'New workstream' });
  });

  it('skips a disabled button', () => {
    renderPage();
    expect(reachableNames()).not.toHaveProperty('n');
  });

  it('skips an aria-disabled one', () => {
    renderPage({ selected: 'row' });
    expect(reachableNames()).not.toHaveProperty('o');
  });

  // Focus on a control outside the list is work on something else: the
  // selection's letters, Delete among them, are not what the user meant.
  it('reaches only the page while focus is on a control outside the list', () => {
    renderPage();
    screen.getByRole('button', { name: 'New workstream' }).focus();
    expect(reachableNames()).toEqual({
      w: 'New workstream',
      d: 'Delete project',
    });
  });

  it('reaches the selection while focus is inside it', () => {
    renderPage();
    screen.getByRole('button', { name: 'Open card' }).focus();
    expect(reachableNames()).toHaveProperty('d', 'Delete card');
  });

  // A form is work of its own: Alt+D in one inside the card must not delete it.
  it('reaches only a form while focus is in it', () => {
    renderPage();
    screen.getByRole('button', { name: 'Create' }).focus();
    expect(reachableNames()).toEqual({ c: 'Cancel' });
  });

  // One with no letters of its own takes none: the page's are still in reach,
  // though not those of the item it is in.
  it('reaches the page past a form without letters, not its item', () => {
    renderPage({ selected: 'row' });
    screen.getByRole('button', { name: 'Send' }).focus();
    expect(reachableNames()).toEqual({
      w: 'New workstream',
      d: 'Delete project',
    });
  });

  it('reaches only the page with nothing selected', () => {
    renderPage({ selected: 'none' });
    expect(reachableNames()).toEqual({
      w: 'New workstream',
      d: 'Delete project',
    });
  });

  // Nothing behind a modal is in reach.
  it('reaches only an open dialog', () => {
    renderPage({ dialog: true });
    expect(reachableNames()).toEqual({ d: 'Delete for good' });
  });

  // An open menu is not inside any scope, but the page behind it is not what
  // a letter typed at it means.
  it('reaches nothing while a menu is open', () => {
    renderPage({ menu: true });
    expect(reachableNames()).toEqual({});
  });

  // A letter meant for the dialog is not the page's while it loads.
  it('reaches nothing while a dialog is loading', () => {
    renderPage({ loading: true });
    expect(reachableNames()).toEqual({});
  });
});

describe('useMnemonics', () => {
  const shown = () =>
    Array.from(document.querySelectorAll('[data-mnemonic-shown]')).map(
      (element) => element.textContent,
    );

  it('marks what Alt reaches while it is held', () => {
    renderPage();
    renderHook(() => useMnemonics());
    fireEvent.keyDown(document.body, { key: 'Alt', altKey: true });
    expect(shown()).toEqual(['New workstream', 'Open card', 'Delete card']);

    fireEvent.keyUp(document.body, { key: 'Alt' });
    expect(shown()).toEqual([]);
  });

  it('marks once for an Alt held down, not on every repeat', () => {
    renderPage();
    renderHook(() => useMnemonics());
    fireEvent.keyDown(document.body, { key: 'Alt', altKey: true });
    screen
      .getByText('Delete card')
      .closest('button')
      ?.setAttribute('disabled', '');
    fireEvent.keyDown(document.body, {
      key: 'Alt',
      altKey: true,
      repeat: true,
    });
    expect(shown()).toEqual(['New workstream', 'Open card', 'Delete card']);
  });

  it('clears the marks when the window loses focus', () => {
    renderPage();
    renderHook(() => useMnemonics());
    fireEvent.keyDown(document.body, { key: 'Alt', altKey: true });
    act(() => {
      window.dispatchEvent(new Event('blur'));
    });
    expect(shown()).toEqual([]);
  });

  it('presses the button a letter reaches, by where the key sits', () => {
    const onClick = renderPage();
    renderHook(() => useMnemonics());
    // A Mac's Option+O types ø; the key is still KeyO.
    const event = fireEvent.keyDown(document.body, {
      key: 'ø',
      code: 'KeyO',
      altKey: true,
    });
    expect(onClick.cardOpen).toHaveBeenCalledTimes(1);
    expect(event).toBe(false);
  });

  // On AZERTY the key labelled W sits where a US keyboard has Z.
  it('presses by the letter the key types, whatever the layout', () => {
    const onClick = renderPage();
    renderHook(() => useMnemonics());
    fireEvent.keyDown(document.body, { key: 'w', code: 'KeyZ', altKey: true });
    expect(onClick.pageNew).toHaveBeenCalledTimes(1);
  });

  it('presses once for a key held down', () => {
    const onClick = renderPage();
    renderHook(() => useMnemonics());
    fireEvent.keyDown(document.body, {
      key: 'o',
      code: 'KeyO',
      altKey: true,
      repeat: true,
    });
    expect(onClick.cardOpen).not.toHaveBeenCalled();
  });

  it('leaves a letter nothing reaches to the page', () => {
    const onClick = renderPage();
    renderHook(() => useMnemonics());
    const event = fireEvent.keyDown(document.body, {
      key: 'q',
      code: 'KeyQ',
      altKey: true,
    });
    expect(event).toBe(true);
    expect(
      Object.values(onClick).every((fn) => fn.mock.calls.length === 0),
    ).toBe(true);
  });

  it('leaves Alt+Shift without an upper-case letter, and AltGr, alone', () => {
    const onClick = renderPage();
    renderHook(() => useMnemonics());
    fireEvent.keyDown(document.body, {
      key: 'O',
      code: 'KeyO',
      altKey: true,
      shiftKey: true,
    });
    fireEvent.keyDown(document.body, {
      key: 'o',
      code: 'KeyO',
      altKey: true,
      ctrlKey: true,
    });
    expect(onClick.cardOpen).not.toHaveBeenCalled();
  });

  // Where Option types characters on a Mac.
  it('leaves a text field outside a dialog alone', () => {
    const onClick = renderPage();
    renderHook(() => useMnemonics());
    fireEvent.keyDown(screen.getByRole('textbox', { name: 'Search' }), {
      key: 'o',
      code: 'KeyO',
      altKey: true,
    });
    expect(onClick.cardOpen).not.toHaveBeenCalled();
  });

  it('presses a form button from a text field in the form', () => {
    const onClick = renderPage();
    renderHook(() => useMnemonics());
    const title = screen.getByRole('textbox', { name: 'Title' });
    title.focus();
    fireEvent.keyDown(title, { key: 'c', code: 'KeyC', altKey: true });
    expect(onClick.formCancel).toHaveBeenCalledTimes(1);
  });

  it('presses a page button from a form without letters', () => {
    const onClick = renderPage();
    renderHook(() => useMnemonics());
    const send = screen.getByRole('button', { name: 'Send' });
    send.focus();
    fireEvent.keyDown(send, { key: 'w', code: 'KeyW', altKey: true });
    expect(onClick.pageNew).toHaveBeenCalledTimes(1);
  });

  it('leaves a text field in a form without letters alone', () => {
    const onClick = renderPage();
    renderHook(() => useMnemonics());
    const text = screen.getByRole('textbox', { name: 'Prompt text' });
    text.focus();
    fireEvent.keyDown(text, { key: 'w', code: 'KeyW', altKey: true });
    expect(onClick.pageNew).not.toHaveBeenCalled();
  });

  it('presses a letter in upper case with Alt+Shift, and only it', () => {
    const lower = vi.fn();
    const upper = vi.fn();
    render(
      <header {...mnemonicScope('page')}>
        <button data-mnemonic="o" onClick={lower}>
          Open
        </button>
        <button data-mnemonic="O" onClick={upper}>
          Open with…
        </button>
      </header>,
    );
    renderHook(() => useMnemonics());
    fireEvent.keyDown(document.body, {
      key: 'O',
      code: 'KeyO',
      altKey: true,
      shiftKey: true,
    });
    expect(upper).toHaveBeenCalledTimes(1);
    expect(lower).not.toHaveBeenCalled();
  });

  it('presses a dialog button from a text field in the dialog', () => {
    const onClick = renderPage({ dialog: true });
    renderHook(() => useMnemonics());
    fireEvent.keyDown(screen.getByRole('textbox', { name: 'Reason' }), {
      key: 'd',
      code: 'KeyD',
      altKey: true,
    });
    expect(onClick.dialogDelete).toHaveBeenCalledTimes(1);
  });

  // Option types characters on a Mac, in a dialog's text field too.
  it('leaves a text field in a dialog alone on a Mac', () => {
    const userAgent = vi
      .spyOn(navigator, 'userAgent', 'get')
      .mockReturnValue('Macintosh');
    const onClick = renderPage({ dialog: true });
    renderHook(() => useMnemonics());
    fireEvent.keyDown(screen.getByRole('textbox', { name: 'Reason' }), {
      key: '∂',
      code: 'KeyD',
      altKey: true,
    });
    userAgent.mockRestore();
    expect(onClick.dialogDelete).not.toHaveBeenCalled();
  });

  it('presses a dialog button, and nothing behind it', () => {
    const onClick = renderPage({ dialog: true });
    renderHook(() => useMnemonics());
    fireEvent.keyDown(document.body, { key: 'd', code: 'KeyD', altKey: true });
    expect(onClick.dialogDelete).toHaveBeenCalledTimes(1);
    expect(onClick.cardDelete).not.toHaveBeenCalled();
  });

  it('stops listening once unmounted', () => {
    const onClick = renderPage();
    const { unmount } = renderHook(() => useMnemonics());
    unmount();
    fireEvent.keyDown(document.body, { key: 'o', code: 'KeyO', altKey: true });
    expect(onClick.cardOpen).not.toHaveBeenCalled();
  });
});

// What a click does not activate — a title, which a click selects — takes the
// letter by cancelling the event raised on it before the click.
describe('useMnemonics on what takes the mnemonic event', () => {
  function renderTitle({ disabled = false } = {}) {
    const onClick = vi.fn();
    const onMnemonic = vi.fn((event: Event) => event.preventDefault());
    const onButton = vi.fn();
    render(
      <section data-mnemonic-scope="item" aria-current="true">
        <span
          data-testid="title"
          data-mnemonic="r"
          aria-disabled={disabled || undefined}
          onClick={onClick}
          ref={(element) => {
            element?.addEventListener(MNEMONIC_EVENT, onMnemonic);
          }}
        >
          api-fix
        </span>
        <button data-mnemonic="o" onClick={onButton}>
          Open
        </button>
      </section>,
    );
    renderHook(() => useMnemonics());
    return { onClick, onMnemonic, onButton };
  }

  it('raises the event on it and does not click it', () => {
    const { onClick, onMnemonic } = renderTitle();
    const event = fireEvent.keyDown(document.body, {
      key: 'r',
      code: 'KeyR',
      altKey: true,
    });
    expect(onMnemonic).toHaveBeenCalledTimes(1);
    expect(onClick).not.toHaveBeenCalled();
    expect(event).toBe(false);
  });

  it('does not reach it while it is aria-disabled', () => {
    const { onClick, onMnemonic } = renderTitle({ disabled: true });
    fireEvent.keyDown(document.body, { key: 'r', code: 'KeyR', altKey: true });
    expect(onMnemonic).not.toHaveBeenCalled();
    expect(onClick).not.toHaveBeenCalled();
    expect(reachableMnemonics().has('r')).toBe(false);
  });

  it('marks it while Alt is held, for its tint', () => {
    renderTitle();
    fireEvent.keyDown(document.body, { key: 'Alt', altKey: true });
    expect(
      screen.getByTestId('title').hasAttribute('data-mnemonic-shown'),
    ).toBe(true);
  });

  it('still clicks a button, which leaves the event alone', () => {
    const { onButton } = renderTitle();
    fireEvent.keyDown(document.body, { key: 'o', code: 'KeyO', altKey: true });
    expect(onButton).toHaveBeenCalledTimes(1);
  });
});
