import { act, fireEvent, render, screen } from '@testing-library/react';
import { type Ref, useRef } from 'react';

import {
  clearFieldError,
  type FormHandle,
  type Refusal,
  useCreateForm,
  useFormFocus,
  useSchemaSubmit,
} from './create-form';
import { fieldsOf } from './schema-form';

/**
 * A schema form with a Name field, and a Path field shown only once a refusal
 * blames something, sending what `send` answers.
 */
function Form({
  send,
  draft = false,
}: {
  send: (config: Record<string, unknown>) => Promise<string | Refusal | null>;
  draft?: boolean;
}) {
  const { formRef, error, clearError, clearFieldError, submitting, submit } =
    useSchemaSubmit({ name: 'x' });
  return (
    <form
      ref={formRef}
      aria-label="New"
      onSubmit={(event) => {
        event.preventDefault();
        void submit(fieldsOf({ properties: { name: { type: 'string' } } }), send);
      }}
    >
      <label data-field="name">
        Name
        <input
          aria-invalid={Boolean(error?.fields.name) || undefined}
          onChange={(event) => clearFieldError('name', 'x', event.target.value)}
        />
      </label>
      <div hidden={!error}>
        <label data-field="a~1b">
          Path
          <input aria-invalid={Boolean(error?.fields['a~1b']) || undefined} />
        </label>
      </div>
      <div data-field="tags">
        <input aria-label="Add to Tags" data-draft={draft ? '' : undefined} />
        {error?.fields.tags && <small>{error.fields.tags}</small>}
      </div>
      {error?.message && <p role="alert">{error.message}</p>}
      <button type="submit" aria-disabled={submitting}>
        Send
      </button>
      <button type="button" onClick={clearError}>
        Clear
      </button>
    </form>
  );
}

const send = async () => {
  fireEvent.click(screen.getByRole('button', { name: 'Send' }));
  await act(async () => undefined);
};
const invalid = (name: string) =>
  screen.getByRole('textbox', { name }).getAttribute('aria-invalid') === 'true';

describe('useSchemaSubmit', () => {
  it('sends the config the fields build, and shows nothing when it is taken', async () => {
    const onSend = vi.fn().mockResolvedValue(null);
    render(<Form send={onSend} />);
    await send();
    expect(onSend).toHaveBeenCalledWith({ name: 'x' });
    expect(screen.queryByRole('alert')).toBeNull();
    expect(document.activeElement).toBe(document.body);
  });

  it('is submitting until the answer comes', async () => {
    let answer: (result: null) => void = () => undefined;
    render(<Form send={() => new Promise((resolve) => (answer = resolve))} />);
    fireEvent.click(screen.getByRole('button', { name: 'Send' }));
    expect(
      screen.getByRole('button', { name: 'Send' }).getAttribute('aria-disabled'),
    ).toBe('true');
    await act(async () => answer(null));
    expect(
      screen.getByRole('button', { name: 'Send' }).getAttribute('aria-disabled'),
    ).toBe('false');
  });

  it('marks the fields blamed, and focuses the first in form order', async () => {
    render(
      <Form
        send={async () => ({
          message: 'Path: bad; Name: taken',
          fields: { '/a~1b': 'bad', '/name': 'taken' },
        })}
      />,
    );
    await send();
    expect(invalid('Name')).toBe(true);
    expect(invalid('Path')).toBe(true);
    expect(screen.queryByRole('alert')).toBeNull();
    expect(document.activeElement).toBe(
      screen.getByRole('textbox', { name: 'Name' }),
    );
  });

  it('focuses a field hidden until the refusal shows it, each time', async () => {
    render(
      <Form send={async () => ({ message: 'bad', fields: { '/a~1b': 'bad' } })} />,
    );
    await send();
    expect(document.activeElement).toBe(
      screen.getByRole('textbox', { name: 'Path' }),
    );
    act(() => screen.getByRole('textbox', { name: 'Name' }).focus());
    await send();
    expect(document.activeElement).toBe(
      screen.getByRole('textbox', { name: 'Path' }),
    );
  });

  // A reason for something the form does not show is not dropped with it.
  it('says the reasons for what the form does not show beside the fields it does', async () => {
    render(
      <Form
        send={async () => ({
          message: 'Name: taken; bad; missing',
          fields: { '/name': 'taken', '/': 'bad', '/other': 'missing' },
        })}
      />,
    );
    await send();
    expect(invalid('Name')).toBe(true);
    expect(screen.getByRole('alert').textContent).toBe('Bad; missing');
  });

  it('keeps the message of a refusal that blames no field, moving no focus', async () => {
    const answers = [{ message: 'Bad', fields: { '/': 'bad' } }, 'Refused'];
    render(<Form send={async () => answers.shift() ?? null} />);
    const button = screen.getByRole('button', { name: 'Send' });
    act(() => button.focus());
    await send();
    expect(screen.getByRole('alert').textContent).toBe('Bad');
    await send();
    expect(screen.getByRole('alert').textContent).toBe('Refused');
    expect(document.activeElement).toBe(button);

    fireEvent.click(screen.getByRole('button', { name: 'Clear' }));
    expect(screen.queryByRole('alert')).toBeNull();
  });

  it('sends nothing while typed text cannot be added, and blames its list', async () => {
    const onSend = vi.fn().mockResolvedValue(null);
    render(<Form send={onSend} draft />);
    await send();
    expect(onSend).not.toHaveBeenCalled();
    expect(screen.getByText(/^not added/)).toBeTruthy();
    expect(document.activeElement).toBe(
      screen.getByRole('textbox', { name: 'Add to Tags' }),
    );
  });
});

