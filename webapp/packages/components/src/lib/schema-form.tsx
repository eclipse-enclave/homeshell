import { useId, useState } from 'react';
import { flushSync } from 'react-dom';
import styles from './new-workstream-form.module.css';
import { PILL_MAX, PillGroup } from './pill-select';
import {
  ADD_DRAFT,
  ChecklistField,
  ChipListField,
  RowListField,
} from './schema-fields';
import groupStyles from './schema-fields.module.css';
import {
  type ConfigObject,
  type ConfigValue,
  type SchemaProp,
  changesOf,
  defaultOf,
  defaultsOf,
  fieldsOf,
  isDefault,
  isLocked,
  kindOf,
  pathHolds,
  pathOf,
  summaryOf,
} from './schema-values';
import { sentence } from './sentence';
import { SuggestInput } from './suggest-input';

export * from './schema-values';

/*
 * The subset of JSON Schema the create forms render as fields, shared by the
 * workstream-create and session-create forms. Providers hand the UI a schema per
 * workstream type / session option; the UI renders each top-level property as a
 * field (booleans as checkboxes, enums as pills or selects, `"format":"textarea"`
 * strings as a multi-line textarea, numbers as number inputs, else a text
 * input, which suggests the property's `examples` when it has any). An object
 * property with `properties` is a collapsible group of such fields; an array is
 * a checklist (items with `oneOf`/`enum` choices), a list of rows (object
 * items) or a list of chips (anything else). Conditional keywords
 * (if/then/allOf) are intentionally not interpreted here: providers keep
 * disjoint shapes as separate schemas so the client never has to evaluate
 * schema logic (domain logic lives in the daemon).
 *
 * Optionality: a field with a schema `default` starts at that value; a *required*
 * enum with no default starts at its first option; an *optional* enum with no
 * default starts **unset** (a leading "Default" option) and is omitted from the
 * submitted config, so the provider/agent applies its own default rather than
 * the UI silently forcing the first choice.
 *
 * Every field carries its config path in `data-field` (see `pathOf`), which is
 * how a refusal's JSON pointers find the field they blame.
 */

/** Whether an enum field should offer a leading "unset" (use-default) option. */
function enumIsOptional(prop: SchemaProp, required: boolean): boolean {
  return Array.isArray(prop.enum) && !required && prop.default === undefined;
}

export interface SchemaFieldProps {
  fieldKey: string;
  prop: SchemaProp;
  required: boolean;
  value: ConfigValue | undefined;
  /** Takes no input, but stays focusable, so a focused field keeps focus. */
  disabled: boolean;
  onChange: (value: ConfigValue) => void;
  /** Why the daemon refused the field's value, shown under it. */
  error?: string;
  /**
   * The field's config path (see `pathOf`); its bare key when unset, as for a
   * top-level field.
   */
  path?: string;
  /**
   * The refusal's reasons by field path, from which a group picks its fields'
   * (and opens when one is blamed). A field's own reason is `error`, else its
   * entry here.
   */
  errors?: Readonly<Record<string, string>>;
  /** Whether a group starts open; collapsed by default. */
  groupsOpen?: boolean;
}

/** Why the daemon refused a field's value, as a sentence. */
function FieldError({
  id,
  error,
  className = '',
}: {
  id: string;
  error?: string;
  className?: string;
}) {
  return error ? (
    <small id={id} className={`${styles.fieldError} ${className}`}>
      {sentence(error)}
    </small>
  ) : null;
}

