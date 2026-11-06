/**
 * The values side of the schema forms: which kind of field a schema property
 * is, its initial value, how a value is coerced for the daemon, and how far it
 * is from its default. Pure functions, so the rendering in schema-form.tsx and
 * schema-fields.tsx only reads them.
 *
 * Everything here is driven by the schema's shape and a few keywords, never by
 * a property's key: a provider may nest any group or list it likes.
 */

/** A choice of a checklist: one `oneOf` entry of an array's `items`. */
export interface SchemaChoice {
  const: string;
  title?: string;
  description?: string;
}

/** Values the provider applies on its own, shown locked beside the field's. */
export interface SchemaInherited {
  values?: unknown[];
  source?: string;
}

export interface Schema {
  properties?: Record<string, SchemaProp>;
  required?: string[];
}

export interface SchemaProp extends Schema {
  type?: string;
  title?: string;
  description?: string;
  enum?: string[];
  default?: unknown;
  /**
   * Rendering hint: "textarea" renders a string as a multi-line textarea,
   * "directory" offers a folder browser beside it.
   */
  format?: string;
  /** Values a string field suggests as the user types; others are allowed. */
  examples?: string[];
  /** Of an array: what each item is. */
  items?: SchemaProp;
  /** Of a checklist's items: the choices. */
  oneOf?: SchemaChoice[];
  uniqueItems?: boolean;
  minimum?: number;
  maximum?: number;
  minLength?: number;
  additionalProperties?: boolean;
  /**
   * Of a boolean: shown locked at its default, which the provider applies
   * whatever a stored config says (see `isLocked`).
   */
  readOnly?: boolean;
  /** Where the field's default comes from, said under it while it holds it. */
  'x-source'?: string;
  /** Values already in effect from elsewhere; not part of the field's value. */
  'x-inherited'?: SchemaInherited;
}

export type ConfigValue =
  | string
  | number
  | boolean
  | ConfigValue[]
  | { [key: string]: ConfigValue };

export type ConfigObject = Record<string, ConfigValue>;

export interface Field {
  key: string;
  prop: SchemaProp;
  required: boolean;
}

/**
 * How a property renders: a collapsible group of fields, a checklist of fixed
 * choices, a list of chips, a list of rows, or one input.
 */
export type FieldKind = 'group' | 'checklist' | 'chips' | 'rows' | 'scalar';

export function kindOf(prop: SchemaProp): FieldKind {
  if (prop.type === 'object' && prop.properties) return 'group';
  if (prop.type !== 'array') return 'scalar';
  const items = prop.items ?? {};
  if (choicesOf(prop).length > 0) return 'checklist';
  if (items.type === 'object' && items.properties) return 'rows';
  return 'chips';
}

/** A checklist's choices, from `items.oneOf` or a plain `items.enum`. */
export function choicesOf(prop: SchemaProp): SchemaChoice[] {
  const items = prop.items ?? {};
  if (Array.isArray(items.oneOf)) {
    return items.oneOf.filter((choice) => typeof choice?.const === 'string');
  }
  if (Array.isArray(items.enum)) {
    return items.enum.map((value) => ({ const: value }));
  }
  return [];
}

function schemaOf(schema: unknown): Schema {
  return schema && typeof schema === 'object' ? (schema as Schema) : {};
}

function isObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

/** The renderable fields of a schema, in declaration order. */
export function fieldsOf(schema: unknown): Field[] {
  const s = schemaOf(schema);
  const required = new Set(s.required ?? []);
  return Object.entries(s.properties ?? {}).map(([key, prop]) => ({
    key,
    prop,
    required: required.has(key),
  }));
}

/** Whether a field is shown locked at its default: a read-only boolean. */
export function isLocked(prop: SchemaProp): boolean {
  return prop.type === 'boolean' && prop.readOnly === true;
}

/**
 * The initial value of one field, with `override` taking precedence — except
 * over a locked field, which holds its default.
 */
function initialOf(
  prop: SchemaProp,
  required: boolean,
  override: unknown,
): ConfigValue {
  const kind = kindOf(prop);
  if (kind === 'group') {
    // Merged per child, so a stored config prefills what it names and the
    // schema fills the rest.
    return defaultsOf(prop, isObject(override) ? override : undefined);
  }
  if (kind !== 'scalar') {
    const items = Array.isArray(override)
      ? (override as ConfigValue[])
      : Array.isArray(prop.default)
        ? (prop.default as ConfigValue[])
        : [];
    // What the provider applies anyway is shown locked, not again as its own.
    const inherited = prop['x-inherited']?.values;
    return Array.isArray(inherited)
      ? items.filter(
          (item) => !inherited.some((applied) => sameValue(applied, item)),
        )
      : items;
  }
  if (override !== undefined && !isLocked(prop)) return override as ConfigValue;
  if (prop.default !== undefined) return prop.default as ConfigValue;
  if (prop.type === 'boolean') return false;
  // A required enum must carry a value, so pick the first; an optional enum
  // starts unset so it is omitted (the agent applies its own default).
  if (required && Array.isArray(prop.enum) && prop.enum.length > 0) {
    return prop.enum[0];
  }
  return '';
}

