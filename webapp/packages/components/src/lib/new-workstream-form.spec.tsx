import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react';
import type { Provider } from '@homeshell/model';

import type { FormHandle } from './create-form';
import {
  type NewWorkstreamData,
  NewWorkstreamForm,
  type NewWorkstreamFormProps,
  type WorkstreamDraft,
} from './new-workstream-form';

const providers: Provider[] = [
  {
    id: 'fake',
    label: 'Fake Provider',
    status: { state: 'available' },
    capabilities: {
      openModes: ['terminal'],
      startStop: true,
      oneOffPrompt: false,
      sessionStartStop: true,
      sessionRestart: true,
      maxConcurrentSessions: 0,
      detectsWaiting: true,
    },
    workstreamSchema: {
      type: 'object',
      required: ['branch'],
      properties: {
        branch: { type: 'string', title: 'Branch' },
        copyEnv: { type: 'boolean', title: 'Copy .env', default: false },
      },
    },
  },
];

describe('NewWorkstreamForm', () => {
  it('renders schema-driven fields for the selected type', () => {
    render(
      <NewWorkstreamForm providers={providers} onSubmit={vi.fn()} onCancel={vi.fn()} />
    );
    expect(screen.getByLabelText('Branch')).toBeTruthy();
    expect(screen.getByRole('checkbox', { name: 'Copy .env' })).toBeTruthy();
  });

  it('gives its buttons the letters Alt presses them by', () => {
    render(
      <NewWorkstreamForm providers={providers} onSubmit={vi.fn()} onCancel={vi.fn()} />
    );
    const letter = (name: string) =>
      screen.getByRole('button', { name }).getAttribute('data-mnemonic');
    expect([letter('Cancel'), letter('Create')]).toEqual(['c', 'r']);
  });

  it('submits the provider, name and config', async () => {
    const onSubmit = vi.fn().mockResolvedValue(null);
    render(
      <NewWorkstreamForm providers={providers} onSubmit={onSubmit} onCancel={vi.fn()} />
    );

    fireEvent.change(screen.getByLabelText('Branch'), {
      target: { value: 'feature/x' },
    });
    fireEvent.click(screen.getByRole('checkbox', { name: 'Copy .env' }));
    fireEvent.click(screen.getByRole('button', { name: 'Create' }));

    await waitFor(() => {
      expect(onSubmit).toHaveBeenCalledWith({
        providerId: 'fake',
        name: '',
        config: { branch: 'feature/x', copyEnv: true },
      });
    });
  });

  it('surfaces an error returned by onSubmit, inside the named form', async () => {
    const onSubmit = vi.fn().mockResolvedValue('configuration is invalid');
    render(
      <NewWorkstreamForm providers={providers} onSubmit={onSubmit} onCancel={vi.fn()} />
    );
    fireEvent.click(screen.getByRole('button', { name: 'Create' }));
    // Scoped to the form: the page it lives on has other alerts (a notification
    // toast is one), so the refusal has to be findable as *this* form's.
    const form = screen.getByRole('form', { name: 'New workstream' });
    const alert = await within(form).findByRole('alert');
    expect(alert.textContent).toBe('configuration is invalid');
  });

  it('shows a message and only a Cancel action when no provider can create', () => {
    const onCancel = vi.fn();
    render(<NewWorkstreamForm providers={[]} onSubmit={vi.fn()} onCancel={onCancel} />);
    expect(screen.getByText(/No provider can create a workstream/)).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Create' })).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(onCancel).toHaveBeenCalled();
  });

  it('applies configured defaults for provider, config and name', () => {
    const twoProviders: Provider[] = [
      providers[0],
      {
        id: 'local',
        label: 'Local System',
        status: { state: 'available' },
        capabilities: {
          openModes: [],
          startStop: false,
          oneOffPrompt: false,
          sessionStartStop: false,
          sessionRestart: false,
          maxConcurrentSessions: 0,
          detectsWaiting: false,
        },
        workstreamSchema: {
          type: 'object',
          properties: {
            strategy: {
              type: 'string',
              title: 'Filesystem strategy',
              enum: ['in-place', 'worktree', 'copy'],
              default: 'in-place',
            },
          },
        },
      },
    ];
    render(
      <NewWorkstreamForm
        providers={twoProviders}
        onSubmit={vi.fn()}
        onCancel={vi.fn()}
        defaultProviderId="local"
        defaultConfig={{ strategy: 'worktree' }}
        defaultName="my-ws"
      />
    );
    // Provider renders as pill radio buttons; the "Local System" pill should be selected.
    expect(
      screen.getByRole('radio', { name: 'Local System' }).getAttribute('aria-checked')
    ).toBe('true');
    expect((screen.getByLabelText('Name') as HTMLInputElement).value).toBe('my-ws');
    // Filesystem strategy also renders as pills; "worktree" should be selected.
    expect(
      screen.getByRole('radio', { name: 'worktree' }).getAttribute('aria-checked')
    ).toBe('true');
  });

  // Offline: the same rule as the session form above — fields and Create go
  // inert, Cancel stays live, and the draft survives so nothing typed is lost.
  describe('when the daemon is unreachable', () => {
    const REASON = 'The daemon is offline';

    it('makes the name, the schema fields and Create inert', () => {
      const onSubmit = vi.fn().mockResolvedValue(null);
      render(
        <NewWorkstreamForm
          providers={providers}
          onSubmit={onSubmit}
          onCancel={vi.fn()}
          disabledReason={REASON}
        />,
      );
      for (const label of ['Name', 'Branch']) {
        const field = screen.getByLabelText(label) as HTMLInputElement;
        expect(field.readOnly).toBe(true);
        expect(field.getAttribute('aria-disabled')).toBe('true');
      }
      const create = screen.getByRole('button', { name: 'Create' });
      expect(create.getAttribute('aria-disabled')).toBe('true');
      expect(create.title).toBe(REASON);
      fireEvent.click(create);
      expect(onSubmit).not.toHaveBeenCalled();
    });

    it('keeps focus on the field being typed in', () => {
      const props = {
        providers,
        onSubmit: vi.fn().mockResolvedValue(null),
        onCancel: vi.fn(),
      };
      const { rerender } = render(<NewWorkstreamForm {...props} />);
      const branch = screen.getByLabelText('Branch');
      branch.focus();
      rerender(<NewWorkstreamForm {...props} disabledReason={REASON} />);
      expect(document.activeElement).toBe(branch);
    });

    it('leaves Cancel usable and keeps the draft', () => {
      const onCancel = vi.fn();
      const { rerender } = render(
        <NewWorkstreamForm
          providers={providers}
          onSubmit={vi.fn().mockResolvedValue(null)}
          onCancel={onCancel}
        />,
      );
      fireEvent.change(screen.getByLabelText('Name'), {
        target: { value: 'typed-before-the-drop' },
      });
      rerender(
        <NewWorkstreamForm
          providers={providers}
          onSubmit={vi.fn().mockResolvedValue(null)}
          onCancel={onCancel}
          disabledReason={REASON}
        />,
      );
      expect((screen.getByLabelText('Name') as HTMLInputElement).value).toBe(
        'typed-before-the-drop',
      );
      fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
      expect(onCancel).toHaveBeenCalled();
    });
  });
});

