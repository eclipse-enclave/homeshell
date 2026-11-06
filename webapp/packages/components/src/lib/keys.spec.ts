import type { KeyboardEvent } from 'react';
import {
  ariaKeys,
  formatKeys,
  isDeleteKey,
  isMac,
  isTextEntry,
  nextIndex,
  platformKeys,
} from './keys';
import { LIST_KEYS } from './list-navigation';

describe('formatKeys', () => {
  it.each([
    ['mod+shift+p', 'Ctrl+Shift+P', '⇧⌘P'],
    ['mod+,', 'Ctrl+,', '⌘,'],
    ['f1', 'F1', 'F1'],
    ['mod+k mod+s', 'Ctrl+K Ctrl+S', '⌘K ⌘S'],
    ['arrowup', '↑', '↑'],
    ['escape', 'Esc', 'Esc'],
    ['mod+backspace', 'Ctrl+Backspace', '⌘⌫'],
    ['mod+shift+`', 'Ctrl+Shift+`', '⇧⌘`'],
    ['ctrl+shift+`', 'Ctrl+Shift+`', '⌃⇧`'],
  ])('shows %s as %s, or %s on a Mac', (keys, others, mac) => {
    expect(formatKeys(keys, false)).toBe(others);
    expect(formatKeys(keys, true)).toBe(mac);
  });
});

describe('ariaKeys', () => {
  it.each([
    ['arrowup', 'ArrowUp', 'ArrowUp'],
    ['f2', 'F2', 'F2'],
    ['shift+f10', 'Shift+F10', 'Shift+F10'],
    ['mod+backspace', 'Control+Backspace', 'Meta+Backspace'],
    ['ctrl+alt+delete', 'Control+Alt+Delete', 'Control+Alt+Delete'],
  ])('names %s %s, or %s on a Mac', (keys, others, mac) => {
    expect(ariaKeys(keys, false)).toBe(others);
    expect(ariaKeys(keys, true)).toBe(mac);
  });
});

describe('isMac', () => {
  afterEach(() => vi.restoreAllMocks());

  it.each([
    ['Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0)', true],
    ['Mozilla/5.0 (X11; Linux x86_64)', false],
  ])('reads %s as a Mac: %s', (userAgent, mac) => {
    vi.spyOn(navigator, 'userAgent', 'get').mockReturnValue(userAgent);
    expect(isMac()).toBe(mac);
  });
});

describe('isDeleteKey', () => {
  const deleteKeys = LIST_KEYS.find((key) => key.label.startsWith('Delete'));

  // What the list describes as deleting is what deletes, on either platform.
  it.each([
    ['elsewhere', false, ['Delete']],
    ['on a Mac', true, ['Delete', 'Meta+Backspace']],
  ])('takes the delete keys described %s', (_platform, mac, expected) => {
    const described = (deleteKeys ? platformKeys(deleteKeys, mac) : []).map(
      (keys) => ariaKeys(keys, mac),
    );
    expect(described).toEqual(expected);
    for (const keys of described) {
      const parts = keys.split('+');
      const event = { key: parts.at(-1), metaKey: parts.includes('Meta') };
      expect(isDeleteKey(event as KeyboardEvent, mac)).toBe(true);
    }
  });

  it('takes no Cmd+Backspace off a Mac', () => {
    const event = { key: 'Backspace', metaKey: true } as KeyboardEvent;
    expect(isDeleteKey(event, false)).toBe(false);
  });
});

describe('isTextEntry', () => {
  const editable = () => {
    const element = document.createElement('div');
    element.contentEditable = 'true';
    // jsdom does not derive isContentEditable from contentEditable.
    Object.defineProperty(element, 'isContentEditable', { value: true });
    return element;
  };

  it.each([
    ['an input', () => document.createElement('input'), true],
    ['a text area', () => document.createElement('textarea'), true],
    ['a select', () => document.createElement('select'), true],
    ['an editable element', editable, true],
    ['a button', () => document.createElement('button'), false],
    ['the window', () => window, false],
    ['nothing', () => null, false],
  ])('takes %s as text entry: %s', (_target, target, expected) => {
    expect(isTextEntry(target())).toBe(expected);
  });
});

describe('nextIndex', () => {
  it.each([
    ['ArrowDown', 1, {}, 2],
    ['ArrowUp', 1, {}, 0],
    ['Home', 2, {}, 0],
    ['End', 0, {}, 3],
    ['ArrowDown', 3, {}, undefined],
    ['ArrowUp', 0, {}, undefined],
    ['ArrowDown', 3, { wrap: true }, 0],
    ['ArrowUp', 0, { wrap: true }, 3],
    ['ArrowRight', 1, {}, undefined],
    ['ArrowRight', 1, { orientation: 'horizontal' }, 2],
    ['ArrowDown', 1, { orientation: 'horizontal' }, undefined],
    ['ArrowLeft', 1, { orientation: 'both' }, 0],
    ['ArrowDown', 1, { orientation: 'both' }, 2],
    ['Enter', 1, {}, undefined],
  ] as const)(
    '%s from %i of four with %o goes to %s',
    (key, index, options, expected) => {
      expect(nextIndex(key, index, 4, options)).toBe(expected);
    },
  );

  it('goes nowhere among no items', () => {
    expect(nextIndex('Home', 0, 0)).toBeUndefined();
  });
});