/** Renders one schema property as the appropriate input. */
export function SchemaField({
  fieldKey,
  prop,
  required,
  value,
  disabled,
  onChange,
  error: ownError,
  path = pathOf(fieldKey),
  errors,
  groupsOpen = false,
}: SchemaFieldProps) {
  const errorId = useId();
  const labelId = useId();
  const descriptionId = useId();
  const error = ownError ?? errors?.[path];
  const title = prop.title ?? fieldKey;
  const label = `${title}${required ? ' *' : ''}`;
  const change = (next: ConfigValue) => {
    if (!disabled) onChange(next);
  };
  const describedBy =
    [error && errorId, prop.description && descriptionId]
      .filter(Boolean)
      .join(' ') || undefined;
  const aria = {
    'aria-invalid': error ? true : undefined,
    'aria-describedby': describedBy,
  };
  const errorText = <FieldError id={errorId} error={error} />;
  // Beside the label rather than under the control: it qualifies the value
  // shown, and costs no line of its own.
  const source = prop['x-source'] && isDefault(prop, value, required) && (
    <small className={styles.source}>From {prop['x-source']}</small>
  );
  const description = prop.description && (
    <small id={descriptionId} className={styles.hint}>
      {prop.description}
    </small>
  );
  const kind = kindOf(prop);

  if (kind === 'group') {
    return (
      <SchemaGroup
        fieldKey={fieldKey}
        path={path}
        prop={prop}
        value={value}
        disabled={disabled}
        onChange={onChange}
        error={error}
        errors={errors}
        defaultOpen={groupsOpen}
      />
    );
  }

  if (kind !== 'scalar') {
    const list = Array.isArray(value) ? value : [];
    const common = {
      prop,
      title,
      value: list,
      disabled,
      onChange: change,
      invalid: Boolean(error),
    };
    return (
      // Named by its visible label, as a fieldset would be, so its controls
      // can be found within it.
      <div
        className={styles.label}
        data-field={path}
        role="group"
        aria-labelledby={labelId}
        aria-describedby={describedBy}
      >
        <span className={styles.labelRow}>
          <span id={labelId}>{label}</span>
          {source}
        </span>
        {kind === 'checklist' ? (
          <ChecklistField {...common} />
        ) : kind === 'rows' ? (
          <RowListField {...common} path={path} errors={errors} />
        ) : (
          <ChipListField {...common} />
        )}
        {errorText}
        {description}
      </div>
    );
  }

  if (prop.type === 'boolean') {
    const locked = disabled || isLocked(prop);
    return (
      <div className={styles.check} data-field={path}>
        <span className={styles.labelRow}>
          <label className={styles.checkboxLabel}>
            <input
              type="checkbox"
              checked={Boolean(value)}
              onChange={(e) => {
                if (!locked) change(e.target.checked);
              }}
              aria-disabled={locked}
              {...aria}
            />
            {label}
          </label>
          {source}
        </span>
        <FieldError
          id={errorId}
          error={error}
          className={groupStyles.checkboxHint}
        />
        {prop.description && (
          <small
            id={descriptionId}
            className={`${styles.hint} ${groupStyles.checkboxHint}`}
          >
            {prop.description}
          </small>
        )}
      </div>
    );
  }

  const isEnum = Array.isArray(prop.enum);
  const usePills = isEnum && prop.enum!.length <= PILL_MAX && !enumIsOptional(prop, required);

  if (usePills) {
    return (
      <div className={styles.label} data-field={path}>
        <span className={styles.labelRow}>
          <span>{label}</span>
          {source}
        </span>
        <PillGroup
          label={title}
          options={prop.enum!.map((option) => ({ value: option, label: option }))}
          value={String(value)}
          disabled={disabled}
          onChange={onChange}
          {...aria}
        />
        {errorText}
        {description}
      </div>
    );
  }

  return (
    <label className={styles.label} data-field={path}>
      <span className={styles.labelRow}>
        <span>{label}</span>
        {source}
      </span>
      {isEnum ? (
        <select
          className={styles.input}
          value={String(value ?? '')}
          onChange={(e) => change(e.target.value)}
          aria-label={title}
          aria-disabled={disabled}
          {...aria}
        >
          {enumIsOptional(prop, required) && <option value="">Default</option>}
          {prop.enum!.map((option) => (
            <option key={option} value={option}>
              {option}
            </option>
          ))}
        </select>
      ) : prop.format === 'textarea' ? (
        <textarea
          className={styles.input}
          rows={4}
          value={String(value ?? '')}
          onChange={(e) => change(e.target.value)}
          aria-label={title}
          readOnly={disabled}
          aria-disabled={disabled}
          {...aria}
        />
      ) : prop.examples?.length ? (
        <SuggestInput
          className={styles.input}
          label={title}
          value={String(value ?? '')}
          onChange={change}
          suggestions={prop.examples}
          disabled={disabled}
          {...aria}
        />
      ) : (
        <input
          className={styles.input}
          type={prop.type === 'number' || prop.type === 'integer' ? 'number' : 'text'}
          value={String(value ?? '')}
          onChange={(e) => change(e.target.value)}
          aria-label={title}
          readOnly={disabled}
          aria-disabled={disabled}
          {...aria}
        />
      )}
      {errorText}
      {description}
    </label>
  );
}

