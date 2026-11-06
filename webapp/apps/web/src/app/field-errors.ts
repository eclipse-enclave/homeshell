/**
 * Turning the daemon's rejection of a create into one line a user can act on.
 *
 * The daemon answers a bad creation config with `{error, fields}`, where `fields`
 * is keyed by **JSON pointer** into the config it was sent (`"/branch"`) — the
 * form's schema is data the daemon authored, so a pointer is the only way it can
 * name an input without knowing how the client laid it out. That pointer is
 * machine talk: showing it verbatim put `configuration is invalid (/branch: …)`
 * in front of the user.
 *
 * This keeps the attribution and drops the notation, and it does so *generically*
 * — from the pointer's own last segment, never from a list of known field names,
 * so a provider that adds a field to its schema tomorrow gets the same treatment
 * with no client change (the client stays ignorant of what providers offer).
 */

import { sentence } from '@homeshell/components';

/** The daemon's error envelope for a refused mutation. */
export interface DaemonErrorBody {
  error?: string;
  fields?: Record<string, string>;
}

/**
 * A JSON pointer's last segment as a field label: `/branch` → `Branch`,
 * `/baseRef` → `Base ref`. Escapes per RFC 6901 (`~1` is `/`, `~0` is `~`) are
 * decoded first, so a pointer into a key containing a slash still reads.
 */
export function fieldLabel(pointer: string): string {
  const last = pointer.split('/').pop() ?? pointer;
  const decoded = last.replace(/~1/g, '/').replace(/~0/g, '~');
  // The root pointer ("" or "/") names the config object, not a field in it —
  // which is what a schema reports for a *missing* property, since the absent
  // key has no location of its own. There is nothing to attribute, so say
  // nothing and let the reason speak (it names the property itself).
  if (!decoded) return '';
  // camelCase to words, then sentence case: the schema's own `title` would be
  // better, but it lives in the form and this runs where only the response is.
  const spaced = decoded.replace(/([a-z0-9])([A-Z])/g, '$1 $2').toLowerCase();
  return sentence(spaced);
}

/**
 * One human-readable line for a refused create.
 *
 * With field detail the generic wrapper ("configuration is invalid") is dropped:
 * it says nothing the attributed reasons do not, and burying the real message in
 * a parenthetical is what made these hard to read. Without field detail the
 * daemon's own message stands, and an unparseable body falls back to `fallback`.
 */
export function describeDaemonError(
  body: DaemonErrorBody | null,
  fallback: string,
): string {
  const entries = Object.entries(body?.fields ?? {});
  if (entries.length > 0) {
    return sentence(
      entries
        .map(([pointer, reason]) => {
          const label = fieldLabel(pointer);
          return label ? `${label}: ${reason}` : reason;
        })
        .join('; '),
    );
  }
  return sentence(body?.error ?? fallback);
}
