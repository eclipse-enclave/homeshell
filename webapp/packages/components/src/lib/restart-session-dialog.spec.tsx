import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from '@testing-library/react';

import { RestartSessionDialog } from './restart-session-dialog';

const schema = {
  type: 'object',
  properties: {
    model: { type: 'string', enum: ['opus', 'sonnet'], default: 'sonnet' },
    container: {
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
      },
    },
  },
};

const launched = {
  model: 'opus',
  container: { allowDomains: ['api.example.com'] },
};

function renderDialog(
  props: Partial<Parameters<typeof RestartSessionDialog>[0]> = {},
) {
  const onSubmit = vi.fn().mockResolvedValue(null);
  const onCancel = vi.fn();
  render(
    <RestartSessionDialog
      sessionTitle="Fix the build"
      schema={schema}
      config={launched}
      onSubmit={onSubmit}
      onCancel={onCancel}
      {...props}
    />,
  );
  return { onSubmit, onCancel };
}

describe('RestartSessionDialog', () => {
  it('opens on the launch config, with its groups open and its first field focused', () => {
    renderDialog();
    expect(screen.getByRole('dialog', { name: 'Restart Fix the build' })).toBeTruthy();
    expect(
      screen.getByRole('radio', { name: 'opus' }).getAttribute('aria-checked'),
    ).toBe('true');
    expect(document.activeElement).toBe(screen.getByRole('radio', { name: 'opus' }));
    expect(
      screen.getByRole('button', { name: /^Container / }).getAttribute('aria-expanded'),
    ).toBe('true');
    expect(screen.getByText('api.example.com')).toBeTruthy();
    const fresh = screen.getByRole('checkbox', {
      name: 'Start a fresh conversation',
    });
    expect(fresh).toHaveProperty('checked', false);
    expect(
      document.getElementById(fresh.getAttribute('aria-describedby') ?? '')
        ?.textContent,
    ).toBe(
      "Otherwise the agent continues its latest conversation in this session's directory",
    );
  });

  it('only starts fresh when the conversation cannot be continued, and says why', async () => {
    const { onSubmit } = renderDialog({
      continueRefusal:
        "cannot continue the session's conversation: another claude session works in /work",
    });
    const fresh = screen.getByRole('checkbox', {
      name: 'Start a fresh conversation',
    });
    expect(fresh).toHaveProperty('checked', true);
    expect(fresh.getAttribute('aria-disabled')).toBe('true');
    expect(
      document.getElementById(fresh.getAttribute('aria-describedby') ?? '')
        ?.textContent,
    ).toBe(
      "Cannot continue the session's conversation: another claude session works in /work",
    );
    fireEvent.click(fresh);
    expect(fresh).toHaveProperty('checked', true);
    fireEvent.click(screen.getByRole('button', { name: 'Restart' }));
    await waitFor(() =>
      expect(onSubmit).toHaveBeenCalledWith(
        expect.objectContaining({ fresh: true }),
      ),
    );
  });

  it('shows a launched value now inherited once, locked, and does not send it', async () => {
    const container = schema.properties.container;
    const { onSubmit } = renderDialog({
      schema: {
        ...schema,
        properties: {
          ...schema.properties,
          container: {
            ...container,
            properties: {
              ...container.properties,
              allowDomains: {
                ...container.properties.allowDomains,
                'x-inherited': {
                  values: ['api.example.com'],
                  source: 'project config',
                },
              },
            },
          },
        },
      },
    });
    expect(screen.getAllByText('api.example.com')).toHaveLength(1);
    expect(
      screen.queryByRole('button', { name: 'Delete api.example.com' }),
    ).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'Restart' }));
    await waitFor(() =>
      expect(onSubmit).toHaveBeenCalledWith({
        config: {
          model: 'opus',
          container: { allowDomains: [], bridgePorts: [] },
        },
        fresh: false,
      }),
    );
  });

  it('restarts with the config, its groups as changed, and the fresh choice', async () => {
    const { onSubmit } = renderDialog();
    fireEvent.click(screen.getByRole('checkbox', { name: 'Start a fresh conversation' }));
    fireEvent.click(screen.getByRole('button', { name: 'Restart' }));
    await waitFor(() =>
      expect(onSubmit).toHaveBeenCalledWith({
        config: {
          model: 'opus',
          container: { allowDomains: ['api.example.com'], bridgePorts: [] },
        },
        fresh: true,
      }),
    );
  });

  it('fills defaults when there is no launch config', async () => {
    const { onSubmit } = renderDialog({ config: undefined });
    fireEvent.click(screen.getByRole('button', { name: 'Restart' }));
    await waitFor(() =>
      expect(onSubmit).toHaveBeenCalledWith({
        config: {
          model: 'sonnet',
          container: { allowDomains: [], bridgePorts: [] },
        },
        fresh: false,
      }),
    );
  });

  it('restarts on the recorded settings when the schema has no fields', async () => {
    const { onSubmit } = renderDialog({ schema: {}, config: undefined });
    fireEvent.click(screen.getByRole('button', { name: 'Restart' }));
    await waitFor(() => expect(onSubmit).toHaveBeenCalledWith({ fresh: false }));
  });

  it('opens on the first field inside its open group when that is all it has', () => {
    renderDialog({
      schema: { properties: { container: schema.properties.container } },
      config: undefined,
    });
    expect(document.activeElement).toBe(
      screen.getByRole('textbox', { name: 'Add to Extra allowed domains' }),
    );
  });

  it('shows a refusal under the field it blames', async () => {
    renderDialog({
      onSubmit: vi.fn().mockResolvedValue({
        message: 'Reach host services: must be at most 65535',
        fields: { '/container/bridgePorts/0': 'must be at most 65535' },
      }),
    });
    fireEvent.click(screen.getByRole('button', { name: 'Restart' }));
    expect(await screen.findByText('Must be at most 65535')).toBeTruthy();
    expect(document.activeElement).toBe(
      screen.getByRole('textbox', { name: 'Add to Reach host services' }),
    );
  });

  it('adds what is typed in its lists on Restart, refusing what it cannot add', async () => {
    const { onSubmit } = renderDialog();
    const ports = screen.getByRole('textbox', {
      name: 'Add to Reach host services',
    });
    fireEvent.change(ports, { target: { value: '5432' } });
    const domains = screen.getByRole('textbox', {
      name: 'Add to Extra allowed domains',
    });
    fireEvent.change(domains, { target: { value: 'api.example.com' } });
    fireEvent.click(screen.getByRole('button', { name: 'Restart' }));
    expect(onSubmit).not.toHaveBeenCalled();
    expect(screen.getByText('api.example.com is already listed.')).toBeTruthy();
    expect(document.activeElement).toBe(domains);

    fireEvent.change(domains, { target: { value: '' } });
    fireEvent.click(screen.getByRole('button', { name: 'Restart' }));
    await waitFor(() =>
      expect(onSubmit).toHaveBeenCalledWith({
        config: {
          model: 'opus',
          container: { allowDomains: ['api.example.com'], bridgePorts: [5432] },
        },
        fresh: false,
      }),
    );
  });

  it('drops what a refusal says of a field once it is edited, and only that', async () => {
    renderDialog({
      onSubmit: vi.fn().mockResolvedValue({
        message: 'refused',
        fields: {
          '/model': 'is not offered',
          '/container/bridgePorts/0': 'must be at most 65535',
        },
      }),
    });
    fireEvent.click(screen.getByRole('button', { name: 'Restart' }));
    expect(await screen.findByText('Is not offered')).toBeTruthy();
    expect(screen.getByText('Must be at most 65535')).toBeTruthy();

    fireEvent.click(screen.getByRole('radio', { name: 'sonnet' }));
    expect(screen.queryByText('Is not offered')).toBeNull();
    expect(screen.getByText('Must be at most 65535')).toBeTruthy();
  });

  it('presses Cancel and Restart by their letters', () => {
    renderDialog();
    expect(
      screen
        .getByRole('button', { name: 'Cancel' })
        .getAttribute('aria-keyshortcuts'),
    ).toBe('Alt+C');
    expect(
      screen
        .getByRole('button', { name: 'Restart' })
        .getAttribute('aria-keyshortcuts'),
    ).toBe('Alt+S');
  });

  it('shows a refusal that blames no field as its message', async () => {
    renderDialog({ onSubmit: vi.fn().mockResolvedValue('Cannot restart.') });
    fireEvent.click(screen.getByRole('button', { name: 'Restart' }));
    expect((await screen.findByRole('alert')).textContent).toBe('Cannot restart.');
  });

  it('cancels on Escape and on Cancel', () => {
    const { onCancel } = renderDialog();
    fireEvent.keyDown(screen.getByRole('radio', { name: 'opus' }), { key: 'Escape' });
    expect(onCancel).toHaveBeenCalledTimes(1);
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(onCancel).toHaveBeenCalledTimes(2);
  });

  it('keeps Restart inert while offline', () => {
    const { onSubmit } = renderDialog({ disabledReason: 'Offline' });
    const restart = screen.getByRole('button', { name: 'Restart' });
    expect(restart.getAttribute('aria-disabled')).toBe('true');
    fireEvent.click(restart);
    expect(onSubmit).not.toHaveBeenCalled();
  });

  it('says what a take-over costs only when the restart is one', () => {
    const warning = 'This ends it there and continues it here.';
    renderDialog({ takeOverWarning: warning });
    expect(screen.getByRole('dialog').textContent).toContain(warning);
    cleanup();
    renderDialog();
    expect(screen.getByRole('dialog').textContent).not.toContain(warning);
  });
});
