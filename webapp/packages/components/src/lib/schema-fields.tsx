import {
  type KeyboardEvent,
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
} from 'react';
import { Button } from './button';
import { FolderPicker } from './folder-picker';
import formStyles from './new-workstream-form.module.css';
import styles from './schema-fields.module.css';
import { sentence } from './sentence';
import {
  type ConfigObject,
  type ConfigValue,
  type SchemaChoice,
  type SchemaProp,
  choicesOf,
  defaultsOf,
  fieldsOf,
  isBlankRow,
  itemNounOf,
  sameValue,
} from './schema-values';

/**
 * The array fields of a schema form (see schema-form.tsx): a checklist of
 * fixed choices, a list of chips, a list of rows. SchemaField frames them with
 * their label, error and hints; these render the list itself.
 */
export interface ListFieldProps {
  prop: SchemaProp;
  /** The field's title, for the names of its controls. */
  title: string;
  value: ConfigValue[];
  /** Takes no input, but stays focusable, as the other fields do. */
  disabled: boolean;
  onChange: (value: ConfigValue[]) => void;
  /** Whether the daemon refused the value. */
  invalid?: boolean;
  /** Of a rows list: its config path, under which each row has its own. */
  path?: string;
  /** Of a rows list: the refusal's reasons by path, for its rows'. */
  errors?: Readonly<Record<string, string>>;
}

/** Past this many choices, the ones not in play fold under "N more". */
const CHECKLIST_FOLD = 6;

/**
 * A checkbox per choice, with its description as secondary text. A long list
 * shows the default choices and the checked ones, and folds the rest away;
 * which those are is settled as it opens, so checking one does not move it,
 * and folded again, what was checked meanwhile stays shown. A value that is not
 * among the choices is listed checked after them, so it can be unchecked; the
 * unlisted values are fixed at mount, so one unchecked stays listed.
 */
export function ChecklistField({
  prop,
  value,
  disabled,
  onChange,
  invalid,
}: ListFieldProps) {
  const [unlisted] = useState(() => {
    const offered = choicesOf(prop).map((choice) => choice.const);
    return value.map(String).filter((id) => !offered.includes(id));
  });
  const choices: SchemaChoice[] = [
    ...choicesOf(prop),
    ...unlisted.map((id) => ({
      const: id,
      description: 'Not offered any more',
    })),
  ];
  const listId = useId();
  const [shown, setShown] = useState(() => {
    if (choices.length <= CHECKLIST_FOLD) return null;
    const defaults = Array.isArray(prop.default) ? prop.default : [];
    return new Set(
      choices
        .map((choice) => choice.const)
        .filter((id) => defaults.includes(id) || value.includes(id)),
    );
  });
  const [unfolded, setUnfolded] = useState(false);
  const primary = shown
    ? choices.filter((choice) => shown.has(choice.const))
    : choices;
  const rest = shown ? choices.filter((choice) => !shown.has(choice.const)) : [];

  const toggle = (id: string, checked: boolean) => {
    if (disabled) return;
    const next = new Set(value.map(String));
    if (checked) next.add(id);
    else next.delete(id);
    // In the choices' order, so the value does not depend on click order.
    onChange(
      choices
        .map((choice) => choice.const)
        .filter((choice) => next.has(choice)),
    );
  };

  const fold = () => {
    if (unfolded && shown) setShown(new Set([...shown, ...value.map(String)]));
    setUnfolded((was) => !was);
  };

  const box = (choice: SchemaChoice) => {
    const descriptionId = `${listId}-${choices.indexOf(choice)}`;
    return (
      <label key={choice.const} className={styles.choice}>
        <input
          type="checkbox"
          checked={value.includes(choice.const)}
          onChange={(event) => toggle(choice.const, event.target.checked)}
          aria-label={choice.title ?? choice.const}
          aria-describedby={choice.description ? descriptionId : undefined}
          aria-disabled={disabled}
          aria-invalid={invalid || undefined}
        />
        <span className={styles.choiceText}>
          <span className={styles.choiceTitle}>
            {choice.title ?? choice.const}
          </span>
          {choice.description && (
            <small id={descriptionId} className={styles.choiceDescription}>
              {choice.description}
            </small>
          )}
        </span>
      </label>
    );
  };

  return (
    <div className={styles.checklist}>
      {primary.map(box)}
      {unfolded && rest.map(box)}
      {rest.length > 0 && (
        <button
          type="button"
          className={styles.more}
          aria-expanded={unfolded}
          onClick={fold}
        >
          {unfolded ? 'Fewer' : `${rest.length} more`}
        </button>
      )}
    </div>
  );
}

