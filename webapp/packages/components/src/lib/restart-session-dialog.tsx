import { type FormEvent, useId, useState } from 'react';
import { Button } from './button';
import { ConfirmDialog } from './confirm-dialog';
import { type Refusal, useSchemaSubmit } from './create-form';
import type { FocusTarget } from './focus-return';
import { MNEMONICS } from './mnemonic';
import formStyles from './new-workstream-form.module.css';
import styles from './restart-session-dialog.module.css';
import {
  type ConfigValue,
  SchemaField,
  defaultsOf,
  fieldsOf,
  firstField,
  pathOf,
} from './schema-form';
import { sentence } from './sentence';

export interface RestartSessionData {
  /**
   * The settings to start it with; absent when the schema has no fields, so
   * the session starts with what it was last started with.
   */
  config?: Record<string, unknown>;
  /** Start the agent on a new conversation rather than continuing its last (default: continue). */
  fresh?: boolean;
}

export interface RestartSessionDialogProps {
  /** The session's title, named in the dialog's title. */
  sessionTitle: string;
  /** The session option's schema, as the new-session form renders it. */
  schema: unknown;
  /** What the session was last started with, prefilling the fields. */
  config?: Record<string, unknown>;
  /** Null on success, else an error message or refusal to show. */
  onSubmit: (data: RestartSessionData) => Promise<string | Refusal | null>;
  /** Cancel, Escape and a click outside. */
  onCancel: () => void;
  /** When set, Restart and the fields are inert, and Restart says why. */
  disabledReason?: string;
  /**
   * Why the session cannot continue its conversation: when set, the restart
   * starts a fresh one, and the dialog says why.
   */
  continueRefusal?: string;
  /**
   * What the restart costs when it ends the session where it runs (its
   * capabilities' takeOverWarning): the dialog says so before it is done.
   */
  takeOverWarning?: string;
  /**
   * Where focus goes on close when what it came from has left the page, such
   * as the session's row once the session is gone.
   */
  fallbackFocus?: FocusTarget;
}

/**
 * Restarts a session with settings: its option's schema form, filled with what
 * it was last started with and with every group open, since changing those is
 * what the dialog is for. A refusal shows under the fields it blames, as the
 * new-session form's does.
 */
export function RestartSessionDialog({
  sessionTitle,
  schema,
  config: launched,
  onSubmit,
  onCancel,
  disabledReason,
  continueRefusal,
  takeOverWarning,
  fallbackFocus,
}: RestartSessionDialogProps) {
  const formId = useId();
  const freshHintId = useId();
  const [config, setConfig] = useState<Record<string, ConfigValue>>(() =>
    defaultsOf(schema, launched),
  );
  const [fresh, setFresh] = useState(Boolean(continueRefusal));
  const { formRef, error, clearFieldError, submitting, submit } =
    useSchemaSubmit(config);
  const fields = fieldsOf(schema);
  const inert = submitting || Boolean(disabledReason);
  const freshLocked = inert || Boolean(continueRefusal);

  const handleSubmit = (event: FormEvent) => {
    event.preventDefault();
    if (inert) return;
    void submit(fields, (config) =>
      onSubmit(fields.length > 0 ? { config, fresh } : { fresh }),
    );
  };

  return (
    <ConfirmDialog
      title={`Restart ${sessionTitle}`}
      onClose={() => {
        if (!submitting) onCancel();
      }}
      size="wide"
      fallbackFocus={fallbackFocus}
      initialFocus={(dialog) => {
        const form = dialog.querySelector('form');
        return form && firstField(form);
      }}
      actions={
        <>
          <Button
            onClick={onCancel}
            disabled={submitting}
            mnemonic={MNEMONICS.cancel}
          >
            Cancel
          </Button>
          <Button
            type="submit"
            form={formId}
            variant="primary"
            aria-disabled={inert}
            mnemonic={MNEMONICS.startStop}
            title={disabledReason}
          >
            Restart
          </Button>
        </>
      }
    >
      <form
        id={formId}
        ref={formRef}
        className={styles.form}
        onSubmit={handleSubmit}
        aria-label="Restart settings"
      >
        {takeOverWarning && <p>{takeOverWarning}</p>}
        {fields.map(({ key, prop, required }) => (
          <SchemaField
            key={key}
            fieldKey={key}
            prop={prop}
            required={required}
            value={config[key]}
            disabled={inert}
            onChange={(value) => {
              setConfig((previous) => ({ ...previous, [key]: value }));
              clearFieldError(pathOf(key), config[key], value);
            }}
            errors={error?.fields}
            groupsOpen
          />
        ))}
        <div className={styles.fresh}>
          <label className={formStyles.checkboxLabel}>
            <input
              type="checkbox"
              checked={fresh}
              onChange={(event) => {
                if (!freshLocked) setFresh(event.target.checked);
              }}
              aria-describedby={freshHintId}
              aria-disabled={freshLocked}
            />
            Start a fresh conversation
          </label>
          <small id={freshHintId} className={formStyles.hint}>
            {continueRefusal
              ? sentence(continueRefusal)
              : "Otherwise the agent continues its latest conversation in this session's directory"}
          </small>
        </div>
        {error?.message && (
          <p className={formStyles.error} role="alert">
            {error.message}
          </p>
        )}
      </form>
    </ConfirmDialog>
  );
}

export default RestartSessionDialog;