interface SchemaGroupProps {
  fieldKey: string;
  path: string;
  prop: SchemaProp;
  value: ConfigValue | undefined;
  disabled: boolean;
  onChange: (value: ConfigValue) => void;
  error?: string;
  errors?: Readonly<Record<string, string>>;
  defaultOpen: boolean;
}

/**
 * An object property's fields under a disclosure. Collapsed, the header says
 * in one line what differs from the defaults, so a form can carry a group of
 * rarely touched settings without showing them all. It opens by itself when a
 * refusal blames one of its fields. Each field that differs from its default
 * is marked and can be reset. A group without fields has nothing to fold or
 * change: it shows its title and description as they are.
 */
function SchemaGroup({
  fieldKey,
  path,
  prop,
  value,
  disabled,
  onChange,
  error,
  errors,
  defaultOpen,
}: SchemaGroupProps) {
  const [open, setOpen] = useState(defaultOpen);
  // Opened as a render adjustment rather than an effect, so the refused field
  // is never drawn hidden: the refusal focuses it as it arrives.
  const [seenErrors, setSeenErrors] = useState(errors);
  if (errors !== seenErrors) {
    setSeenErrors(errors);
    if (
      Object.keys(errors ?? {}).some((blamed) => pathHolds(path, `/${blamed}`))
    ) {
      setOpen(true);
    }
  }
  const bodyId = useId();
  const titleId = useId();
  const summaryId = useId();
  const errorId = useId();
  const descriptionId = useId();
  const title = prop.title ?? fieldKey;
  const values: ConfigObject =
    value && typeof value === 'object' && !Array.isArray(value)
      ? value
      : defaultsOf(prop);
  const changes = changesOf(prop, values);
  const summary = summaryOf(changes);
  const fields = fieldsOf(prop);
  const description = prop.description && (
    <small id={descriptionId} className={styles.hint}>
      {prop.description}
    </small>
  );

  if (fields.length === 0) {
    return (
      <div
        className={groupStyles.group}
        data-field={path}
        role="group"
        aria-label={title}
        aria-describedby={
          [prop.description && descriptionId, error && errorId]
            .filter(Boolean)
            .join(' ') || undefined
        }
      >
        <span
          className={`${groupStyles.groupTitle} ${groupStyles.groupStatic}`}
        >
          {title}
        </span>
        {description}
        <FieldError id={errorId} error={error} />
      </div>
    );
  }

  return (
    <div
      className={groupStyles.group}
      data-field={path}
      role="group"
      aria-labelledby={titleId}
    >
      {/* Named by its title and summary: what it opens, and what differs. */}
      <button
        type="button"
        className={groupStyles.groupHeader}
        aria-expanded={open}
        aria-controls={bodyId}
        aria-labelledby={`${titleId} ${summaryId}`}
        aria-describedby={error ? errorId : undefined}
        onClick={() => setOpen((was) => !was)}
      >
        <svg
          className={`${groupStyles.caret} ${open ? groupStyles.caretOpen : ''}`}
          viewBox="0 0 16 16"
          aria-hidden="true"
        >
          <path
            d="M6 4l4 4-4 4"
            fill="none"
            stroke="currentColor"
            strokeWidth="1.5"
          />
        </svg>
        <span id={titleId} className={groupStyles.groupTitle}>
          {title}
        </span>
        <span
          id={summaryId}
          className={`${groupStyles.groupSummary} ${
            changes.length > 0 ? groupStyles.groupSummaryChanged : ''
          }`}
          data-testid="group-summary"
          title={summary}
        >
          {summary}
        </span>
      </button>
      <FieldError id={errorId} error={error} />
      <div id={bodyId} className={groupStyles.groupBody} hidden={!open}>
        {description}
        {fields.map(({ key, prop: child, required }) => {
          const modified = !isDefault(child, values[key], required);
          const childTitle = child.title ?? key;
          return (
            <div
              key={key}
              className={`${groupStyles.groupField} ${
                modified ? groupStyles.modified : ''
              }`}
            >
              <SchemaField
                fieldKey={key}
                path={`${path}/${pathOf(key)}`}
                prop={child}
                required={required}
                value={values[key]}
                disabled={disabled}
                errors={errors}
                groupsOpen
                onChange={(next) => onChange({ ...values, [key]: next })}
              />
              {modified && (
                <button
                  type="button"
                  className={groupStyles.reset}
                  onClick={() => {
                    if (!disabled) {
                      onChange({
                        ...values,
                        [key]: defaultOf(child, required),
                      });
                    }
                  }}
                  aria-disabled={disabled}
                  aria-label={`Reset ${childTitle} to its default`}
                  title="Changed from the default. Reset it."
                >
                  Reset
                </button>
              )}
            </div>
          );
        })}
      </div>
    </div>
  );
}