/** The values the provider applies anyway, if the schema names any. */
function inheritedOf(prop: SchemaProp) {
  const inherited = prop['x-inherited'];
  const values = Array.isArray(inherited?.values) ? inherited.values : [];
  return { values: values as ConfigValue[], source: inherited?.source ?? '' };
}

/** A note of where locked values come from, beside them. */
function InheritedCaption({ source }: { source: string }) {
  return source ? (
    <small className={styles.inheritedCaption}>From {source}</small>
  ) : null;
}

/** Asks a list's box to add what is typed in it, as Enter does (addDrafts). */
export const ADD_DRAFT = 'schema-form:add-draft';

/**
 * Values as removable chips, and a box to add one: Enter adds it, as does
 * sending the form (`ADD_DRAFT`). Leaving the box does not: a click on the
 * form's button leaves it, and the box must not change under the pointer
 * before the click lands. Numeric items are added as numbers, and a value
 * already listed is not added twice. Values the provider applies anyway
 * (`x-inherited`) are shown first, locked, and are not in the value.
 */
export function ChipListField({
  prop,
  title,
  value,
  disabled,
  onChange,
  invalid,
}: ListFieldProps) {
  const [draft, setDraft] = useState('');
  const [problem, setProblem] = useState<string | null>(null);
  const problemId = useId();
  const boxRef = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const [focusAfterRemove, setFocusAfterRemove] = useState<number | null>(
    null,
  );
  const itemType = prop.items?.type;
  const numeric = itemType === 'integer' || itemType === 'number';
  const inherited = inheritedOf(prop);

  const add = (): boolean => {
    const text = draft.trim();
    if (!text || disabled) return false;
    let item: ConfigValue = text;
    if (numeric) {
      const number = Number(text);
      if (
        !Number.isFinite(number) ||
        (itemType === 'integer' && !Number.isInteger(number))
      ) {
        setProblem(
          itemType === 'integer' ? 'Enter a whole number.' : 'Enter a number.',
        );
        return false;
      }
      item = number;
    }
    if ([...inherited.values, ...value].some((v) => sameValue(v, item))) {
      setProblem(`${text} is already listed.`);
      return false;
    }
    onChange([...value, item]);
    setDraft('');
    setProblem(null);
    return true;
  };

  const addRef = useRef(add);
  useLayoutEffect(() => {
    addRef.current = add;
  });
  useEffect(() => {
    const input = inputRef.current;
    const onAdd = () => addRef.current();
    input?.addEventListener(ADD_DRAFT, onAdd);
    return () => input?.removeEventListener(ADD_DRAFT, onAdd);
  }, []);

  const handleKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    // Enter adds what is typed rather than submitting the form; with nothing
    // typed it submits, as in any other box.
    if (event.key === 'Enter' && draft.trim()) {
      event.preventDefault();
      add();
    }
  };

  // Focus held by a chip removed goes to the Delete of the chip now in its
  // place, else of the one before it, else to the box to add one.
  useEffect(() => {
    if (focusAfterRemove === null) return;
    const removes = boxRef.current?.querySelectorAll<HTMLElement>('button');
    (
      removes?.[focusAfterRemove] ??
      removes?.[focusAfterRemove - 1] ??
      boxRef.current?.querySelector('input')
    )?.focus();
    setFocusAfterRemove(null);
  }, [focusAfterRemove]);

  const remove = (index: number) => {
    if (disabled) return;
    if (boxRef.current?.contains(document.activeElement)) {
      setFocusAfterRemove(index);
    }
    onChange(value.filter((_, at) => at !== index));
  };

  return (
    <div className={styles.list}>
      {inherited.values.length > 0 && (
        <div className={styles.chips} data-testid="inherited-values">
          {inherited.values.map((item) => (
            <span
              key={String(item)}
              className={`${styles.chip} ${styles.chipLocked}`}
              title={`From ${inherited.source}; change it there`}
            >
              {String(item)}
            </span>
          ))}
          <InheritedCaption source={inherited.source} />
        </div>
      )}
      <div
        ref={boxRef}
        className={`${styles.chips} ${styles.chipBox}`}
        onClick={(event) => {
          // Anywhere in the box puts the caret in it, as in one text field.
          if (event.target === event.currentTarget) {
            event.currentTarget.querySelector('input')?.focus();
          }
        }}
      >
        {value.map((item, index) => (
          <span key={`${String(item)}-${index}`} className={styles.chip}>
            <span>{String(item)}</span>
            <button
              type="button"
              className={styles.chipRemove}
              onClick={() => remove(index)}
              aria-label={`Delete ${String(item)}`}
              aria-disabled={disabled}
              title="Delete"
            >
              ×
            </button>
          </span>
        ))}
        <input
          ref={inputRef}
          className={`${formStyles.input} ${styles.chipInput}`}
          type="text"
          inputMode={numeric ? 'numeric' : undefined}
          value={draft}
          onChange={(event) => {
            setDraft(event.target.value);
            setProblem(null);
          }}
          onKeyDown={handleKeyDown}
          placeholder="Add…"
          aria-label={`Add to ${title}`}
          readOnly={disabled}
          aria-disabled={disabled}
          aria-invalid={invalid || Boolean(problem) || undefined}
          aria-describedby={problem ? problemId : undefined}
          // Typed but not added: a form adds it, or refuses to drop it.
          data-draft={draft.trim() ? '' : undefined}
        />
      </div>
      <small
        id={problemId}
        className={`${formStyles.fieldError} ${styles.problem}`}
        role="status"
      >
        {problem}
      </small>
    </div>
  );
}

