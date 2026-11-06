/** Keybinding mechanics, for keys in the notation of components' Stroke. */

import {
  isMac,
  isTextEntry,
  type KeyHelp,
  parseKeys,
  platformKeys,
  type Stroke,
} from '@homeshell/components';

/** Binds keys to the ID of a registered command (see commands.tsx). */
export interface Keybinding extends Pick<KeyHelp, 'keys' | 'mac'> {
  command: string;
}

const MODIFIER_KEYS = new Set(['Control', 'Meta', 'Shift', 'Alt']);

// Symbols bound by where they sit rather than by what they type, because the
// binding holds Shift: Shift+` types ~ on a US keyboard. As in VS Code, where
// Ctrl+Shift+` is Create New Terminal.
const PHYSICAL_KEYS: Record<string, string> = { '`': 'Backquote' };

function matchesKey(stroke: Stroke, event: KeyboardEvent): boolean {
  const physical = PHYSICAL_KEYS[stroke.key];
  if (physical) return event.code === physical;
  const key = event.key.toLowerCase();
  if (key === stroke.key) return true;
  // On a non-Latin layout Ctrl+P arrives as, say, Ctrl+з: fall back to the
  // physical key for letters and digits, as VS Code does. Not when the layout
  // types a Latin letter itself (Dvorak's Ctrl+L sits where P is), and not for
  // symbols, which are bound by what they type.
  return (
    (stroke.mod || stroke.ctrl) &&
    !/^[a-z0-9]$/.test(key) &&
    /^[a-z0-9]$/.test(stroke.key) &&
    event.code.replace(/^(Key|Digit)/, '').toLowerCase() === stroke.key
  );
}

function matches(stroke: Stroke, event: KeyboardEvent, mac: boolean): boolean {
  // A symbol already encodes Shift, and which symbols need it depends on the
  // layout: `?` is Shift+/ on a US keyboard but not on every other.
  const shiftMatters =
    stroke.key.length > 1 ||
    /[a-z0-9]/.test(stroke.key) ||
    stroke.key in PHYSICAL_KEYS;
  return (
    matchesKey(stroke, event) &&
    event.ctrlKey === (stroke.ctrl || (!mac && stroke.mod)) &&
    event.metaKey === (mac && stroke.mod) &&
    event.altKey === stroke.alt &&
    (!shiftMatters || event.shiftKey === stroke.shift)
  );
}

/** A stroke that would otherwise type into a field: no modifier, no F-key. */
function typesText(stroke: Stroke): boolean {
  return (
    !stroke.mod && !stroke.ctrl && !stroke.alt && !/^f\d+$/.test(stroke.key)
  );
}

/** How long a chord's first stroke waits for its second, as in VS Code. */
export const CHORD_TIMEOUT_MS = 1500;

/** A key taken without a command to run: a chord's first stroke, or one breaking it off. */
export const CONSUMED = Symbol('consumed');

/** What a key comes to: the ID of the command to run, CONSUMED, or null for none. */
export type KeyResolution = string | typeof CONSUMED | null;

/**
 * Turns keydown events into commands one stroke at a time, holding a chord's
 * first stroke for CHORD_TIMEOUT_MS at most. Strokes that would type are not
 * bindings inside a text field. `reset` drops an unfinished chord.
 */
export function createKeybindingResolver(
  bindings: readonly Keybinding[],
  mac = isMac(),
) {
  const all = bindings.flatMap((binding) =>
    platformKeys(binding, mac).map((keys) => ({
      command: binding.command,
      strokes: parseKeys(keys),
    })),
  );
  let pending: { bindings: typeof all; until: number } | null = null;

  return {
    reset() {
      pending = null;
    },
    resolve(event: KeyboardEvent): KeyResolution {
      if (event.isComposing || MODIFIER_KEYS.has(event.key)) return null;
      const live = pending && Date.now() <= pending.until ? pending : null;
      pending = null;
      const step = live ? 1 : 0;
      const candidates = live?.bindings ?? all;
      const typing = isTextEntry(event.target);
      const hits = candidates.filter((binding) => {
        const stroke = binding.strokes[step];
        return (
          stroke !== undefined &&
          matches(stroke, event, mac) &&
          !(typing && typesText(stroke))
        );
      });
      const complete = hits.find(
        (binding) => binding.strokes.length === step + 1,
      );
      if (complete) return complete.command;
      if (hits.length > 0) {
        pending = { bindings: hits, until: Date.now() + CHORD_TIMEOUT_MS };
        return CONSUMED;
      }
      return step > 0 ? CONSUMED : null;
    },
  };
}