describe('NewWorkstreamForm when the daemon is unreachable', () => {
  const REASON = 'The daemon is offline';

  it('makes Create inert but keeps Cancel live', () => {
    const onCancel = vi.fn();
    render(
      <NewWorkstreamForm
        providers={providers}
        onSubmit={vi.fn()}
        onCancel={onCancel}
        disabledReason={REASON}
      />
    );
    const submit = screen.getByRole('button', { name: 'Create' });
    expect(submit.getAttribute('aria-disabled')).toBe('true');
    expect(submit.getAttribute('title')).toBe(REASON);
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(onCancel).toHaveBeenCalled();
  });

  // The fields are what would be sent, so they are inert with the submit.
  it('makes the fields inert as well', () => {
    render(
      <NewWorkstreamForm
        providers={providers}
        onSubmit={vi.fn()}
        onCancel={vi.fn()}
        disabledReason={REASON}
      />
    );
    const name = screen.getByPlaceholderText(
      'defaults to a generated name'
    ) as HTMLInputElement;
    expect(name.readOnly).toBe(true);
  });
});

describe('NewWorkstreamForm by keyboard', () => {
  const renderForm = (
    props: Partial<Parameters<typeof NewWorkstreamForm>[0]> = {}
  ) =>
    render(
      <NewWorkstreamForm
        providers={providers}
        onSubmit={vi.fn().mockResolvedValue(null)}
        onCancel={vi.fn()}
        defaultName="ws-1"
        {...props}
      />
    );
  const name = () => screen.getByLabelText('Name') as HTMLInputElement;

  it('opens on the name, its prefilled text selected', () => {
    renderForm();
    expect(document.activeElement).toBe(name());
    expect([name().selectionStart, name().selectionEnd]).toEqual([0, 4]);
  });

  it('focuses the name again through its ref', () => {
    const ref = { current: null as FormHandle | null };
    renderForm({ ref });
    screen.getByLabelText('Branch').focus();
    ref.current?.focus();
    expect(document.activeElement).toBe(name());
  });

  it('cancels on Escape', () => {
    const onCancel = vi.fn();
    renderForm({ onCancel });
    fireEvent.keyDown(name(), { key: 'Escape' });
    expect(onCancel).toHaveBeenCalled();
  });

  it('leaves Escape to an input method that is composing', () => {
    const onCancel = vi.fn();
    renderForm({ onCancel });
    fireEvent.keyDown(name(), { key: 'Escape', isComposing: true });
    expect(onCancel).not.toHaveBeenCalled();
  });

  it('keeps Create focused, and submits once, while the request is out', () => {
    const onSubmit = vi.fn(() => new Promise<null>(() => undefined));
    const onCancel = vi.fn();
    renderForm({ onSubmit, onCancel });
    const create = screen.getByRole('button', { name: 'Create' });
    create.focus();
    fireEvent.click(create);
    fireEvent.submit(screen.getByRole('form', { name: 'New workstream' }));
    fireEvent.keyDown(create, { key: 'Escape' });

    expect(onSubmit).toHaveBeenCalledTimes(1);
    expect(onCancel).not.toHaveBeenCalled();
    expect(create.getAttribute('aria-disabled')).toBe('true');
    expect(document.activeElement).toBe(create);
  });

  it('marks the field a refusal blames, says why under it, and focuses it', async () => {
    renderForm({
      onSubmit: vi.fn().mockResolvedValue({
        message: 'Branch: required',
        fields: { '/branch': 'required' },
      }),
    });
    fireEvent.submit(screen.getByRole('form', { name: 'New workstream' }));

    const branch = screen.getByLabelText('Branch');
    await waitFor(() => expect(branch.getAttribute('aria-invalid')).toBe('true'));
    expect(
      document.getElementById(branch.getAttribute('aria-describedby') ?? '')
        ?.textContent,
    ).toBe('Required');
    expect(document.activeElement).toBe(branch);
    // Said once, where it applies.
    expect(screen.queryByRole('alert')).toBeNull();
  });

  it('marks the name when a refusal blames it, and says why under it', async () => {
    renderForm({
      onSubmit: vi.fn().mockResolvedValue({
        message: 'Name: "copy one" is already used in this project; enter another',
        fields: { '/name': '"copy one" is already used in this project; enter another' },
      }),
    });
    fireEvent.change(name(), { target: { value: 'copy one' } });
    fireEvent.submit(screen.getByRole('form', { name: 'New workstream' }));

    await waitFor(() => expect(name().getAttribute('aria-invalid')).toBe('true'));
    expect(
      document.getElementById(name().getAttribute('aria-describedby') ?? '')
        ?.textContent,
    ).toBe('"copy one" is already used in this project; enter another');
    expect(document.activeElement).toBe(name());
    expect(name()).toHaveProperty('value', 'copy one');
    expect(screen.queryByRole('alert')).toBeNull();
  });

  // The reason was about a value that is no longer there: a corrected field
  // must not go on saying it is wrong until the next Create.
  it('drops a reason once its field is edited, keeping the others', async () => {
    renderForm({
      onSubmit: vi.fn().mockResolvedValue({
        message: 'refused',
        fields: { '/branch': 'required', '/name': 'taken' },
      }),
    });
    fireEvent.submit(screen.getByRole('form', { name: 'New workstream' }));
    const branch = screen.getByLabelText('Branch');
    await waitFor(() =>
      expect(branch.getAttribute('aria-invalid')).toBe('true'),
    );

    fireEvent.change(branch, { target: { value: 'main' } });
    expect(branch.hasAttribute('aria-invalid')).toBe(false);
    expect(name().getAttribute('aria-invalid')).toBe('true');

    fireEvent.change(name(), { target: { value: 'free' } });
    expect(name().hasAttribute('aria-invalid')).toBe(false);
  });

  it("drops the form's message once anything is edited", async () => {
    renderForm({
      onSubmit: vi.fn().mockResolvedValue('path must be absolute'),
    });
    fireEvent.submit(screen.getByRole('form', { name: 'New workstream' }));
    await screen.findByRole('alert');

    fireEvent.change(screen.getByLabelText('Branch'), {
      target: { value: 'main' },
    });
    expect(screen.queryByRole('alert')).toBeNull();
  });

  it('keeps focus where it was on a refusal that blames no field', async () => {
    renderForm({ onSubmit: vi.fn().mockResolvedValue('name in use') });
    fireEvent.submit(screen.getByRole('form', { name: 'New workstream' }));

    await screen.findByRole('alert');
    expect(document.activeElement).toBe(name());
  });
});