/**
 * The initial config values for a schema (its declared defaults, groups
 * included). Any `overrides` take precedence over the schema default, so a
 * caller can prefill a field (e.g. a preferred filesystem strategy from the
 * user's settings, or the config a session was last started with).
 */
export function defaultsOf(
  schema: unknown,
  overrides?: Record<string, unknown>,
): ConfigObject {
  const out: ConfigObject = {};
  for (const { key, prop, required } of fieldsOf(schema)) {
    out[key] = initialOf(prop, required, overrides?.[key]);
  }
  return out;
}

function coerceScalar(prop: SchemaProp, value: ConfigValue): ConfigValue {
  if (prop.type === 'number' || prop.type === 'integer') return Number(value);
  return value;
}

/** Whether a row leaves a required string empty, which drops it. */
export function isBlankRow(items: SchemaProp, row: ConfigValue): boolean {
  if (!isObject(row)) return true;
  return (items.required ?? []).some(
    (key) => typeof row[key] !== 'boolean' && String(row[key] ?? '').trim() === '',
  );
}

/** Coerces the form values into a config object typed per the schema. */
export function buildConfig(
  fields: Field[],
  config: Readonly<Record<string, ConfigValue | undefined>>,
): Record<string, unknown> {
  const payload: Record<string, unknown> = {};
  for (const { key, prop, required } of fields) {
    const value = config[key];
    const kind = kindOf(prop);
    if (kind === 'group') {
      payload[key] = buildConfig(
        fieldsOf(prop),
        isObject(value) ? (value as ConfigObject) : defaultsOf(prop),
      );
    } else if (kind === 'rows') {
      const itemFields = fieldsOf(prop.items);
      payload[key] = itemsOf(prop, value).map((row) =>
        buildConfig(itemFields, row as ConfigObject),
      );
    } else if (kind !== 'scalar') {
      const items = prop.items ?? {};
      payload[key] = (Array.isArray(value) ? value : []).map((item) =>
        coerceScalar(items, item),
      );
    } else if (prop.type === 'boolean') {
      payload[key] = Boolean(value);
    } else if (value === '' || value === undefined) {
      if (required) payload[key] = '';
    } else {
      payload[key] = coerceScalar(prop, value);
    }
  }
  return payload;
}

/** Structural equality of two config values; key order does not matter. */
export function sameValue(a: unknown, b: unknown): boolean {
  if (Array.isArray(a) || Array.isArray(b)) {
    return (
      Array.isArray(a) &&
      Array.isArray(b) &&
      a.length === b.length &&
      a.every((item, index) => sameValue(item, b[index]))
    );
  }
  if (isObject(a) || isObject(b)) {
    if (!isObject(a) || !isObject(b)) return false;
    const keys = new Set([...Object.keys(a), ...Object.keys(b)]);
    return [...keys].every((key) => sameValue(a[key], b[key]));
  }
  return a === b;
}

/** A field's value when untouched. */
export function defaultOf(prop: SchemaProp, required = false): ConfigValue {
  return initialOf(prop, required, undefined);
}

/** A list's items as sent, without a rows list's blank rows. */
function itemsOf(
  prop: SchemaProp,
  value: ConfigValue | undefined,
): ConfigValue[] {
  const items = Array.isArray(value) ? value : [];
  if (kindOf(prop) !== 'rows') return items;
  return items.filter((row) => !isBlankRow(prop.items ?? {}, row));
}

/** Whether a set-like array holds the same members, in any order. */
function sameMembers(a: ConfigValue[], b: ConfigValue[]): boolean {
  return (
    a.length === b.length &&
    a.every((item) => b.some((other) => sameValue(item, other)))
  );
}

