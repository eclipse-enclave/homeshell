import {
  type FormEvent,
  type Ref,
  useEffect,
  useId,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from 'react';
import type { Provider } from '@homeshell/model';
import { Button } from './button';
import { MNEMONICS } from './mnemonic';
import {
  type FormHandle,
  type Refusal,
  cancelOnEscape,
  useFormFocus,
  useSchemaSubmit,
} from './create-form';
import { PILL_MAX, PillSelect } from './pill-select';
import {
  type ConfigValue,
  SchemaField,
  buildConfig,
  defaultsOf,
  fieldsOf,
  pathOf,
} from './schema-form';
import styles from './new-workstream-form.module.css';
import { sentence } from './sentence';

export interface NewWorkstreamData {
  providerId: string;
  name: string;
  config: Record<string, unknown>;
}

/** What a create would derive for the values a form leaves blank. */
export interface WorkstreamDraft {
  name: string;
  config: Readonly<Record<string, string>>;
}

/** How long the form waits after an edit before asking for the defaults again. */
const DRAFT_DELAY_MS = 150;

export interface NewWorkstreamFormProps {
  /** Providers that can create a workstream on the project, each with a schema. */
  providers: Provider[];
  onSubmit: (data: NewWorkstreamData) => Promise<string | Refusal | null>;
  /** Called by Cancel and by Escape. */
  onCancel: () => void;
  /** Provider to preselect; falls back to the first provider when absent or unknown. */
  defaultProviderId?: string;
  /** Initial values for the selected provider's schema fields, e.g. a preferred strategy. */
  defaultConfig?: Record<string, unknown>;
  /** Prefilled workstream name. */
  defaultName?: string;
  /**
   * What a create of the form's values would derive (the name, a worktree's
   * branch, a path): the form shows it in each field the user has not edited,
   * kept up to date as they edit the rest. Must be stable across renders.
   */
  resolveDraft?: (data: NewWorkstreamData) => Promise<WorkstreamDraft | null>;
  /**
   * When set, the form is inert — fields and submit both — with this text as the
   * reason: what is filled in here exists to be sent, so there is nothing to add to
   * it until the daemon is back. Whatever is already typed is kept, focus stays
   * where it was, and Cancel stays live.
   */
  disabledReason?: string;
  /** Focuses the form again, e.g. when what opened it is asked for once more. */
  ref?: Ref<FormHandle>;
}

/** The provider to preselect: the caller's preferred one if offered, else the first. */
function initialProviderId(providers: Provider[], preferred?: string): string {
  if (preferred && providers.some((p) => p.id === preferred)) return preferred;
  return providers[0]?.id ?? '';
}

/** draft with the value it derives for key blank. */
function blankIn(draft: WorkstreamDraft, key: string): WorkstreamDraft {
  if (key === 'name') return { ...draft, name: '' };
  if (!(key in draft.config)) return draft;
  return { ...draft, config: { ...draft.config, [key]: '' } };
}

/**
 * Inline form for creating a workstream. The user picks a provider and fills the
 * fields described by that provider's JSON Schema; onSubmit returns null on
 * success, or an error message or refusal. It opens on the name, selected, and a
 * refusal moves focus to the first field it blames.
 */
export function NewWorkstreamForm({
  providers,
  onSubmit,
  onCancel,
  defaultProviderId,
  defaultConfig,
  defaultName,
  resolveDraft,
  disabledReason,
  ref,
}: NewWorkstreamFormProps) {
  const [selectedId, setSelectedId] = useState(() =>
    initialProviderId(providers, defaultProviderId)
  );
  const selected = useMemo(
    () => providers.find((p) => p.id === selectedId) ?? providers[0],
    [providers, selectedId]
  );
  const [name, setName] = useState(defaultName ?? '');
  const [config, setConfig] = useState<Record<string, ConfigValue>>(() =>
    defaultsOf(
      providers.find((p) => p.id === initialProviderId(providers, defaultProviderId))
        ?.workstreamSchema,
      defaultConfig
    )
  );
  // The fields the user has edited. The rest show what the daemon derives and
  // are sent blank, so the create derives them afresh.
  const [edited, setEdited] = useState<ReadonlySet<string>>(
    () => new Set(defaultName ? ['name'] : [])
  );
  // The daemon's last draft, kept while the next is pending.
  const [draft, setDraft] = useState<WorkstreamDraft | null>(null);
  const fields = fieldsOf(selected?.workstreamSchema);
  const derived = (key: string) =>
    !edited.has(key) && draft !== null && key in draft.config;
  // What is sent: the derived values left out, so the create derives them.
  const sentConfig = Object.fromEntries(
    Object.entries(config).filter(([key]) => !derived(key))
  );
  const { formRef, error, clearError, clearFieldError, submitting, submit } =
    useSchemaSubmit(sentConfig);
  const blocked = Boolean(disabledReason);
  const inert = submitting || blocked;
  const nameRef = useRef<HTMLInputElement>(null);
  const nameErrorId = useId();

  useFormFocus(ref, () => {
    nameRef.current?.focus();
    nameRef.current?.select();
  });

  const providerId = selected?.id ?? '';
  const sentName = edited.has('name') ? name.trim() : '';
  const data: NewWorkstreamData = {
    providerId,
    name: sentName,
    config: buildConfig(fields, sentConfig),
  };
  const request = JSON.stringify(data);
  const shownName = edited.has('name') || !draft ? name : draft.name;
  const shownConfig = {
    ...config,
    ...Object.fromEntries(
      Object.entries(draft?.config ?? {}).filter(([key]) => !edited.has(key))
    ),
  };

  useEffect(() => {
    if (!resolveDraft || !providerId) return;
    let current = true;
    const timer = setTimeout(async () => {
      const answer = await resolveDraft(JSON.parse(request));
      if (current) setDraft(answer);
    }, DRAFT_DELAY_MS);
    return () => {
      current = false;
      clearTimeout(timer);
    };
  }, [request, resolveDraft, providerId]);

  // A derived value arriving in the focused box is selected, so typing replaces
  // it rather than adding to it.
  useLayoutEffect(() => {
    const focused = document.activeElement;
    if (
      !(focused instanceof HTMLInputElement) ||
      focused.type !== 'text' ||
      !formRef.current?.contains(focused)
    ) {
      return;
    }
    const key =
      focused === nameRef.current
        ? 'name'
        : focused.closest('[data-field]')?.getAttribute('data-field');
    if (key && !edited.has(key)) focused.select();
  }, [draft, edited]);

  if (providers.length === 0) {
    return (
      <div className={styles.form}>
        <p className={styles.empty}>No provider can create a workstream here.</p>
        <div className={styles.actions}>
          <Button type="button" onClick={onCancel} mnemonic={MNEMONICS.cancel}>
            Cancel
          </Button>
        </div>
      </div>
    );
  }

  const nameError = error?.fields.name;
  // A field changed is edited, and one cleared to empty is not: it shows what
  // the daemon derives again.
  const edit = (key: string, value: ConfigValue) => {
    clearFieldError(
      key === 'name' ? key : pathOf(key),
      key === 'name' ? name : config[key],
      value
    );
    const cleared = value === '';
    if (edited.has(key) !== cleared) return;
    const next = new Set(edited);
    if (cleared) {
      next.delete(key);
      // The last draft's value is the one just deleted.
      setDraft((last) => last && blankIn(last, key));
    } else {
      next.add(key);
    }
    setEdited(next);
  };

  const selectProvider = (id: string) => {
    if (inert || id === selectedId) return;
    setSelectedId(id);
    setDraft(null);
    setEdited((keys) => new Set(keys.has('name') ? ['name'] : []));
    setConfig(
      defaultsOf(providers.find((p) => p.id === id)?.workstreamSchema, defaultConfig)
    );
    clearError();
  };

  const setField = (key: string, value: ConfigValue) => {
    setConfig((c) => ({ ...c, [key]: value }));
    edit(key, value);
  };

  const handleSubmit = (e: FormEvent) => {
    e.preventDefault();
    if (!selected || inert) return;
    void submit(fields, (config) =>
      onSubmit({ providerId: selected.id, name: sentName, config })
    );
  };

  return (
    // Named, like the new-session form: it gives the form an addressable role, so
    // a refusal rendered inside it can be found without reaching for a test id.
    <form
      ref={formRef}
      className={styles.form}
      onSubmit={handleSubmit}
      onKeyDown={cancelOnEscape(onCancel, submitting)}
      aria-label="New workstream"
    >
      {providers.length <= PILL_MAX ? (
        <PillSelect
          label="Provider"
          options={providers.map((p) => ({ value: p.id, label: p.label }))}
          value={selectedId}
          disabled={inert}
          onChange={selectProvider}
        />
      ) : (
        <label className={styles.label}>
          Provider
          <select
            className={styles.input}
            value={selectedId}
            onChange={(e) => selectProvider(e.target.value)}
            aria-disabled={inert}
          >
            {providers.map((p) => (
              <option key={p.id} value={p.id}>
                {p.label}
              </option>
            ))}
          </select>
        </label>
      )}
      <label className={styles.label} data-field="name">
        Name
        <input
          ref={nameRef}
          className={styles.input}
          type="text"
          value={shownName}
          onChange={(e) => {
            setName(e.target.value);
            edit('name', e.target.value);
          }}
          aria-label="Name"
          placeholder="defaults to a generated name"
          readOnly={inert}
          aria-disabled={inert}
          {...(nameError && { 'aria-invalid': true, 'aria-describedby': nameErrorId })}
        />
        {nameError && (
          <small id={nameErrorId} className={styles.fieldError}>
            {sentence(nameError)}
          </small>
        )}
      </label>
      {fields.map(({ key, prop, required }) => (
        <SchemaField
          key={key}
          fieldKey={key}
          prop={prop}
          required={required}
          value={shownConfig[key]}
          disabled={inert}
          onChange={(v) => setField(key, v)}
          errors={error?.fields}
        />
      ))}
      {error?.message && (
        <p className={styles.error} role="alert">
          {error.message}
        </p>
      )}
      <div className={styles.actions}>
        <Button
          type="button"
          onClick={onCancel}
          disabled={submitting}
          mnemonic={MNEMONICS.cancel}
        >
          Cancel
        </Button>
        <Button
          type="submit"
          variant="primary"
          aria-disabled={inert}
          mnemonic={MNEMONICS.create}
          title={disabledReason}
        >
          Create
        </Button>
      </div>
    </form>
  );
}

export default NewWorkstreamForm;