describe('NewWorkstreamForm with a git-backed schema', () => {
  const gitProviders: Provider[] = [
    {
      ...providers[0],
      workstreamSchema: {
        type: 'object',
        properties: {
          branch: {
            type: 'string',
            title: 'Branch',
            description: 'Defaults to a branch named after the workstream.',
            examples: ['main', 'feature'],
          },
          baseRef: {
            type: 'string',
            title: 'Base ref',
            default: 'main',
            examples: ['main', 'feature', 'v1.0'],
          },
        },
      },
    },
  ];
  const renderForm = (
    props: Partial<Parameters<typeof NewWorkstreamForm>[0]> = {}
  ) =>
    render(
      <NewWorkstreamForm
        providers={gitProviders}
        onSubmit={vi.fn().mockResolvedValue(null)}
        onCancel={vi.fn()}
        {...props}
      />
    );
  const name = () => screen.getByLabelText('Name');
  const branch = () => screen.getByRole('combobox', { name: 'Branch' });
  const baseRef = () => screen.getByRole('combobox', { name: 'Base ref' });

  it('prefills the base ref with its default', () => {
    renderForm();
    expect(baseRef()).toHaveProperty('value', 'main');
  });

  // The daemon names an empty branch after the workstream's final name.
  it('leaves the branch to the daemon, and says so', () => {
    renderForm();
    fireEvent.change(name(), { target: { value: 'Fix login bug' } });
    expect(branch()).toHaveProperty('value', '');
    expect(
      screen.getByText('Defaults to a branch named after the workstream.')
    ).toBeTruthy();
  });

  it('submits no branch, and the default base', async () => {
    const onSubmit = vi.fn().mockResolvedValue(null);
    renderForm({ onSubmit });
    fireEvent.change(name(), { target: { value: 'Fix login bug' } });
    fireEvent.click(screen.getByRole('button', { name: 'Create' }));
    await waitFor(() =>
      expect(onSubmit).toHaveBeenCalledWith({
        providerId: 'fake',
        name: 'Fix login bug',
        config: { baseRef: 'main' },
      })
    );
  });

  describe('with the defaults the daemon derives', () => {
    const draftProviders: Provider[] = [
      {
        ...providers[0],
        workstreamSchema: {
          type: 'object',
          properties: {
            branch: { type: 'string', title: 'Branch' },
            path: { type: 'string', title: 'Destination path' },
          },
        },
      },
    ];
    // As the core derives them: a name, and a branch and a path from it.
    const derive = async ({ name, config }: NewWorkstreamData) => {
      const shown = name || 'Proj';
      const slug = shown.toLowerCase().replace(/ /g, '-');
      const branch = (config.branch as string | undefined) ?? slug;
      return { name: shown, config: { branch, path: `/ws/${slug}` } };
    };
    const renderForm = (
      onSubmit = vi.fn().mockResolvedValue(null),
      resolveDraft: NewWorkstreamFormProps['resolveDraft'] = vi.fn(derive),
      props: Partial<NewWorkstreamFormProps> = {}
    ) => {
      render(
        <NewWorkstreamForm
          providers={draftProviders}
          onSubmit={onSubmit}
          onCancel={vi.fn()}
          resolveDraft={resolveDraft}
          {...props}
        />
      );
      return {
        name: screen.getByRole('textbox', { name: 'Name' }) as HTMLInputElement,
        branch: screen.getByLabelText('Branch') as HTMLInputElement,
        path: screen.getByLabelText('Destination path') as HTMLInputElement,
      };
    };

    it('fills them in, the name selected so typing replaces it', async () => {
      const { name, branch, path } = renderForm();
      await waitFor(() => expect(name.value).toBe('Proj'));
      expect(document.activeElement).toBe(name);
      expect([name.selectionStart, name.selectionEnd]).toEqual([0, 4]);
      expect([branch.value, path.value]).toEqual(['proj', '/ws/proj']);
    });

    it('keeps each following the rest until it is edited', async () => {
      const resolveDraft = vi.fn(derive);
      const { name, branch, path } = renderForm(undefined, resolveDraft);
      fireEvent.change(name, { target: { value: 'Fix Login' } });
      await waitFor(() => expect(path.value).toBe('/ws/fix-login'));
      expect(branch.value).toBe('fix-login');

      fireEvent.change(branch, { target: { value: 'mine' } });
      fireEvent.change(name, { target: { value: 'Other' } });
      await waitFor(() => expect(path.value).toBe('/ws/other'));
      expect(branch.value).toBe('mine');

      fireEvent.change(path, { target: { value: '/elsewhere' } });
      fireEvent.change(name, { target: { value: 'Third' } });
      await waitFor(() =>
        expect(resolveDraft).toHaveBeenLastCalledWith(
          expect.objectContaining({ name: 'Third' })
        )
      );
      await act(async () => undefined);
      expect([branch.value, path.value]).toEqual(['mine', '/elsewhere']);
    });

    it('shows the derived name again once the typed one is cleared', async () => {
      const { name, branch, path } = renderForm();
      fireEvent.change(name, { target: { value: 'Fix' } });
      await waitFor(() => expect(path.value).toBe('/ws/fix'));

      fireEvent.change(name, { target: { value: '' } });
      await waitFor(() => expect(name.value).toBe('Proj'));
      expect([branch.value, path.value]).toEqual(['proj', '/ws/proj']);
      expect([name.selectionStart, name.selectionEnd]).toEqual([0, 4]);

      fireEvent.change(name, { target: { value: 'N' } });
      expect(name.value).toBe('N');
    });

    it('shows a derived branch and path again once the typed ones are cleared', async () => {
      const onSubmit = vi.fn().mockResolvedValue(null);
      const { name, branch, path } = renderForm(onSubmit);
      fireEvent.change(name, { target: { value: 'Fix' } });
      fireEvent.change(branch, { target: { value: 'mine' } });
      fireEvent.change(path, { target: { value: '/elsewhere' } });
      await act(async () => new Promise((resolve) => setTimeout(resolve, 200)));

      fireEvent.change(branch, { target: { value: '' } });
      fireEvent.change(path, { target: { value: '' } });
      await waitFor(() => expect([branch.value, path.value]).toEqual(['fix', '/ws/fix']));

      fireEvent.click(screen.getByRole('button', { name: 'Create' }));
      await waitFor(() =>
        expect(onSubmit).toHaveBeenCalledWith({ providerId: 'fake', name: 'Fix', config: {} })
      );
    });

    it('selects a derived branch arriving in the focused box, so typing replaces it', async () => {
      const { name, branch } = renderForm();
      fireEvent.change(name, { target: { value: 'Fix' } });
      act(() => branch.focus());
      fireEvent.change(branch, { target: { value: 'mine' } });
      await act(async () => new Promise((resolve) => setTimeout(resolve, 200)));

      fireEvent.change(branch, { target: { value: '' } });
      await waitFor(() => expect(branch.value).toBe('fix'));
      expect(document.activeElement).toBe(branch);
      expect([branch.selectionStart, branch.selectionEnd]).toEqual([0, 3]);
    });

    it('shows a space typed over the derived name', async () => {
      const { name } = renderForm();
      await waitFor(() => expect(name.value).toBe('Proj'));

      fireEvent.change(name, { target: { value: ' ' } });
      expect(name.value).toBe(' ');
      await act(async () => new Promise((resolve) => setTimeout(resolve, 200)));
      expect(name.value).toBe(' ');

      fireEvent.change(name, { target: { value: ' X' } });
      expect(name.value).toBe(' X');
    });

    it('keeps the draft shown when the selected provider is picked again', async () => {
      const resolveDraft = vi.fn(derive);
      const { name, branch, path } = renderForm(undefined, resolveDraft);
      await waitFor(() => expect(path.value).toBe('/ws/proj'));

      fireEvent.click(screen.getByRole('radio', { name: 'Fake Provider' }));
      expect([name.value, branch.value, path.value]).toEqual(['Proj', 'proj', '/ws/proj']);
      await new Promise((resolve) => setTimeout(resolve, 200));
      expect([name.value, branch.value, path.value]).toEqual(['Proj', 'proj', '/ws/proj']);
      expect(resolveDraft).toHaveBeenCalledTimes(1);
    });

    it('sends what it derived blank, so the create derives it afresh', async () => {
      const onSubmit = vi.fn().mockResolvedValue(null);
      const { name, branch } = renderForm(onSubmit);
      await waitFor(() => expect(name.value).toBe('Proj'));
      fireEvent.change(branch, { target: { value: 'mine' } });
      fireEvent.click(screen.getByRole('button', { name: 'Create' }));
      await waitFor(() =>
        expect(onSubmit).toHaveBeenCalledWith({
          providerId: 'fake',
          name: '',
          config: { branch: 'mine' },
        })
      );
    });

    // Each call answers only when the test says so, in any order.
    const heldDrafts = () => {
      const answers: Array<(draft: WorkstreamDraft | null) => void> = [];
      const resolveDraft = vi.fn(
        () => new Promise<WorkstreamDraft | null>((resolve) => answers.push(resolve))
      );
      const answer = async (call: number, draft: WorkstreamDraft | null) => {
        await waitFor(() => expect(answers.length).toBeGreaterThan(call));
        await act(async () => answers[call](draft));
      };
      return { resolveDraft, answer };
    };

    it('shows no answer to a request it no longer makes', async () => {
      const { resolveDraft, answer } = heldDrafts();
      const { name, path } = renderForm(undefined, resolveDraft);
      await waitFor(() => expect(resolveDraft).toHaveBeenCalledTimes(1));
      fireEvent.change(screen.getByLabelText('Branch'), { target: { value: 'b' } });

      await answer(1, { name: 'Proj', config: { branch: 'b', path: '/ws/b' } });
      expect(path.value).toBe('/ws/b');
      await answer(0, { name: 'Old', config: { branch: 'old', path: '/ws/old' } });
      expect([name.value, path.value]).toEqual(['Proj', '/ws/b']);
    });

    it('keeps the last answer shown until the next replaces it', async () => {
      const { resolveDraft, answer } = heldDrafts();
      const { name, branch, path } = renderForm(undefined, resolveDraft);
      await answer(0, { name: 'Proj', config: { branch: 'proj', path: '/ws/proj' } });

      fireEvent.change(name, { target: { value: 'Fix' } });
      await waitFor(() => expect(resolveDraft).toHaveBeenCalledTimes(2));
      expect([branch.value, path.value]).toEqual(['proj', '/ws/proj']);

      await answer(1, { name: 'Fix', config: { branch: 'fix', path: '/ws/fix' } });
      expect([name.value, branch.value, path.value]).toEqual(['Fix', 'fix', '/ws/fix']);
    });

    it('shows no draft once the next one fails', async () => {
      const { resolveDraft, answer } = heldDrafts();
      const { name, branch, path } = renderForm(undefined, resolveDraft);
      await answer(0, { name: 'Proj', config: { branch: 'proj', path: '/ws/proj' } });
      expect(path.value).toBe('/ws/proj');

      fireEvent.change(branch, { target: { value: 'mine' } });
      await waitFor(() => expect(resolveDraft).toHaveBeenCalledTimes(2));
      expect(path.value).toBe('/ws/proj');
      await answer(1, null);
      expect([name.value, path.value]).toEqual(['', '']);
    });

    it('asks for no draft when no provider is offered', async () => {
      const resolveDraft = vi.fn(derive);
      render(
        <NewWorkstreamForm
          providers={[]}
          onSubmit={vi.fn()}
          onCancel={vi.fn()}
          resolveDraft={resolveDraft}
        />
      );
      await new Promise((resolve) => setTimeout(resolve, 200));
      expect(resolveDraft).not.toHaveBeenCalled();
    });

    it('on a provider switch, resets edited config but keeps an edited name', async () => {
      const resolveDraft = vi.fn(derive);
      render(
        <NewWorkstreamForm
          providers={[...draftProviders, { ...draftProviders[0], id: 'other', label: 'Other' }]}
          onSubmit={vi.fn()}
          onCancel={vi.fn()}
          resolveDraft={resolveDraft}
        />
      );
      const branch = screen.getByLabelText('Branch') as HTMLInputElement;
      fireEvent.change(screen.getByRole('textbox', { name: 'Name' }), {
        target: { value: 'Fix Login' },
      });
      fireEvent.change(branch, { target: { value: 'mine' } });
      const path = screen.getByLabelText('Destination path') as HTMLInputElement;
      await waitFor(() => expect(path.value).toBe('/ws/fix-login'));
      fireEvent.click(screen.getByRole('radio', { name: 'Other' }));
      expect(path.value).toBe('');

      await waitFor(() => expect(branch.value).toBe('fix-login'));
      expect(resolveDraft).toHaveBeenLastCalledWith({
        providerId: 'other',
        name: 'Fix Login',
        config: {},
      });
      expect(screen.getByRole('textbox', { name: 'Name' })).toHaveProperty(
        'value',
        'Fix Login'
      );
    });

    it('sends a default name as given, as though typed', async () => {
      const onSubmit = vi.fn().mockResolvedValue(null);
      const resolveDraft = vi.fn(async () => ({ name: 'Derived', config: {} }));
      const { name } = renderForm(onSubmit, resolveDraft, { defaultName: 'ws-1' });
      await waitFor(() =>
        expect(resolveDraft).toHaveBeenCalledWith(expect.objectContaining({ name: 'ws-1' }))
      );
      await act(async () => undefined);
      expect(name.value).toBe('ws-1');

      fireEvent.click(screen.getByRole('button', { name: 'Create' }));
      await waitFor(() =>
        expect(onSubmit).toHaveBeenCalledWith(expect.objectContaining({ name: 'ws-1' }))
      );
    });
  });
});