describe('useCreateForm', () => {
  function Form({ ref }: { ref: Ref<FormHandle> }) {
    const input = useRef<HTMLInputElement>(null);
    useFormFocus(ref, () => input.current?.focus());
    return <input ref={input} aria-label="Name" />;
  }

  function Page() {
    const opener = useRef<HTMLButtonElement>(null);
    const form = useCreateForm(() => opener.current);
    return (
      <>
        <button ref={opener} type="button" onClick={form.show}>
          New
        </button>
        <button type="button" onClick={form.cancel}>
          Cancel
        </button>
        <button type="button" onClick={form.close}>
          Created
        </button>
        {form.open && <Form ref={form.formRef} />}
      </>
    );
  }

  const button = (name: string) => screen.getByRole('button', { name });

  it('opens the form, and focuses it again when asked once more', () => {
    render(<Page />);
    fireEvent.click(button('New'));
    const name = screen.getByRole('textbox', { name: 'Name' });
    expect(document.activeElement).toBe(name);

    act(() => button('New').focus());
    fireEvent.click(button('New'));
    expect(document.activeElement).toBe(name);
  });

  it('gives focus back on cancel, and leaves it alone on close', () => {
    render(<Page />);
    act(() => button('New').focus());
    fireEvent.click(button('New'));
    fireEvent.click(button('Cancel'));
    expect(screen.queryByRole('textbox')).toBeNull();
    expect(document.activeElement).toBe(button('New'));

    fireEvent.click(button('New'));
    act(() => button('Created').focus());
    fireEvent.click(button('Created'));
    expect(screen.queryByRole('textbox')).toBeNull();
    expect(document.activeElement).toBe(button('Created'));
  });
});

describe('useSchemaSubmit clearFieldError', () => {
  it("drops an edited field's reason without moving focus to the rest", async () => {
    render(
      <Form
        send={async () => ({
          message: 'bad',
          fields: { '/name': 'taken', '/a~1b': 'bad' },
        })}
      />,
    );
    await send();
    expect(document.activeElement).toBe(
      screen.getByRole('textbox', { name: 'Name' }),
    );
    act(() => screen.getByRole('button', { name: 'Clear' }).focus());
    fireEvent.change(screen.getByRole('textbox', { name: 'Name' }), {
      target: { value: 'y' },
    });
    expect(invalid('Name')).toBe(false);
    expect(invalid('Path')).toBe(true);
    expect(document.activeElement).toBe(
      screen.getByRole('button', { name: 'Clear' }),
    );
  });
});

describe('clearFieldError', () => {
  it('drops the reasons inside an edited group, and the group its field is in', () => {
    expect(
      clearFieldError(
        {
          message: null,
          fields: {
            sandbox: 'conflicting',
            'sandbox/ports': 'bad port',
            'sandbox/mounts': 'missing',
            name: 'taken',
          },
        },
        'sandbox/ports',
      ),
    ).toEqual({
      message: null,
      fields: { 'sandbox/mounts': 'missing', name: 'taken' },
    });
    expect(
      clearFieldError(
        { message: null, fields: { 'sandbox/ports': 'bad', name: 'taken' } },
        'sandbox',
      ),
    ).toEqual({ message: null, fields: { name: 'taken' } });
  });

  it('does not take a key for one that merely starts like it', () => {
    expect(
      clearFieldError({ message: null, fields: { names: 'bad' } }, 'name'),
    ).toEqual({ message: null, fields: { names: 'bad' } });
  });

  it("drops the edited field's reason and the message, keeping the rest", () => {
    expect(
      clearFieldError(
        {
          message: 'other: bad',
          fields: { name: 'taken', branch: 'required' },
        },
        'name',
      ),
    ).toEqual({ message: null, fields: { branch: 'required' } });
  });

  it('leaves nothing once the last reason goes', () => {
    expect(
      clearFieldError({ message: null, fields: { name: 'taken' } }, 'name'),
    ).toBeNull();
    expect(
      clearFieldError({ message: 'refused', fields: {} }, 'name'),
    ).toBeNull();
    expect(clearFieldError(null, 'name')).toBeNull();
  });
});
