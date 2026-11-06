import {
  type KeyboardEvent,
  type Ref,
  useCallback,
  useEffect,
  useImperativeHandle,
  useLayoutEffect,
  useRef,
  useState,
} from 'react';
import { type FocusTarget, useReturnFocus } from './focus-return';
import {
  type ConfigValue,
  type Field,
  addDrafts,
  buildConfig,
  changedPaths,
  focusBlamedField,
  pathHolds,
  refusedFields,
} from './schema-form';
import { sentence } from './sentence';

/** What a create form's ref offers. */
export interface FormHandle {
  /** Focuses the field the form opens on, as it did when it opened. */
  focus: () => void;
}

/**
 * Whether an inline create form is open, and the ways it opens and closes.
 * `show` opens it, or focuses it again when it is open; `cancel` closes it with
 * focus going back to what had it when it opened, else to `fallback`; `close`
 * closes it with focus left alone, for a create that focuses what it made.
 * Give `formRef` to the form.
 */
export function useCreateForm(fallback: FocusTarget) {
  const [open, setOpen] = useState(false);
  const formRef = useRef<FormHandle>(null);
  const setReturnFocus = useReturnFocus(open, fallback);
  const close = useCallback(() => setOpen(false), []);
  return {
    open,
    formRef,
    show: () => (open ? formRef.current?.focus() : setOpen(true)),
    cancel: () => {
      setReturnFocus(true);
      setOpen(false);
    },
    close,
  };
}

/**
 * Puts focus where `focus` does as the form opens, and offers it on `ref` for
 * whoever wants the form focused again.
 */
export function useFormFocus(
  ref: Ref<FormHandle> | undefined,
  focus: () => void,
) {
  const latest = useRef(focus);
  useLayoutEffect(() => {
    latest.current = focus;
  });
  useImperativeHandle(ref, () => ({ focus: () => latest.current() }), []);
  useEffect(() => {
    latest.current();
  }, []);
}

/**
 * A create the daemon refused: the message to show, and the reasons it gives
 * by JSON pointer of the config field each blames.
 */
export interface Refusal {
  message: string;
  fields: Readonly<Record<string, string>>;
}

/**
 * What a create form shows of a refusal: the reasons under the fields they
 * blame, by field key, and as the message the reasons for anything the form
 * does not show — or the refusal's own message, when it blames no field of the
 * form.
 */
export interface FormError {
  message: string | null;
  fields: Readonly<Record<string, string>>;
}

/** What to show of what a create form's onSubmit refused with. */
function handleRefusal(
  form: HTMLFormElement | null,
  result: string | Refusal,
): FormError {
  const refusal =
    typeof result === 'string' ? { message: result, fields: {} } : result;
  const fields = form ? refusedFields(form, refusal.fields) : {};
  if (Object.keys(fields).length === 0) {
    return { message: refusal.message, fields };
  }
  const unshown = Object.entries(refusal.fields)
    .filter(
      ([pointer]) => !Object.keys(fields).some((path) => pathHolds(path, pointer)),
    )
    .map(([, reason]) => reason);
  return {
    message: unshown.length > 0 ? sentence(unshown.join('; ')) : null,
    fields,
  };
}

/**
 * Sends a schema form, as its submit handler does: adds what is typed in its
 * lists, as Enter would, then hands `send` the config its `fields` build from
 * `config` as last rendered, and shows what `send` refused with — a bare
 * message blames no field. While some typed text cannot be added, which
 * sending would drop, nothing is sent and the lists holding it are blamed.
 * Once the form shows a refusal, focus moves to the first field it blames: a
 * collapsed group holding it opens as it renders the error, not before. What
 * is left of it as fields are edited (see clearFieldError) leaves focus be. Give
 * `formRef` to the form.
 */
export function useSchemaSubmit(config: Readonly<Record<string, ConfigValue>>) {
  const formRef = useRef<HTMLFormElement>(null);
  const [error, setError] = useState<FormError | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const latestConfig = useRef(config);
  useLayoutEffect(() => {
    latestConfig.current = config;
  });
  // The refusal last shown by a submit, whose field focus has yet to move to:
  // what is left of it as fields are edited moves focus nowhere.
  const refused = useRef<FormError | null>(null);
  useLayoutEffect(() => {
    if (error && error === refused.current && formRef.current) {
      focusBlamedField(formRef.current, error.fields);
    }
    refused.current = null;
  }, [error]);
  const refuse = (refusal: FormError) => {
    refused.current = refusal;
    setError(refusal);
  };

  const submit = async (
    fields: Field[],
    send: (config: Record<string, unknown>) => Promise<string | Refusal | null>,
  ) => {
    const form = formRef.current;
    const unadded = form ? addDrafts(form) : {};
    if (Object.keys(unadded).length > 0) {
      refuse({ message: null, fields: unadded });
      return;
    }
    setError(null);
    setSubmitting(true);
    const result = await send(buildConfig(fields, latestConfig.current));
    setSubmitting(false);
    if (result) refuse(handleRefusal(formRef.current, result));
  };

  return {
    formRef,
    error,
    clearError: useCallback(() => setError(null), []),
    /**
     * Takes back what the shown refusal says about the field at config path
     * `key` (see clearFieldError), once `before` there is edited to `after`.
     */
    clearFieldError: useCallback(
      (key: string, before: unknown, after: unknown) =>
        setError((shown) => clearEditedFields(shown, key, before, after)),
      [],
    ),
    submitting,
    submit,
  };
}

/**
 * What is left of `error` once the user edits the fields at config paths
 * `keys` (see pathOf): their own reasons go, since they are about values that
 * are no longer there, with those of the fields inside them and of the groups
 * holding them, and so does the form's message, which a refusal that blamed no
 * field of the form leaves as the only account of what to change. Reasons for
 * fields not touched stay — they are still true. Null once nothing is left to
 * show.
 */
export function clearFieldError(
  error: FormError | null,
  ...keys: string[]
): FormError | null {
  if (!error) return null;
  const fields = Object.fromEntries(
    Object.entries(error.fields).filter(
      ([field]) => !keys.some((key) => pathsMeet(field, key)),
    ),
  );
  return Object.keys(fields).length > 0 ? { message: null, fields } : null;
}

/**
 * clearFieldError for an edit of the field at config path `key` from `before`
 * to `after`: in a group, only the fields within it that changed — and the
 * group itself, whose value they are part of — lose their reasons.
 */
function clearEditedFields(
  error: FormError | null,
  key: string,
  before: unknown,
  after: unknown,
): FormError | null {
  const changed = changedPaths(key, before, after);
  return clearFieldError(error, ...(changed.length > 0 ? changed : [key]));
}

/**
 * Whether a reason given for the field at path `field` is about the value at
 * path `edited`: the same field, one inside it, or the group holding it.
 */
function pathsMeet(field: string, edited: string): boolean {
  return pathHolds(field, `/${edited}`) || pathHolds(edited, `/${field}`);
}

/**
 * A create form's keydown handler: Escape cancels, unless it is submitting or
 * an input method is composing, for which Escape drops the composition.
 */
export function cancelOnEscape(onCancel: () => void, submitting: boolean) {
  return (event: KeyboardEvent) => {
    if (event.key !== 'Escape' || submitting || event.nativeEvent.isComposing) {
      return;
    }
    event.preventDefault();
    onCancel();
  };
}