interface RowProps {
  itemProp: SchemaProp;
  row: ConfigObject;
  label: string;
  /** Inherited rows take no input at all, and are not focusable. */
  locked?: boolean;
  disabled: boolean;
  invalid?: boolean;
  onChange?: (row: ConfigObject) => void;
  onBrowse?: (key: string) => void;
  onRemove?: () => void;
  removeLabel?: string;
  /** The row's config path, as sent; none for a row that is not sent. */
  path?: string;
  /** Why the daemon refused the row. */
  error?: string;
}

function Row({
  itemProp,
  row,
  label,
  locked,
  disabled,
  invalid,
  onChange,
  onBrowse,
  onRemove,
  removeLabel,
  path,
  error,
}: RowProps) {
  const errorId = useId();
  const set = (key: string, next: ConfigValue) => {
    if (!disabled && !locked) onChange?.({ ...row, [key]: next });
  };
  return (
    <>
      <div
        className={`${styles.row} ${locked ? styles.rowLocked : ''}`}
        role="group"
        aria-label={label}
        data-row={locked ? 'locked' : 'own'}
        data-field={path}
      >
        {fieldsOf(itemProp).map(({ key, prop }) => {
          const title = prop.title ?? key;
          if (prop.type === 'boolean') {
            return (
              <label key={key} className={formStyles.checkboxLabel}>
                <input
                  type="checkbox"
                  checked={Boolean(row[key])}
                  onChange={(event) => set(key, event.target.checked)}
                  disabled={locked}
                  aria-disabled={disabled}
                />
                {title}
              </label>
            );
          }
          return (
            <span key={key} className={styles.rowText}>
              <input
                className={formStyles.input}
                type={
                  prop.type === 'number' || prop.type === 'integer'
                    ? 'number'
                    : 'text'
                }
                value={String(row[key] ?? '')}
                onChange={(event) => set(key, event.target.value)}
                placeholder={title}
                aria-label={title}
                disabled={locked}
                readOnly={disabled}
                aria-disabled={disabled}
                aria-invalid={
                  ((invalid || Boolean(error)) && !locked) || undefined
                }
                aria-describedby={error ? errorId : undefined}
              />
              {prop.format === 'directory' && !locked && onBrowse && (
                <Button
                  onClick={() => onBrowse(key)}
                  aria-disabled={disabled}
                  aria-label={`Browse for ${title}`}
                >
                  Browse
                </Button>
              )}
            </span>
          );
        })}
        {onRemove && (
          <button
            type="button"
            className={styles.chipRemove}
            onClick={onRemove}
            aria-label={removeLabel}
            aria-disabled={disabled}
            title="Delete"
          >
            ×
          </button>
        )}
      </div>
      {error && (
        <small id={errorId} className={formStyles.fieldError}>
          {sentence(error)}
        </small>
      )}
    </>
  );
}

let lastRowKey = 0;
const nextRowKey = () => ++lastRowKey;

/**
 * One row per item, each item property inline (text boxes, and checkboxes for
 * booleans), with a button to remove it and one to add another, filled with
 * the item's defaults. A directory gets a Browse button. Rows the provider
 * applies anyway (`x-inherited`) come first, locked, and are not in the value.
 * A row left with a required box empty is dropped when the form is sent.
 */
