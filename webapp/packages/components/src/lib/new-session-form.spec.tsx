import type { SessionProviderSchemas } from '@homeshell/model';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';

import type { FormHandle } from './create-form';
import { NewSessionForm } from './new-session-form';

const localProvider: SessionProviderSchemas = {
  providerId: 'local',
  label: 'Local System',
  options: [
    {
      id: 'claude',
      label: 'Claude Code',
      schema: {
        properties: {
          model: { type: 'string', enum: ['sonnet', 'opus'], default: 'sonnet' },
        },
      },
    },
  ],
};

// Mirrors what the Enclave provider serves: a tool whose model HomeShell can set
// carries an *optional* model enum with no default (unset means "leave the
// container's own choice alone"), and a tool with no knobs carries no schema.
const enclaveProvider: SessionProviderSchemas = {
  providerId: 'enclave',
  label: 'Eclipse Enclave',
  options: [
    {
      id: 'claude',
      label: 'claude',
      schema: {
        properties: {
          model: {
            type: 'string',
            title: 'Model',
            enum: ['claude-opus-5', 'claude-sonnet-5'],
          },
        },
      },
    },
    {
      id: 'opencode',
      label: 'opencode',
      schema: {
        properties: {
          model: {
            type: 'string',
            title: 'Model',
            description: 'Model as provider/model.',
          },
        },
      },
    },
    { id: 'theia', label: 'theia' },
  ],
};

const multiOptionLocal: SessionProviderSchemas = {
  providerId: 'local',
  label: 'Local System',
  options: [
    {
      id: 'claude',
      label: 'Claude Code',
      schema: {
        properties: {
          model: { type: 'string', enum: ['sonnet', 'opus'], default: 'sonnet' },
        },
      },
    },
    {
      id: 'codex',
      label: 'Codex',
      schema: {
        properties: {
          model: { type: 'string', enum: ['o3'], default: 'o3' },
        },
      },
    },
  ],
};

