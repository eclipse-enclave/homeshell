import {
  CHORD_TIMEOUT_MS,
  CONSUMED,
  createKeybindingResolver,
} from './keymap';

const key = (
  init: KeyboardEventInit & { key: string },
  target: EventTarget = document.body,
) => {
  const event = new KeyboardEvent('keydown', init);
  Object.defineProperty(event, 'target', { value: target });
  return event;
};

describe('createKeybindingResolver', () => {
  const bindings = [
    { keys: ['mod+shift+p'], command: 'palette' },
    { keys: ['f1'], command: 'palette' },
    { keys: ['mod+k mod+s'], command: 'shortcuts' },
    { keys: ['g'], command: 'plain' },
  ];

  it.each([
    ['Ctrl on Linux and Windows', false, { ctrlKey: true }],
    ['Cmd on a Mac', true, { metaKey: true }],
  ])('reads mod as %s', (_platform, mac, modifier) => {
    const { resolve } = createKeybindingResolver(bindings, mac);
    expect(resolve(key({ key: 'P', shiftKey: true, ...modifier }))).toBe(
      'palette',
    );
  });

  it.each([
    ['without Shift', { key: 'p', ctrlKey: true }],
    [
      'with the other platform’s modifier',
      { key: 'P', metaKey: true, shiftKey: true },
    ],
    [
      'with an extra Alt',
      { key: 'P', ctrlKey: true, shiftKey: true, altKey: true },
    ],
  ])('does not match %s', (_case, init) => {
    expect(
      createKeybindingResolver(bindings, false).resolve(key(init)),
    ).toBeNull();
  });

  // `?` is Shift+/ on a US keyboard, so Shift is part of how it was typed.
  it('matches a symbol whatever Shift it took', () => {
    const { resolve } = createKeybindingResolver(
      [{ keys: ['?'], command: 'shortcuts' }],
      false,
    );
    expect(resolve(key({ key: '?', shiftKey: true }))).toBe('shortcuts');
    expect(resolve(key({ key: '?' }))).toBe('shortcuts');
  });

  // Shift+` types ~ on a US keyboard, and something else elsewhere.
  it('goes by the physical key for ` and holds it to its Shift', () => {
    const { resolve } = createKeybindingResolver(
      [{ keys: ['mod+shift+`'], command: 'newSession' }],
      false,
    );
    const stroke = { code: 'Backquote', ctrlKey: true, shiftKey: true };
    expect(resolve(key({ key: '~', ...stroke }))).toBe('newSession');
    expect(resolve(key({ key: '`', ...stroke }))).toBe('newSession');
    expect(resolve(key({ key: '`', ...stroke, shiftKey: false }))).toBeNull();
    expect(
      resolve(key({ key: '~', code: 'Digit1', ctrlKey: true, shiftKey: true })),
    ).toBeNull();
  });

  // ⇧⌘` cycles a Mac's windows, so the binding is Control there.
  it('takes a binding’s Mac keys on a Mac, with ctrl as Control', () => {
    const bindings = [
      { keys: ['mod+shift+`'], mac: ['ctrl+shift+`'], command: 'newSession' },
    ];
    const stroke = { key: '~', code: 'Backquote', shiftKey: true };
    const mac = createKeybindingResolver(bindings, true);
    expect(mac.resolve(key({ ...stroke, ctrlKey: true }))).toBe('newSession');
    expect(mac.resolve(key({ ...stroke, metaKey: true }))).toBeNull();
    const others = createKeybindingResolver(bindings, false);
    expect(others.resolve(key({ ...stroke, ctrlKey: true }))).toBe(
      'newSession',
    );
  });

  it('goes by the physical key when a layout types another letter', () => {
    const { resolve } = createKeybindingResolver(bindings, false);
    const cyrillic = { key: 'З', code: 'KeyP', ctrlKey: true, shiftKey: true };
    expect(resolve(key(cyrillic))).toBe('palette');
    expect(resolve(key({ ...cyrillic, shiftKey: false }))).toBeNull();
  });

  it('takes a Latin layout’s own letter over the physical key', () => {
    const { resolve } = createKeybindingResolver(
      [{ keys: ['mod+p'], command: 'quickOpen' }],
      false,
    );
    expect(resolve(key({ key: 'l', code: 'KeyP', ctrlKey: true }))).toBeNull();
    expect(resolve(key({ key: 'p', code: 'KeyR', ctrlKey: true }))).toBe(
      'quickOpen',
    );
  });

  it('leaves keys to an input method that is composing', () => {
    const { resolve } = createKeybindingResolver(bindings, false);
    expect(resolve(key({ key: 'F1', isComposing: true }))).toBeNull();
  });

  it('completes a chord on its second stroke', () => {
    const { resolve } = createKeybindingResolver(bindings, false);
    expect(resolve(key({ key: 'k', ctrlKey: true }))).toBe(CONSUMED);
    // Releasing and re-pressing Ctrl between the strokes must not break it.
    expect(resolve(key({ key: 'Control', ctrlKey: true }))).toBeNull();
    expect(resolve(key({ key: 's', ctrlKey: true }))).toBe('shortcuts');
  });

  it('swallows the key that breaks a chord, then starts afresh', () => {
    const { resolve } = createKeybindingResolver(bindings, false);
    resolve(key({ key: 'k', ctrlKey: true }));
    expect(resolve(key({ key: 'x' }))).toBe(CONSUMED);
    expect(resolve(key({ key: 's', ctrlKey: true }))).toBeNull();
    expect(resolve(key({ key: 'F1' }))).toBe('palette');
  });

  describe('an unfinished chord', () => {
    afterEach(() => {
      vi.useRealTimers();
    });

    it('lapses, and the next key is its own again', () => {
      vi.useFakeTimers();
      const { resolve } = createKeybindingResolver(bindings, false);
      resolve(key({ key: 'k', ctrlKey: true }));
      vi.advanceTimersByTime(CHORD_TIMEOUT_MS + 1);
      expect(resolve(key({ key: 's', ctrlKey: true }))).toBeNull();
      expect(resolve(key({ key: 'F1' }))).toBe('palette');
    });

    it('is dropped on reset', () => {
      const resolver = createKeybindingResolver(bindings, false);
      resolver.resolve(key({ key: 'k', ctrlKey: true }));
      resolver.reset();
      expect(resolver.resolve(key({ key: 'x' }))).toBeNull();
    });
  });

  it('leaves typing keys to a text field, but not modified or F-keys', () => {
    const { resolve } = createKeybindingResolver(bindings, false);
    const input = document.createElement('input');
    expect(resolve(key({ key: 'g' }, input))).toBeNull();
    expect(resolve(key({ key: 'F1' }, input))).toBe('palette');
    expect(
      resolve(key({ key: 'P', ctrlKey: true, shiftKey: true }, input)),
    ).toBe('palette');
    expect(resolve(key({ key: 'g' }))).toBe('plain');
  });
});