export function RowListField({
  prop,
  value,
  disabled,
  onChange,
  invalid,
  path,
  errors,
}: ListFieldProps) {
  const itemProp = prop.items ?? {};
  const noun = itemNounOf(prop);
  const Noun = noun.charAt(0).toUpperCase() + noun.slice(1);
  const inherited = inheritedOf(prop);
  const rows = value as ConfigObject[];
  // A key per row, kept as rows come and go, so a row removed takes its boxes
  // with it rather than handing them, and their focus, to the next one.
  const [keys, setKeys] = useState(() => rows.map(nextRowKey));
  if (keys.length !== rows.length) {
    setKeys(rows.map((_, at) => keys[at] ?? nextRowKey()));
  }
  // Each row's path as sent, which leaves the blank rows out.
  let sent = 0;
  const rowPaths = rows.map((row) =>
    path === undefined || isBlankRow(itemProp, row)
      ? undefined
      : `${path}/${sent++}`,
  );

  // A refusal's reasons by row key, pinned as it arrives, so each stays with
  // the row it blames as rows are edited, added or removed.
  const reasonsByKey = () =>
    Object.fromEntries(
      rows.flatMap((_, index) => {
        const reason = errors?.[rowPaths[index] ?? ''];
        return reason ? [[keys[index], reason]] : [];
      }),
    );
  const [rowErrors, setRowErrors] =
    useState<Record<number, string>>(reasonsByKey);
  const [seenErrors, setSeenErrors] = useState(errors);
  if (errors !== seenErrors) {
    setSeenErrors(errors);
    setRowErrors(reasonsByKey());
  }
  const listRef = useRef<HTMLDivElement>(null);
  const addRef = useRef<HTMLButtonElement>(null);
  const [focusRow, setFocusRow] = useState<number | null>(null);
  const [focusAfterRemove, setFocusAfterRemove] = useState<number | null>(
    null,
  );
  const [browsing, setBrowsing] = useState<{ index: number; key: string } | null>(
    null,
  );

  // A row just added takes focus on its first box.
  useEffect(() => {
    if (focusRow === null) return;
    const rows = listRef.current?.querySelectorAll('[data-row="own"]');
    rows?.[focusRow]?.querySelector<HTMLElement>('input')?.focus();
    setFocusRow(null);
  }, [focusRow]);

  // Focus held by a row removed goes to the Delete of the row now in its
  // place, else of the one before it, else to Add.
  useEffect(() => {
    if (focusAfterRemove === null) return;
    const rows = listRef.current?.querySelectorAll('[data-row="own"]');
    const row = rows?.[focusAfterRemove] ?? rows?.[focusAfterRemove - 1];
    (
      row?.querySelector<HTMLElement>(':scope > button') ?? addRef.current
    )?.focus();
    setFocusAfterRemove(null);
  }, [focusAfterRemove]);

  const setRow = (index: number, row: ConfigObject) =>
    onChange(rows.map((existing, at) => (at === index ? row : existing)));

  const add = () => {
    if (disabled) return;
    onChange([...rows, defaultsOf(itemProp)]);
    setKeys([...keys, nextRowKey()]);
    setFocusRow(rows.length);
  };

  const remove = (index: number) => {
    if (disabled) return;
    const own = listRef.current?.querySelectorAll('[data-row="own"]');
    if (own?.[index]?.contains(document.activeElement)) {
      setFocusAfterRemove(index);
    }
    onChange(rows.filter((_, at) => at !== index));
    setKeys(keys.filter((_, at) => at !== index));
  };

  return (
    <div className={styles.list} ref={listRef}>
      {inherited.values.length > 0 && (
        <div className={styles.rows}>
          {inherited.values.map((row, index) => (
            <Row
              key={index}
              itemProp={itemProp}
              row={(row ?? {}) as ConfigObject}
              label={`${Noun} from ${inherited.source}`}
              locked
              disabled={disabled}
            />
          ))}
          <InheritedCaption source={inherited.source} />
        </div>
      )}
      {rows.length > 0 && (
        <div className={styles.rows}>
          {rows.map((row, index) => (
            <Row
              key={keys[index]}
              itemProp={itemProp}
              row={row}
              label={`${Noun} ${index + 1}`}
              disabled={disabled}
              invalid={invalid}
              onChange={(next) => setRow(index, next)}
              onBrowse={(key) => !disabled && setBrowsing({ index, key })}
              onRemove={() => remove(index)}
              removeLabel={`Delete ${noun} ${index + 1}`}
              path={rowPaths[index]}
              error={rowErrors[keys[index]]}
            />
          ))}
        </div>
      )}
      <span>
        <Button ref={addRef} onClick={add} aria-disabled={disabled}>
          Add {noun}
        </Button>
      </span>
      {browsing && (
        <FolderPicker
          initialPath={String(rows[browsing.index]?.[browsing.key] ?? '') || undefined}
          onSelect={(path) => {
            const row = rows[browsing.index];
            if (row) setRow(browsing.index, { ...row, [browsing.key]: path });
            setBrowsing(null);
          }}
          onCancel={() => setBrowsing(null)}
        />
      )}
    </div>
  );
}