// The daemon groups session options by provider (SessionProviderSchemas). This
// form renders a Provider selector when more than one provider is available and
// an Agent selector when the selected provider offers more than one option;
// both render as pill radiogroups (PillSelect) for a small set, matching the
// new-workstream form. Config fields render via SchemaField (a small enum with
// a default is also a pill radiogroup).
describe('NewSessionForm', () => {
  it('single provider, single option: no provider or agent selector, schema fields render', () => {
    render(
      <NewSessionForm
        providers={[localProvider]}
        onSubmit={vi.fn().mockResolvedValue(null)}
        onCancel={vi.fn()}
      />
    );
    expect(screen.queryByRole('radiogroup', { name: 'Provider' })).toBeNull();
    expect(screen.queryByRole('radiogroup', { name: 'Agent' })).toBeNull();
    // The single option's model enum renders as pill radio buttons.
    expect(screen.getByRole('radiogroup', { name: 'model' })).toBeTruthy();
  });

  it('gives its buttons the letters Alt presses them by', () => {
    render(
      <NewSessionForm providers={[localProvider]} onSubmit={vi.fn()} onCancel={vi.fn()} />
    );
    const letter = (name: string) =>
      screen.getByRole('button', { name }).getAttribute('data-mnemonic');
    expect([letter('Cancel'), letter('Start session')]).toEqual(['c', 's']);
  });

  it('single provider, multiple options: no provider selector, agent pills show', () => {
    render(
      <NewSessionForm
        providers={[multiOptionLocal]}
        onSubmit={vi.fn().mockResolvedValue(null)}
        onCancel={vi.fn()}
      />
    );
    expect(screen.queryByRole('radiogroup', { name: 'Provider' })).toBeNull();
    expect(screen.getByRole('radiogroup', { name: 'Agent' })).toBeTruthy();
  });

  it('switching option reseeds the fields from the new schema', () => {
    render(
      <NewSessionForm
        providers={[multiOptionLocal]}
        onSubmit={vi.fn().mockResolvedValue(null)}
        onCancel={vi.fn()}
      />
    );
    fireEvent.click(screen.getByRole('radio', { name: 'Codex' }));
    // Codex's model enum default (o3) should be the checked pill.
    const o3 = screen.getByRole('radio', { name: 'o3' });
    expect(o3.getAttribute('aria-checked')).toBe('true');
  });

  it('multiple providers: provider pills show, switching resets option and fields', () => {
    render(
      <NewSessionForm
        providers={[localProvider, enclaveProvider]}
        onSubmit={vi.fn().mockResolvedValue(null)}
        onCancel={vi.fn()}
      />
    );
    expect(screen.getByRole('radiogroup', { name: 'Provider' })).toBeTruthy();
    // Local has a schema field (model as pills).
    expect(screen.getByRole('radiogroup', { name: 'model' })).toBeTruthy();
    // Switch to enclave: two options, and its own model field in place of local's
    // pills — an optional enum, so a select rather than a radiogroup.
    fireEvent.click(screen.getByRole('radio', { name: 'Eclipse Enclave' }));
    expect(screen.queryByRole('radiogroup', { name: 'model' })).toBeNull();
    expect(screen.getByRole('radiogroup', { name: 'Agent' })).toBeTruthy();
    expect(screen.getByRole('combobox', { name: 'Model' })).toBeTruthy();
  });

  // The Enclave shape: a model may be chosen, and choosing nothing has to stay a
  // real answer — the field starts on "Default" and is left out of the config, so
  // the container keeps whatever model it was built with.
  it('an optional model enum starts at Default and is omitted from the config', async () => {
    const onSubmit = vi.fn().mockResolvedValue(null);
    render(
      <NewSessionForm
        providers={[enclaveProvider]}
        onSubmit={onSubmit}
        onCancel={vi.fn()}
      />
    );
    const model = screen.getByRole('combobox', {
      name: 'Model',
    }) as HTMLSelectElement;
    expect(model.value).toBe('');
    expect(
      Array.from(model.options).map((o) => o.textContent)
    ).toEqual(['Default', 'claude-opus-5', 'claude-sonnet-5']);

    fireEvent.click(screen.getByRole('button', { name: 'Start session' }));
    await waitFor(() =>
      expect(onSubmit).toHaveBeenCalledWith({
        providerId: 'enclave',
        optionId: 'claude',
        config: {},
      })
    );
  });

  it('submits the model that was picked for a container session', async () => {
    const onSubmit = vi.fn().mockResolvedValue(null);
    render(
      <NewSessionForm
        providers={[enclaveProvider]}
        onSubmit={onSubmit}
        onCancel={vi.fn()}
      />
    );
    fireEvent.change(screen.getByRole('combobox', { name: 'Model' }), {
      target: { value: 'claude-opus-5' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Start session' }));
    await waitFor(() =>
      expect(onSubmit).toHaveBeenCalledWith({
        providerId: 'enclave',
        optionId: 'claude',
        config: { model: 'claude-opus-5' },
      })
    );
  });

  // Switching to a tool with no knobs must clear the previous tool's field, or a
  // model would be sent for a session that has no model to set.
  it('switching to a tool without knobs drops the model field', () => {
    render(
      <NewSessionForm
        providers={[enclaveProvider]}
        onSubmit={vi.fn().mockResolvedValue(null)}
        onCancel={vi.fn()}
      />
    );
    expect(screen.getByRole('combobox', { name: 'Model' })).toBeTruthy();
    fireEvent.click(screen.getByRole('radio', { name: 'theia' }));
    expect(screen.queryByRole('combobox', { name: 'Model' })).toBeNull();
    expect(screen.queryByRole('textbox', { name: 'Model' })).toBeNull();
  });

  // The third shape a model knob comes in: an agent whose accepted values live in
  // a catalog it downloads, so there is nothing to enumerate and the field is free
  // text. Left empty it must be omitted, exactly like the unset enum.
  it('a free-text model field is omitted when empty and sent when filled', async () => {
    const onSubmit = vi.fn().mockResolvedValue(null);
    render(
      <NewSessionForm
        providers={[enclaveProvider]}
        onSubmit={onSubmit}
        onCancel={vi.fn()}
      />
    );
    fireEvent.click(screen.getByRole('radio', { name: 'opencode' }));
    const model = screen.getByRole('textbox', {
      name: 'Model',
    }) as HTMLInputElement;
    expect(model.value).toBe('');

    fireEvent.click(screen.getByRole('button', { name: 'Start session' }));
    await waitFor(() =>
      expect(onSubmit).toHaveBeenCalledWith({
        providerId: 'enclave',
        optionId: 'opencode',
        config: {},
      })
    );

    fireEvent.change(model, { target: { value: 'anthropic/claude-sonnet-5' } });
    fireEvent.click(screen.getByRole('button', { name: 'Start session' }));
    await waitFor(() =>
      expect(onSubmit).toHaveBeenLastCalledWith({
        providerId: 'enclave',
        optionId: 'opencode',
        config: { model: 'anthropic/claude-sonnet-5' },
      })
    );
  });

  it('submits { providerId, optionId, config }', async () => {
    const onSubmit = vi.fn().mockResolvedValue(null);
    render(
      <NewSessionForm
        providers={[localProvider]}
        onSubmit={onSubmit}
        onCancel={vi.fn()}
      />
    );
    fireEvent.click(screen.getByRole('radio', { name: 'opus' }));
    fireEvent.click(screen.getByRole('button', { name: 'Start session' }));
    await waitFor(() =>
      expect(onSubmit).toHaveBeenCalledWith({
        providerId: 'local',
        optionId: 'claude',
        config: { model: 'opus' },
      })
    );
  });

  it('shows error from onSubmit', async () => {
    const onSubmit = vi.fn().mockResolvedValue('Concurrent session limit reached');
    render(
      <NewSessionForm
        providers={[localProvider]}
        onSubmit={onSubmit}
        onCancel={vi.fn()}
      />
    );
    fireEvent.click(screen.getByRole('button', { name: 'Start session' }));
    expect((await screen.findByRole('alert')).textContent).toContain(
      'Concurrent session limit reached'
    );
  });

  it('empty providers shows empty message', () => {
    render(
      <NewSessionForm
        providers={[]}
        onSubmit={vi.fn().mockResolvedValue(null)}
        onCancel={vi.fn()}
      />
    );
    expect(
      screen.getByText('No agent is available to start a session here.')
    ).toBeTruthy();
  });

  // Offline: what is filled in here exists to be sent, so the fields and Start go
  // inert while the daemon is gone. Cancel stays live and the draft is kept.
  describe('when the daemon is unreachable', () => {
    const REASON = 'The daemon is offline';

    it('makes every field and the submit inert, with the reason on it', () => {
      render(
        <NewSessionForm
          providers={[localProvider]}
          onSubmit={vi.fn().mockResolvedValue(null)}
          onCancel={vi.fn()}
          disabledReason={REASON}
        />,
      );
      const submit = screen.getByRole('button', { name: 'Start session' });
      expect(submit.getAttribute('aria-disabled')).toBe('true');
      expect(submit.title).toBe(REASON);
      // The schema-driven field goes with it: a value chosen here has nowhere to go.
      for (const pill of screen.getAllByRole('radio')) {
        expect(pill.getAttribute('aria-disabled')).toBe('true');
      }
      fireEvent.click(screen.getByRole('radio', { name: 'opus' }));
      expect(
        screen.getByRole('radio', { name: 'opus' }).getAttribute('aria-checked')
      ).toBe('false');
    });

    it('leaves Cancel usable', () => {
      const onCancel = vi.fn();
      render(
        <NewSessionForm
          providers={[localProvider]}
          onSubmit={vi.fn().mockResolvedValue(null)}
          onCancel={onCancel}
          disabledReason={REASON}
        />,
      );
      fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
      expect(onCancel).toHaveBeenCalled();
    });
  });
});

describe('NewSessionForm when the daemon is unreachable', () => {
  const REASON = 'The daemon is offline';

  it('makes Start session inert but keeps Cancel live', () => {
    const onCancel = vi.fn();
    render(
      <NewSessionForm
        providers={[localProvider]}
        onSubmit={vi.fn()}
        onCancel={onCancel}
        disabledReason={REASON}
      />
    );
    const submit = screen.getByRole('button', { name: 'Start session' });
    expect(submit.getAttribute('aria-disabled')).toBe('true');
    expect(submit.getAttribute('title')).toBe(REASON);
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(onCancel).toHaveBeenCalled();
  });

  // The schema fields are what would be sent, so they are inert with the submit.
  it('makes the schema fields inert as well', () => {
    render(
      <NewSessionForm
        providers={[localProvider]}
        onSubmit={vi.fn()}
        onCancel={vi.fn()}
        disabledReason={REASON}
      />
    );
    const pills = screen.getAllByRole('radio');
    expect(pills.length).toBeGreaterThan(0);
    for (const pill of pills) {
      expect(pill.getAttribute('aria-disabled')).toBe('true');
    }
  });
});

describe('NewSessionForm by keyboard', () => {
  const renderForm = (
    props: Partial<Parameters<typeof NewSessionForm>[0]> = {}
  ) =>
    render(
      <NewSessionForm
        providers={[localProvider]}
        onSubmit={vi.fn().mockResolvedValue(null)}
        onCancel={vi.fn()}
        {...props}
      />
    );

  it('opens on the checked pill of its first group', () => {
    renderForm({ providers: [localProvider, enclaveProvider] });
    expect(document.activeElement).toBe(
      screen.getByRole('radio', { name: 'Local System' })
    );
  });

  it('opens on Start session when it has no fields', () => {
    renderForm({
      providers: [{ ...localProvider, options: [{ id: 'claude', label: 'Claude' }] }],
    });
    expect(document.activeElement).toBe(
      screen.getByRole('button', { name: 'Start session' })
    );
  });

  it('focuses its first field again through its ref', () => {
    const ref = { current: null as FormHandle | null };
    renderForm({ ref });
    screen.getByRole('button', { name: 'Cancel' }).focus();
    ref.current?.focus();
    expect(document.activeElement).toBe(
      screen.getByRole('radio', { name: 'sonnet' })
    );
  });

  it('cancels on Escape', () => {
    const onCancel = vi.fn();
    renderForm({ onCancel });
    fireEvent.keyDown(screen.getByRole('radio', { name: 'sonnet' }), {
      key: 'Escape',
    });
    expect(onCancel).toHaveBeenCalled();
  });

  it('focuses the field a refusal blames', async () => {
    renderForm({
      providers: [multiOptionLocal],
      onSubmit: vi
        .fn()
        .mockResolvedValue({
          message: 'Model: not offered',
          fields: { '/model': 'not offered' },
        }),
    });
    expect(document.activeElement).toBe(
      screen.getByRole('radio', { name: 'Claude Code' })
    );
    fireEvent.submit(screen.getByRole('form', { name: 'New session' }));

    const model = await screen.findByRole('radiogroup', { description: 'Not offered' });
    expect(model.getAttribute('aria-invalid')).toBe('true');
    expect(document.activeElement).toBe(
      screen.getByRole('radio', { name: 'sonnet' })
    );
  });

  // Choosing another value answers the reason, so it goes until the next try.
  it("drops a field's reason once it is changed", async () => {
    renderForm({
      providers: [multiOptionLocal],
      onSubmit: vi.fn().mockResolvedValue({
        message: 'Model: not offered',
        fields: { '/model': 'not offered' },
      }),
    });
    fireEvent.submit(screen.getByRole('form', { name: 'New session' }));
    const model = await screen.findByRole('radiogroup', {
      description: 'Not offered',
    });

    const others = screen
      .getAllByRole('radio')
      .filter(
        (radio) =>
          model.contains(radio) && !(radio as HTMLInputElement).checked,
      );
    fireEvent.click(others[0]);
    expect(model.hasAttribute('aria-invalid')).toBe(false);
  });

  it('keeps Start session focused while the request is out', () => {
    renderForm({ onSubmit: vi.fn(() => new Promise<null>(() => undefined)) });
    const start = screen.getByRole('button', { name: 'Start session' });
    start.focus();
    fireEvent.click(start);
    expect(start.getAttribute('aria-disabled')).toBe('true');
    expect(document.activeElement).toBe(start);
  });
});

// The shape the Enclave provider serves: a flat `sandbox` enum of the agent's
// own beside the `enclave` group of container settings.
const containerProvider: SessionProviderSchemas = {
  providerId: 'enclave',
  label: 'Eclipse Enclave',
  options: [
    {
      id: 'codex',
      label: 'codex',
      schema: {
        type: 'object',
        properties: {
          sandbox: {
            type: 'string',
            title: 'Sandbox',
            enum: ['read-only', 'workspace-write'],
            default: 'workspace-write',
          },
          enclave: {
            type: 'object',
            title: 'Container',
            properties: {
              allowDomains: {
                type: 'array',
                title: 'Extra allowed domains',
                items: { type: 'string' },
                default: [],
              },
              bridgePorts: {
                type: 'array',
                title: 'Reach host services',
                items: { type: 'integer' },
                default: [],
              },
              hostConfig: {
                type: 'boolean',
                title: 'Pass through my host agent config',
                default: false,
              },
            },
          },
        },
      },
    },
  ],
};

describe('NewSessionForm with a group', () => {
  it('collapses the group to a summary, and sends it whole', async () => {
    const onSubmit = vi.fn().mockResolvedValue(null);
    render(
      <NewSessionForm
        providers={[containerProvider]}
        onSubmit={onSubmit}
        onCancel={vi.fn()}
      />
    );
    const header = screen.getByRole('button', { name: /^Container / });
    expect(header.getAttribute('aria-expanded')).toBe('false');
    expect(screen.getByTestId('group-summary').textContent).toBe('Defaults');

    fireEvent.click(header);
    const domains = screen.getByRole('textbox', {
      name: 'Add to Extra allowed domains',
    });
    fireEvent.change(domains, { target: { value: 'api.example.com' } });
    fireEvent.keyDown(domains, { key: 'Enter' });
    expect(screen.getByTestId('group-summary').textContent).toBe(
      'Extra allowed domains +1'
    );

    fireEvent.submit(screen.getByRole('form', { name: 'New session' }));
    await waitFor(() =>
      expect(onSubmit).toHaveBeenCalledWith({
        providerId: 'enclave',
        optionId: 'codex',
        config: {
          sandbox: 'workspace-write',
          enclave: {
            allowDomains: ['api.example.com'],
            bridgePorts: [],
            hostConfig: false,
          },
        },
      })
    );
  });

  it('opens on Start session when its only field is a collapsed group', () => {
    const group = containerProvider.options[0].schema as {
      properties: Record<string, unknown>;
    };
    render(
      <NewSessionForm
        providers={[
          {
            ...containerProvider,
            options: [
              {
                id: 'claude',
                label: 'claude',
                schema: { properties: { enclave: group.properties.enclave } },
              },
            ],
          },
        ]}
        onSubmit={vi.fn()}
        onCancel={vi.fn()}
      />
    );
    expect(document.activeElement).toBe(
      screen.getByRole('button', { name: 'Start session' })
    );
  });

  it('starts another option’s group afresh, even under the same key', () => {
    const [codex] = containerProvider.options;
    render(
      <NewSessionForm
        providers={[
          {
            ...containerProvider,
            options: [codex, { ...codex, id: 'claude', label: 'claude' }],
          },
        ]}
        onSubmit={vi.fn()}
        onCancel={vi.fn()}
      />
    );
    fireEvent.click(screen.getByRole('button', { name: /^Container / }));
    expect(
      screen.getByRole('button', { name: /^Container / }).getAttribute('aria-expanded')
    ).toBe('true');

    fireEvent.click(screen.getByRole('radio', { name: 'claude' }));
    expect(
      screen.getByRole('button', { name: /^Container / }).getAttribute('aria-expanded')
    ).toBe('false');
  });

  it('keeps what is typed in a list but not added, rather than sending without it', async () => {
    const onSubmit = vi.fn().mockResolvedValue(null);
    render(
      <NewSessionForm
        providers={[containerProvider]}
        onSubmit={onSubmit}
        onCancel={vi.fn()}
      />
    );
    const header = screen.getByRole('button', { name: /^Container / });
    fireEvent.click(header);
    const ports = screen.getByRole('textbox', {
      name: 'Add to Reach host services',
    });
    fireEvent.change(ports, { target: { value: '5432x' } });
    fireEvent.click(header);

    const form = screen.getByRole('form', { name: 'New session' });
    fireEvent.submit(form);
    expect(onSubmit).not.toHaveBeenCalled();
    expect(header.getAttribute('aria-expanded')).toBe('true');
    expect(document.activeElement).toBe(ports);
    expect(screen.getByText('Enter a whole number.')).toBeTruthy();
    expect(screen.getByText(/^Not added/)).toBeTruthy();

    // Corrected, the submit adds it, and it is sent.
    fireEvent.change(ports, { target: { value: '5432' } });
    fireEvent.submit(form);
    await waitFor(() =>
      expect(onSubmit).toHaveBeenCalledWith({
        providerId: 'enclave',
        optionId: 'codex',
        config: {
          sandbox: 'workspace-write',
          enclave: { allowDomains: [], bridgePorts: [5432], hostConfig: false },
        },
      })
    );
  });

  it('opens the group on a refusal inside it, focusing and explaining the field', async () => {
    const onSubmit = vi.fn().mockResolvedValue({
      message: 'Reach host services: must be at most 65535',
      fields: { '/enclave/bridgePorts/0': 'must be at most 65535' },
    });
    render(
      <NewSessionForm
        providers={[containerProvider]}
        onSubmit={onSubmit}
        onCancel={vi.fn()}
      />
    );
    fireEvent.submit(screen.getByRole('form', { name: 'New session' }));
    await waitFor(() =>
      expect(
        screen
          .getByRole('button', { name: /^Container / })
          .getAttribute('aria-expanded')
      ).toBe('true')
    );
    expect(screen.getByText('Must be at most 65535')).toBeTruthy();
    expect(document.activeElement).toBe(
      screen.getByRole('textbox', { name: 'Add to Reach host services' })
    );
    expect(screen.queryByRole('alert')).toBeNull();
  });
});