// A control that takes a value; of a pill group, the pill holding its tab stop.
const FIELD = 'input, select, textarea, [role="radio"][tabindex="0"]';

/**
 * The first field under `scope` that can take focus: one in a collapsed group
 * cannot, nor can a disabled one, such as an inherited row's.
 */
export function firstField(scope: ParentNode): HTMLElement | null {
  return (
    Array.from(scope.querySelectorAll<HTMLElement>(FIELD)).find(
      (field) => !field.closest('[hidden]') && !field.matches(':disabled'),
    ) ?? null
  );
}

/** Focuses the first field under `scope`, and says whether it took focus. */
export function focusFirstField(scope: ParentNode): boolean {
  const field = firstField(scope);
  field?.focus();
  return field !== null && field.ownerDocument.activeElement === field;
}

/**
 * The reasons a refusal gives by JSON pointer into the submitted config, keyed
 * by the path (`data-field`) of the field of `form` each blames — the deepest
 * one holding what the pointer points at, so `/sandbox/ports/1` blames the
 * ports list inside the sandbox group — in form order.
 */
export function refusedFields(
  form: ParentNode,
  reasons: Readonly<Record<string, string>>,
): Record<string, string> {
  const elements = Array.from(
    form.querySelectorAll<HTMLElement>('[data-field]'),
  );
  const byPath = new Map<string, string[]>();
  for (const [pointer, reason] of Object.entries(reasons)) {
    const holder = elements
      .map((element) => element.dataset.field ?? '')
      .filter((path) => pathHolds(path, pointer))
      .sort((a, b) => b.length - a.length)[0];
    if (holder !== undefined)
      byPath.set(holder, [...(byPath.get(holder) ?? []), reason]);
  }
  return Object.fromEntries(
    elements
      .map((element) => element.dataset.field ?? '')
      .filter((path) => byPath.has(path))
      .map((path) => [path, (byPath.get(path) ?? []).join('; ')]),
  );
}

/**
 * Focuses the field of `form` that `fields` blames first, by path — in form
 * order, as refusedFields and addDrafts give them. Nothing moves when it
 * blames none, or the field is not shown yet.
 */
export function focusBlamedField(
  form: ParentNode,
  fields: Readonly<Record<string, string>>,
) {
  const [first] = Object.keys(fields);
  const blamed = Array.from(
    form.querySelectorAll<HTMLElement>('[data-field]'),
  ).find((element) => element.dataset.field === first);
  if (blamed) focusFirstField(blamed);
}

/**
 * Adds what is typed in the lists of `form` (see ChipListField), as Enter
 * would, one list at a time so each sees the value the last one left. Returns
 * the lists still holding text they could not add, blamed by path: sending
 * the form would drop it.
 */
export function addDrafts(form: ParentNode): Record<string, string> {
  const drafts = () =>
    Array.from(form.querySelectorAll<HTMLElement>('[data-draft]'));
  for (const input of drafts()) {
    flushSync(() => input.dispatchEvent(new Event(ADD_DRAFT)));
  }
  const fields: Record<string, string> = {};
  for (const input of drafts()) {
    const path = input.closest<HTMLElement>('[data-field]')?.dataset.field;
    if (path !== undefined) {
      fields[path] =
        'not added: correct what is typed and press Enter, or clear it';
    }
  }
  return fields;
}
