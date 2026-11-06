import { type FormEvent, type Ref, useMemo, useRef, useState } from 'react';
import type { SessionProviderSchemas } from '@homeshell/model';
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
  defaultsOf,
  fieldsOf,
  focusFirstField,
  pathOf,
} from './schema-form';
import styles from './new-workstream-form.module.css';

export interface NewSessionData {
  providerId: string;
  optionId: string;
  config: Record<string, unknown>;
}

export interface NewSessionFormProps {
  /** Session providers and their options, grouped by provider. */
  providers: SessionProviderSchemas[];
  onSubmit: (data: NewSessionData) => Promise<string | Refusal | null>;
  /** Called by Cancel and by Escape. */
  onCancel: () => void;
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

/**
 * Inline form for creating a session in a workstream. When more than one
 * provider is available a provider selector is shown; when the selected
 * provider offers more than one session type an option selector is shown.
 * onSubmit returns null on success, or an error message or refusal. It opens on
 * its first field, or on Start session when it has none, and a refusal moves
 * focus to the first field it blames.
 */
export function NewSessionForm({
  providers,
  onSubmit,
  onCancel,
  disabledReason,
  ref,
}: NewSessionFormProps) {
  const [selectedProviderId, setSelectedProviderId] = useState(providers[0]?.providerId ?? '');
  const selectedProvider = useMemo(
    () => providers.find((p) => p.providerId === selectedProviderId) ?? providers[0],
    [providers, selectedProviderId]
  );

  const [selectedOptionId, setSelectedOptionId] = useState(
    providers[0]?.options[0]?.id ?? ''
  );
  const selectedOption = useMemo(
    () =>
      selectedProvider?.options.find((o) => o.id === selectedOptionId) ??
      selectedProvider?.options[0],
    [selectedProvider, selectedOptionId]
  );

  const [config, setConfig] = useState<Record<string, ConfigValue>>(() =>
    defaultsOf(providers[0]?.options[0]?.schema)
  );
  const { formRef, error, clearError, clearFieldError, submitting, submit } =
    useSchemaSubmit(config);
  const blocked = Boolean(disabledReason);
  const inert = submitting || blocked;
  const submitRef = useRef<HTMLButtonElement>(null);

  useFormFocus(ref, () => {
    if (formRef.current && !focusFirstField(formRef.current)) {
      submitRef.current?.focus();
    }
  });

  if (providers.length === 0 || providers.every((p) => p.options.length === 0)) {
    return (
      <div className={styles.form}>
        <p className={styles.empty}>No agent is available to start a session here.</p>
        <div className={styles.actions}>
          <Button type="button" onClick={onCancel} mnemonic={MNEMONICS.cancel}>
            Cancel
          </Button>
        </div>
      </div>
    );
  }

  const fields = fieldsOf(selectedOption?.schema);

  const selectProvider = (id: string) => {
    if (inert) return;
    setSelectedProviderId(id);
    const provider = providers.find((p) => p.providerId === id);
    const firstOption = provider?.options[0];
    setSelectedOptionId(firstOption?.id ?? '');
    setConfig(defaultsOf(firstOption?.schema));
    clearError();
  };

  const selectOption = (id: string) => {
    if (inert) return;
    setSelectedOptionId(id);
    const option = selectedProvider?.options.find((o) => o.id === id);
    setConfig(defaultsOf(option?.schema));
    clearError();
  };

  const setField = (key: string, value: ConfigValue) => {
    setConfig((c) => ({ ...c, [key]: value }));
    clearFieldError(pathOf(key), config[key], value);
  };

  const handleSubmit = (e: FormEvent) => {
    e.preventDefault();
    if (!selectedProvider || !selectedOption || inert) return;
    void submit(fields, (config) =>
      onSubmit({
        providerId: selectedProvider.providerId,
        optionId: selectedOption.id,
        config,
      })
    );
  };

  return (
    <form
      ref={formRef}
      className={styles.form}
      onSubmit={handleSubmit}
      onKeyDown={cancelOnEscape(onCancel, submitting)}
      aria-label="New session"
    >
      {providers.length > 1 &&
        (providers.length <= PILL_MAX ? (
          <PillSelect
            label="Provider"
            options={providers.map((p) => ({ value: p.providerId, label: p.label }))}
            value={selectedProviderId}
            disabled={inert}
            onChange={selectProvider}
          />
        ) : (
          <label className={styles.label}>
            Provider
            <select
              className={styles.input}
              value={selectedProviderId}
              onChange={(e) => selectProvider(e.target.value)}
              aria-disabled={inert}
              aria-label="Provider"
            >
              {providers.map((p) => (
                <option key={p.providerId} value={p.providerId}>
                  {p.label}
                </option>
              ))}
            </select>
          </label>
        ))}
      {selectedProvider &&
        selectedProvider.options.length > 1 &&
        (selectedProvider.options.length <= PILL_MAX ? (
          <PillSelect
            label="Agent"
            options={selectedProvider.options.map((o) => ({ value: o.id, label: o.label }))}
            value={selectedOptionId}
            disabled={inert}
            onChange={selectOption}
          />
        ) : (
          <label className={styles.label}>
            Agent
            <select
              className={styles.input}
              value={selectedOptionId}
              onChange={(e) => selectOption(e.target.value)}
              aria-disabled={inert}
              aria-label="Agent"
            >
              {selectedProvider.options.map((o) => (
                <option key={o.id} value={o.id}>
                  {o.label}
                </option>
              ))}
            </select>
          </label>
        ))}
      {/* Keyed by the option, so another option's fields start afresh. */}
      {fields.map(({ key, prop, required }) => (
        <SchemaField
          key={`${selectedProvider?.providerId}/${selectedOption?.id}/${key}`}
          fieldKey={key}
          prop={prop}
          required={required}
          value={config[key]}
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
          ref={submitRef}
          type="submit"
          variant="primary"
          aria-disabled={inert}
          mnemonic={MNEMONICS.startStop}
          title={disabledReason}
        >
          Start session
        </Button>
      </div>
    </form>
  );
}

export default NewSessionForm;