/** Whether `value` is the field's default (a checklist's in any order). */
export function isDefault(
  prop: SchemaProp,
  value: ConfigValue | undefined,
  required = false,
): boolean {
  const fallback = defaultOf(prop, required);
  const kind = kindOf(prop);
  if (kind === 'checklist' || (kind === 'chips' && prop.uniqueItems)) {
    return sameMembers(itemsOf(prop, value), fallback as ConfigValue[]);
  }
  if (kind === 'group') {
    return fieldsOf(prop).every(({ key, prop: child, required: req }) =>
      isDefault(child, (value as ConfigObject | undefined)?.[key], req),
    );
  }
  if (kind === 'rows' || kind === 'chips') {
    return sameValue(itemsOf(prop, value ?? fallback), fallback);
  }
  // As sent (see buildConfig): an emptied box is left out, and a number box
  // holds the text typed into it.
  if (value === '' && !required) return true;
  const sent =
    typeof value === 'string' && value !== ''
      ? coerceScalar(prop, value)
      : value;
  return sameValue(sent ?? fallback, fallback);
}

function titleOf(key: string, prop: SchemaProp): string {
  return prop.title ?? key;
}

/** What one field changed from its default, in a few words; null when none. */
function changeOf(
  key: string,
  prop: SchemaProp,
  value: ConfigValue | undefined,
  required: boolean,
): string | null {
  if (isDefault(prop, value, required)) return null;
  const title = titleOf(key, prop);
  const kind = kindOf(prop);
  if (kind === 'group') {
    const inner = changesOf(prop, value);
    return inner.length > 0 ? `${title}: ${inner.join(', ')}` : null;
  }
  if (kind !== 'scalar') {
    const now = itemsOf(prop, value);
    const was = defaultOf(prop, required) as ConfigValue[];
    const added = now.filter((item) => !was.some((w) => sameValue(w, item)));
    const removed = was.filter((item) => !now.some((n) => sameValue(n, item)));
    const parts =
      kind === 'checklist'
        ? [
            ...added.map((item) => `+${String(item)}`),
            ...removed.map((item) => `−${String(item)}`),
          ]
        : [
            added.length > 0 ? `+${added.length}` : '',
            removed.length > 0 ? `−${removed.length}` : '',
          ].filter(Boolean);
    // Same members, another order or other row contents.
    return parts.length > 0 ? `${title} ${parts.join(' ')}` : `${title} changed`;
  }
  if (prop.type === 'boolean') return value ? title : `${title} off`;
  return `${title}: ${String(value ?? '')}`;
}

/**
 * What a group's value changed from its defaults, one entry per changed child
 * by title, e.g. `Features +playwright −python`, `Directories +1`, or a
 * boolean's bare title when it was switched on. Empty when nothing changed.
 */
export function changesOf(group: SchemaProp, value: unknown): string[] {
  const values = isObject(value) ? (value as ConfigObject) : {};
  return fieldsOf(group)
    .map(({ key, prop, required }) =>
      changeOf(key, prop, values[key], required),
    )
    .filter((change): change is string => change !== null);
}

/** A group's one-line summary of its `changesOf`: "Defaults", or those. */
export function summaryOf(changes: string[]): string {
  return changes.length > 0 ? changes.join(' · ') : 'Defaults';
}

/**
 * What a list calls one of its items, for "Add …" and "Delete … 2": the
 * items' title, lower-cased unless it starts with an acronym, else "item".
 */
export function itemNounOf(prop: SchemaProp): string {
  const title = prop.items?.title ?? 'item';
  return /^[A-Z][^A-Z]/.test(title)
    ? title.charAt(0).toLowerCase() + title.slice(1)
    : title;
}

/**
 * A config path as `data-field` carries it: its keys escaped as in a JSON
 * pointer and joined by "/", so a top-level field's is its bare key and a
 * pointer the daemon blames is the path with a leading "/".
 */
export function pathOf(...keys: string[]): string {
  return keys.map((key) => key.replace(/~/g, '~0').replace(/\//g, '~1')).join('/');
}

/**
 * The config paths below `path` whose values differ from `before` to `after`:
 * the deepest keys of objects on both sides that changed, else `path` itself
 * if the value did. Empty when nothing changed.
 */
export function changedPaths(
  path: string,
  before: unknown,
  after: unknown,
): string[] {
  if (sameValue(before, after)) return [];
  if (!isObject(before) || !isObject(after)) return [path];
  const keys = new Set([...Object.keys(before), ...Object.keys(after)]);
  return [...keys].flatMap((key) =>
    changedPaths(`${path}/${pathOf(key)}`, before[key], after[key]),
  );
}

/** Whether the field at `path` holds what the JSON `pointer` points at. */
export function pathHolds(path: string, pointer: string): boolean {
  const field = `/${path}`;
  return pointer === field || pointer.startsWith(`${field}/`);
}
