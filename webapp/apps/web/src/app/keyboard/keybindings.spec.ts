import { BOUND_COMMANDS, keysFor, labelOf } from './keybindings';

describe('keysFor', () => {
  const bindings = [
    { keys: ['mod+shift+p', 'f1'], command: 'palette' },
    { keys: ['mod+shift+`'], mac: ['ctrl+shift+`'], command: 'terminal' },
  ];

  it('lists every key bound to a command, formatted', () => {
    expect(keysFor('palette', false, bindings)).toEqual(['Ctrl+Shift+P', 'F1']);
    expect(keysFor('palette', true, bindings)).toEqual(['⇧⌘P', 'F1']);
  });

  it("lists a Mac's own keys there", () => {
    expect(keysFor('terminal', false, bindings)).toEqual(['Ctrl+Shift+`']);
    expect(keysFor('terminal', true, bindings)).toEqual(['⌃⇧`']);
  });

  it('is empty for a command without keys', () => {
    expect(keysFor('logs', false, bindings)).toEqual([]);
  });
});

describe('labelOf', () => {
  it('is the label the shortcut reference lists a command by', () => {
    for (const { command, label } of BOUND_COMMANDS) {
      expect(labelOf(command)).toBe(label);
    }
  });
});
