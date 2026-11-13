import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  within,
} from '@testing-library/react';
import { useState } from 'react';

import {
  type ConfigObject,
  type ConfigValue,
  type SchemaProp,
  SchemaField,
  defaultsOf,
  firstField,
  addDrafts,
  focusBlamedField,
  focusFirstField,
  refusedFields,
} from './schema-form';

describe('SchemaField', () => {
  it('moves an enum pill choice with the arrows', () => {
    const onChange = vi.fn();
    render(
      <SchemaField
        fieldKey="strategy"
        prop={{ type: 'string', enum: ['in-place', 'worktree'] }}
        required
        value="in-place"
        disabled={false}
        onChange={onChange}
      />,
    );
    const inPlace = screen.getByRole('radio', { name: 'in-place' });
    expect(inPlace.tabIndex).toBe(0);
    expect(screen.getByRole('radio', { name: 'worktree' }).tabIndex).toBe(-1);
    fireEvent.keyDown(inPlace, { key: 'ArrowRight' });
    expect(onChange).toHaveBeenCalledWith('worktree');
  });

  it('shows a few enum values as pills by their enumNames, choosing the raw value', () => {
    const onChange = vi.fn();
    const prop: SchemaProp = {
      type: 'string',
      enum: ['in-place', 'worktree'],
      enumNames: ['In place', 'Worktree'],
    };
    render(
      <SchemaField
        fieldKey="strategy"
        prop={prop}
        required
        value="in-place"
        disabled={false}
        onChange={onChange}
      />,
    );
    fireEvent.click(screen.getByRole('radio', { name: 'Worktree' }));
    expect(onChange).toHaveBeenCalledWith('worktree');
  });

  it('shows many enum values as a select by their enumNames, choosing the raw value', () => {
    const onChange = vi.fn();
    const many = ['a', 'b', 'c', 'd', 'e', 'f'];
    render(
      <SchemaField
        fieldKey="strategy"
        prop={{ type: 'string', enum: many, enumNames: many.map((v) => v.toUpperCase()) }}
        required
        value="a"
        disabled={false}
        onChange={onChange}
      />,
    );
    fireEvent.change(screen.getByRole('combobox'), { target: { value: 'c' } });
    expect(onChange).toHaveBeenLastCalledWith('c');
    expect(screen.getByRole('option', { name: 'C' })).toBeTruthy();
  });

  it('suggests a string field’s examples', () => {
    render(
      <SchemaField
        fieldKey="baseRef"
        prop={{ type: 'string', title: 'Base ref', examples: ['main', 'v1.0'] }}
        required={false}
        value="main"
        disabled={false}
        onChange={vi.fn()}
      />,
    );
    const input = screen.getByRole('combobox', { name: 'Base ref' });
    fireEvent.keyDown(input, { key: 'ArrowDown' });
    expect(
      screen.getAllByRole('option').map((option) => option.textContent),
    ).toEqual(['main', 'v1.0']);
  });

  it('keeps a plain text box for a field without examples', () => {
    render(
      <SchemaField
        fieldKey="path"
        prop={{ type: 'string', title: 'Path', examples: [] }}
        required={false}
        value=""
        disabled={false}
        onChange={vi.fn()}
      />,
    );
    expect(screen.getByRole('textbox', { name: 'Path' })).toBeTruthy();
  });

  it('reads name, control, then description, with its source beside the name', () => {
    render(
      <SchemaField
        fieldKey="effort"
        prop={{
          type: 'string',
          title: 'Reasoning effort',
          description: 'How much reasoning the model spends per turn.',
          enum: ['low', 'medium', 'high', 'xhigh', 'max', 'auto'],
          'x-source': 'team defaults',
        }}
        required={false}
        value=""
        disabled={false}
        onChange={vi.fn()}
      />,
    );
    const name = screen.getByText('Reasoning effort');
    const description = screen.getByText(
      'How much reasoning the model spends per turn.',
    );
    const control = screen.getByRole('combobox', { name: 'Reasoning effort' });
    const source = screen.getByText('From team defaults');
    const follows = (a: Node, b: Node) =>
      Boolean(a.compareDocumentPosition(b) & Node.DOCUMENT_POSITION_FOLLOWING);
    expect(follows(name, control)).toBe(true);
    expect(follows(control, description)).toBe(true);
    // On the name's line, not under the control.
    expect(source.parentElement).toBe(name.parentElement);
  });

  it('keeps a read-only checkbox at its value', () => {
    const onChange = vi.fn();
    render(
      <SchemaField
        fieldKey="telemetry"
        prop={{
          type: 'boolean',
          title: 'Send usage data',
          default: true,
          readOnly: true,
          'x-source': 'global config',
        }}
        required={false}
        value={true}
        disabled={false}
        onChange={onChange}
      />,
    );
    const box = screen.getByRole('checkbox', { name: 'Send usage data' });
    expect(box.getAttribute('aria-disabled')).toBe('true');
    fireEvent.click(box);
    expect(onChange).not.toHaveBeenCalled();
    expect(screen.getByText('From global config')).toBeTruthy();
  });

  it('says what a checkbox does under it, at the top level too', () => {
    render(
      <SchemaField
        fieldKey="copyEnv"
        prop={{
          type: 'boolean',
          title: 'Copy .env',
          description: 'Copy the project’s .env into the workstream',
        }}
        required={false}
        value={false}
        disabled={false}
        onChange={vi.fn()}
      />,
    );
    expect(
      screen.getByText('Copy the project’s .env into the workstream'),
    ).toBeTruthy();
  });
});

const container: SchemaProp = {
  type: 'object',
  title: 'Container',
  description: 'The container settings for this session.',
  properties: {
    features: {
      type: 'array',
      title: 'Features',
      uniqueItems: true,
      items: {
        type: 'string',
        oneOf: [
          { const: 'devtools', title: 'devtools', description: 'Core tools' },
          { const: 'playwright', title: 'playwright', description: 'Browsers' },
        ],
      },
      default: ['devtools'],
      'x-source': 'built-in defaults',
    },
    dirs: {
      type: 'array',
      title: 'Directories',
      items: {
        type: 'object',
        title: 'Directory',
        required: ['path'],
        properties: {
          path: { type: 'string', title: 'Path', format: 'directory' },
          readonly: { type: 'boolean', title: 'Read-only', default: true },
        },
      },
      default: [],
      'x-inherited': {
        values: [{ path: '/home/me/shared', readonly: true }],
        source: 'global config',
      },
    },
    ports: {
      type: 'array',
      title: 'Open from your browser',
      items: { type: 'string' },
      default: [],
      'x-inherited': { values: ['3000'], source: 'project config' },
    },
    bridgePorts: {
      type: 'array',
      title: 'Reach host services',
      items: { type: 'integer' },
      default: [],
    },
  },
};

/** A form holding one field, keeping its value as the forms do. */
function Harness({
  prop,
  initial,
  errors,
  groupsOpen,
  onValue,
}: {
  prop: SchemaProp;
  initial?: ConfigValue;
  errors?: Record<string, string>;
  groupsOpen?: boolean;
  onValue?: (value: ConfigValue) => void;
}) {
  const [value, setValue] = useState<ConfigValue>(
    initial ?? defaultsOf({ properties: { container: prop } }).container,
  );
  return (
    <form aria-label="Form">
      <SchemaField
        fieldKey="container"
        prop={prop}
        required={false}
        value={value}
        disabled={false}
        errors={errors}
        groupsOpen={groupsOpen}
        onChange={(next) => {
          setValue(next);
          onValue?.(next);
        }}
      />
    </form>
  );
}

const containerDefaults = defaultsOf({ properties: { container } })
  .container as ConfigObject;

describe('SchemaField group', () => {
  it('starts collapsed on a summary of Defaults, and expands', () => {
    render(<Harness prop={container} />);
    // The group by its title; its header by title and summary, read once.
    expect(screen.getByRole('group', { name: 'Container' })).toBeTruthy();
    const header = screen.getByRole('button', {
      name: 'Container Defaults',
    });
    expect(header.getAttribute('aria-expanded')).toBe('false');
    expect(screen.getByTestId('group-summary').textContent).toBe('Defaults');
    expect(screen.queryByRole('checkbox', { name: 'playwright' })).toBeNull();

    fireEvent.click(header);
    expect(header.getAttribute('aria-expanded')).toBe('true');
    expect(screen.getByRole('checkbox', { name: 'playwright' })).toBeTruthy();
    expect(screen.getByText('From built-in defaults')).toBeTruthy();
  });

  it('summarizes, marks and resets what changed', () => {
    const onValue = vi.fn();
    render(<Harness prop={container} groupsOpen onValue={onValue} />);
    fireEvent.click(screen.getByRole('checkbox', { name: 'playwright' }));
    expect(screen.getByTestId('group-summary').textContent).toBe(
      'Features +playwright',
    );
    expect(screen.queryByText('From built-in defaults')).toBeNull();

    fireEvent.click(
      screen.getByRole('button', { name: 'Reset Features to its default' }),
    );
    expect(screen.getByTestId('group-summary').textContent).toBe('Defaults');
    expect(onValue).toHaveBeenLastCalledWith(
      expect.objectContaining({ features: ['devtools'] }),
    );
  });

  it('adds chips with Enter, as numbers for integer items, once each', () => {
    const onValue = vi.fn();
    render(<Harness prop={container} groupsOpen onValue={onValue} />);
    const input = screen.getByRole('textbox', {
      name: 'Add to Reach host services',
    });
    fireEvent.change(input, { target: { value: '5432' } });
    fireEvent.keyDown(input, { key: 'Enter' });
    expect(onValue).toHaveBeenLastCalledWith(
      expect.objectContaining({ bridgePorts: [5432] }),
    );
    fireEvent.change(input, { target: { value: '5432' } });
    fireEvent.keyDown(input, { key: 'Enter' });
    expect(screen.getByText('5432 is already listed.')).toBeTruthy();
    fireEvent.change(input, { target: { value: 'db' } });
    fireEvent.keyDown(input, { key: 'Enter' });
    expect(screen.getByText('Enter a whole number.')).toBeTruthy();

    fireEvent.click(screen.getByRole('button', { name: 'Delete 5432' }));
    expect(onValue).toHaveBeenLastCalledWith(
      expect.objectContaining({ bridgePorts: [] }),
    );
  });

  // One live region, there before it speaks, so it is heard.
  it('says why a chip was not added in a status there from the start', () => {
    render(<Harness prop={container.properties?.bridgePorts ?? {}} />);
    const status = screen.getByRole('status');
    expect(status.textContent).toBe('');
    const input = screen.getByRole('textbox', {
      name: 'Add to Reach host services',
    });
    fireEvent.change(input, { target: { value: 'db' } });
    fireEvent.keyDown(input, { key: 'Enter' });
    expect(status.textContent).toBe('Enter a whole number.');
    fireEvent.change(input, { target: { value: '5' } });
    expect(status.textContent).toBe('');
  });

  it('puts the caret in a chip list when its box is clicked anywhere', () => {
    render(<Harness prop={container} groupsOpen />);
    const input = screen.getByRole('textbox', {
      name: 'Add to Reach host services',
    });
    fireEvent.click(input.parentElement!);
    expect(document.activeElement).toBe(input);
  });

  it('shows inherited values locked, and keeps them out of the value', () => {
    const onValue = vi.fn();
    render(<Harness prop={container} groupsOpen onValue={onValue} />);
    expect(screen.getByText('3000')).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Delete 3000' })).toBeNull();
    expect(screen.getByText('From project config')).toBeTruthy();
    // An inherited value is already listed.
    const input = screen.getByRole('textbox', {
      name: 'Add to Open from your browser',
    });
    fireEvent.change(input, { target: { value: '3000' } });
    fireEvent.keyDown(input, { key: 'Enter' });
    expect(onValue).not.toHaveBeenCalled();

    const locked = screen.getByRole('group', {
      name: 'Directory from global config',
    });
    expect(
      within(locked).getByRole('textbox', { name: 'Path' }),
    ).toHaveProperty('disabled', true);
    expect(within(locked).queryByRole('button')).toBeNull();
  });

  it('adds a row from the item defaults, focused, and removes it', () => {
    const onValue = vi.fn();
    render(<Harness prop={container} groupsOpen onValue={onValue} />);
    fireEvent.click(screen.getByRole('button', { name: 'Add directory' }));
    const row = screen.getByRole('group', { name: 'Directory 1' });
    const path = within(row).getByRole('textbox', { name: 'Path' });
    expect(document.activeElement).toBe(path);
    expect(within(row).getByRole('checkbox', { name: 'Read-only' })).toHaveProperty(
      'checked',
      true,
    );
    fireEvent.change(path, { target: { value: '/data' } });
    fireEvent.click(within(row).getByRole('checkbox', { name: 'Read-only' }));
    expect(onValue).toHaveBeenLastCalledWith(
      expect.objectContaining({ dirs: [{ path: '/data', readonly: false }] }),
    );
    expect(screen.getByTestId('group-summary').textContent).toBe(
      'Directories +1',
    );
    fireEvent.click(screen.getByRole('button', { name: 'Delete directory 1' }));
    expect(onValue).toHaveBeenLastCalledWith(
      expect.objectContaining({ dirs: [] }),
    );
  });

  it('folds a long checklist past its defaults and checked choices', () => {
    const many: SchemaProp = {
      type: 'array',
      title: 'Features',
      items: {
        type: 'string',
        enum: ['a', 'b', 'c', 'd', 'e', 'f', 'g', 'h'],
      },
      default: ['b'],
    };
    render(
      <SchemaField
        fieldKey="features"
        prop={many}
        required={false}
        value={['b', 'g']}
        disabled={false}
        onChange={vi.fn()}
      />,
    );
    expect(
      screen.getAllByRole('checkbox').map((box) => box.getAttribute('aria-label')),
    ).toEqual(['b', 'g']);
    fireEvent.click(screen.getByRole('button', { name: '6 more' }));
    expect(screen.getAllByRole('checkbox')).toHaveLength(8);
  });

  it('opens on a refusal blaming a field inside it, and shows it there', () => {
    const errors = { 'container/bridgePorts': 'must be at most 65535' };
    const { rerender } = render(<Harness prop={container} />);
    expect(
      screen.getByRole('button', { name: /^Container / }).getAttribute('aria-expanded'),
    ).toBe('false');
    rerender(<Harness prop={container} errors={errors} />);
    expect(
      screen.getByRole('button', { name: /^Container / }).getAttribute('aria-expanded'),
    ).toBe('true');
    expect(screen.getByText('Must be at most 65535')).toBeTruthy();
  });

  it('describes its header by a refusal blaming the group itself', () => {
    render(
      <Harness
        prop={container}
        errors={{ container: 'must not have additional properties' }}
      />,
    );
    const header = screen.getByRole('button', { name: /^Container / });
    const described = (header.getAttribute('aria-describedby') ?? '')
      .split(' ')
      .map((id) => document.getElementById(id)?.textContent);
    expect(described).toEqual(['Must not have additional properties']);
  });

  it('keeps focus on a row when one before it is removed', () => {
    render(
      <Harness
        prop={container}
        groupsOpen
        initial={{
          ...containerDefaults,
          dirs: [
            { path: '/a', readonly: true },
            { path: '/b', readonly: true },
            { path: '/c', readonly: true },
          ],
        }}
      />,
    );
    const third = within(
      screen.getByRole('group', { name: 'Directory 3' }),
    ).getByRole('textbox', { name: 'Path' });
    third.focus();
    fireEvent.click(screen.getByRole('button', { name: 'Delete directory 2' }));
    expect(document.activeElement).toBe(third);
    expect(third).toHaveProperty('value', '/c');
    expect(
      within(screen.getByRole('group', { name: 'Directory 2' })).getByRole(
        'textbox',
        { name: 'Path' },
      ),
    ).toBe(third);
  });

  it('hands the focus of a removed row on to the next Delete, else to Add', () => {
    render(
      <Harness
        prop={container}
        groupsOpen
        initial={{
          ...containerDefaults,
          dirs: [
            { path: '/a', readonly: true },
            { path: '/b', readonly: true },
          ],
        }}
      />,
    );
    const remove = (label: string) => {
      const button = screen.getByRole('button', { name: label });
      button.focus();
      fireEvent.click(button);
    };
    remove('Delete directory 1');
    expect(document.activeElement).toBe(
      screen.getByRole('button', { name: 'Delete directory 1' }),
    );
    remove('Delete directory 1');
    expect(document.activeElement).toBe(
      screen.getByRole('button', { name: 'Add directory' }),
    );
  });
});

describe('firstField', () => {
  it('passes over the fields of a collapsed group', () => {
    render(<Harness prop={container} />);
    const form = screen.getByRole('form', { name: 'Form' });
    expect(firstField(form)).toBeNull();
    expect(focusFirstField(form)).toBe(false);
    expect(document.activeElement).toBe(document.body);

    fireEvent.click(screen.getByRole('button', { name: /^Container / }));
    expect(firstField(form)).toBe(
      screen.getByRole('checkbox', { name: 'devtools' }),
    );
  });

  // An inherited row's boxes are disabled: they cannot take focus.
  it('passes over the rows a list inherits', () => {
    render(<Harness prop={container.properties?.dirs ?? {}} />);
    const form = screen.getByRole('form', { name: 'Form' });
    expect(firstField(form)).toBeNull();
    expect(focusFirstField(form)).toBe(false);

    fireEvent.click(screen.getByRole('button', { name: 'Add directory' }));
    act(() => (document.activeElement as HTMLElement).blur());
    expect(focusFirstField(form)).toBe(true);
    expect(document.activeElement).toBe(
      within(screen.getByRole('group', { name: 'Directory 1' })).getByRole(
        'textbox',
        { name: 'Path' },
      ),
    );
  });
});

describe('refusedFields', () => {
  it('blames the deepest field holding a pointer', () => {
    render(<Harness prop={container} />);
    const form = screen.getByRole('form', { name: 'Form' });
    expect(
      refusedFields(form, {
        '/container/bridgePorts/0': 'must be at most 65535',
      }),
    ).toEqual({ 'container/bridgePorts': 'must be at most 65535' });
    expect(document.activeElement).toBe(document.body);
  });
});

describe('a refused row', () => {
  // The second row is blank, so it is not sent: the third is sent second.
  const dirs = [
    { path: '/a', readonly: true },
    { path: '', readonly: true },
    { path: '/b', readonly: true },
  ];
  const pathOfRow = (row: number) =>
    within(screen.getByRole('group', { name: `Directory ${row}` })).getByRole(
      'textbox',
      { name: 'Path' },
    );

  it('is blamed by its index as sent', () => {
    render(
      <Harness
        prop={container}
        groupsOpen
        initial={{ ...containerDefaults, dirs }}
      />,
    );
    const form = screen.getByRole('form', { name: 'Form' });
    expect(
      refusedFields(form, { '/container/dirs/1/path': 'is not a directory' }),
    ).toEqual({ 'container/dirs/1': 'is not a directory' });
  });

  it('alone is marked, explained and focused', () => {
    render(
      <Harness
        prop={container}
        groupsOpen
        initial={{ ...containerDefaults, dirs }}
        errors={{ 'container/dirs/1': 'is not a directory' }}
      />,
    );
    expect(pathOfRow(3).getAttribute('aria-invalid')).toBe('true');
    expect(descriptionOf(pathOfRow(3))).toBe('Is not a directory');
    expect(pathOfRow(1).getAttribute('aria-invalid')).toBeNull();
    expect(pathOfRow(2).getAttribute('aria-invalid')).toBeNull();

    focusBlamedField(screen.getByRole('form', { name: 'Form' }), {
      'container/dirs/1': 'is not a directory',
    });
    expect(document.activeElement).toBe(pathOfRow(3));
  });

  const refused = () =>
    render(
      <Harness
        prop={container}
        groupsOpen
        initial={{ ...containerDefaults, dirs }}
        errors={{ 'container/dirs/1': 'is not a directory' }}
      />,
    );

  it('keeps its mark when a blank row before it is filled in', () => {
    refused();
    fireEvent.change(pathOfRow(2), { target: { value: '/c' } });
    expect(pathOfRow(2).getAttribute('aria-invalid')).toBeNull();
    expect(pathOfRow(3).getAttribute('aria-invalid')).toBe('true');
  });

  it('keeps its mark when a row before it is removed', () => {
    refused();
    fireEvent.click(screen.getByRole('button', { name: 'Delete directory 1' }));
    expect(pathOfRow(1).getAttribute('aria-invalid')).toBeNull();
    expect(pathOfRow(2).getAttribute('aria-invalid')).toBe('true');
    expect(screen.getByText('Is not a directory')).toBeTruthy();
  });
});

describe('focusBlamedField', () => {
  it('focuses the field blamed first', () => {
    render(<Harness prop={container} groupsOpen />);
    const form = screen.getByRole('form', { name: 'Form' });
    focusBlamedField(form, {
      'container/bridgePorts': 'bad',
      'container/ports': 'bad',
    });
    expect(document.activeElement).toBe(
      screen.getByRole('textbox', { name: 'Add to Reach host services' }),
    );
  });

  it('focuses a list’s own row, past the rows it inherits', () => {
    render(
      <Harness
        prop={container}
        groupsOpen
        initial={{ ...containerDefaults, dirs: [{ path: '', readonly: true }] }}
      />,
    );
    const form = screen.getByRole('form', { name: 'Form' });
    focusBlamedField(form, { 'container/dirs': 'bad' });
    expect(document.activeElement).toBe(
      within(screen.getByRole('group', { name: 'Directory 1' })).getByRole(
        'textbox',
        { name: 'Path' },
      ),
    );
  });

  // A collapsed group opens as it renders the refusal; the form focuses after.
  it('leaves a collapsed group as React drew it', () => {
    render(<Harness prop={container} />);
    const form = screen.getByRole('form', { name: 'Form' });
    focusBlamedField(form, { 'container/bridgePorts': 'bad' });
    expect(document.activeElement).toBe(document.body);
    expect(
      screen.getByRole('button', { name: /^Container / }).getAttribute(
        'aria-expanded',
      ),
    ).toBe('false');
    expect(screen.queryByRole('textbox')).toBeNull();
  });
});

describe('addDrafts', () => {
  it('adds what is typed, and blames a list it cannot add to', () => {
    const onValue = vi.fn();
    render(<Harness prop={container} groupsOpen onValue={onValue} />);
    const form = screen.getByRole('form', { name: 'Form' });
    expect(addDrafts(form)).toEqual({});
    const ports = screen.getByRole('textbox', {
      name: 'Add to Reach host services',
    });
    const browser = screen.getByRole('textbox', {
      name: 'Add to Open from your browser',
    });
    fireEvent.change(ports, { target: { value: '5432x' } });
    let refused = {};
    act(() => {
      refused = addDrafts(form);
    });
    expect(Object.keys(refused)).toEqual(['container/bridgePorts']);
    expect(screen.getByText('Enter a whole number.')).toBeTruthy();

    // Both lists of the group get theirs, the second on top of the first.
    fireEvent.change(ports, { target: { value: '5432' } });
    fireEvent.change(browser, { target: { value: '8080' } });
    act(() => {
      refused = addDrafts(form);
    });
    expect(refused).toEqual({});
    expect(onValue).toHaveBeenLastCalledWith(
      expect.objectContaining({ ports: ['8080'], bridgePorts: [5432] }),
    );
    expect(ports).toHaveProperty('value', '');
  });

  // A click on the form's button leaves the box: nothing may move under it.
  it('leaves what is typed alone when the box is left', () => {
    const onValue = vi.fn();
    render(<Harness prop={container} groupsOpen onValue={onValue} />);
    const ports = screen.getByRole('textbox', {
      name: 'Add to Reach host services',
    });
    for (const typed of ['5432x', '5432']) {
      fireEvent.change(ports, { target: { value: typed } });
      fireEvent.blur(ports);
      expect(
        screen.getAllByRole('status').map((status) => status.textContent),
      ).toEqual(['', '']);
    }
    expect(onValue).not.toHaveBeenCalled();
    expect(ports).toHaveProperty('value', '5432');
  });
});

/** An element's accessible description, from its aria-describedby. */
const descriptionOf = (element: Element) =>
  (element.getAttribute('aria-describedby') ?? '')
    .split(' ')
    .map((id) => document.getElementById(id)?.textContent ?? '')
    .join(' ');

describe('SchemaField descriptions', () => {
  const field = (prop: SchemaProp, value: ConfigValue, error?: string) =>
    render(
      <SchemaField
        fieldKey="field"
        prop={{ title: 'Field', description: 'What it does.', ...prop }}
        required={false}
        value={value}
        disabled={false}
        onChange={vi.fn()}
        error={error}
      />,
    );

  it.each([
    ['a text box', { type: 'string' }, '', 'textbox'],
    ['a number box', { type: 'integer' }, '', 'spinbutton'],
    ['a long text', { type: 'string', format: 'textarea' }, '', 'textbox'],
    ['a select', { type: 'string', enum: ['a', 'b'] }, '', 'combobox'],
    [
      'pills',
      { type: 'string', enum: ['a', 'b'], default: 'a' },
      'a',
      'radiogroup',
    ],
    ['a checkbox', { type: 'boolean' }, false, 'checkbox'],
    ['a list', { type: 'array', items: { type: 'string' } }, [], 'group'],
  ] as const)(
    'describes %s by its error, then the field’s description',
    (_, prop, value, role) => {
      field(prop as SchemaProp, value as ConfigValue);
      const control = screen.getByRole(role, { name: 'Field' });
      expect(descriptionOf(control)).toBe('What it does.');
      cleanup();

      field(prop as SchemaProp, value as ConfigValue, 'is wrong');
      expect(descriptionOf(screen.getByRole(role, { name: 'Field' }))).toBe(
        'Is wrong What it does.',
      );
    },
  );

  it('describes each choice by its own description, whatever its value', () => {
    render(
      <SchemaField
        fieldKey="tags"
        prop={{
          type: 'array',
          title: 'Tags',
          items: {
            type: 'string',
            oneOf: [
              { const: 'a b', description: 'Spaced' },
              { const: 'a"b', description: 'Quoted' },
            ],
          },
        }}
        required={false}
        value={[]}
        disabled={false}
        onChange={vi.fn()}
      />,
    );
    const box = (name: string) => screen.getByRole('checkbox', { name });
    expect(descriptionOf(box('a b'))).toBe('Spaced');
    expect(descriptionOf(box('a"b'))).toBe('Quoted');
  });
});

describe('SchemaField lists', () => {
  const features: SchemaProp = {
    type: 'array',
    title: 'Features',
    items: { type: 'string', enum: ['a', 'b', 'c', 'd', 'e', 'f', 'g', 'h'] },
    default: ['b'],
  };
  const group: SchemaProp = {
    type: 'object',
    title: 'Settings',
    properties: {
      features: { ...features, items: { type: 'string', enum: ['a', 'b'] } },
      other: { type: 'boolean', title: 'Other' },
    },
  };

  it('lists a value not among the choices checked, so it can be unchecked', () => {
    const onValue = vi.fn();
    render(
      <Harness
        prop={group}
        groupsOpen
        initial={{ features: ['b', 'gone'], other: false }}
        onValue={onValue}
      />,
    );
    const gone = screen.getByRole('checkbox', { name: 'gone' });
    expect(gone).toHaveProperty('checked', true);
    expect(descriptionOf(gone)).toBe('Not offered any more');
    expect(screen.getByTestId('group-summary').textContent).toBe(
      'Features +gone',
    );

    // Kept as the other choices change, and gone once unchecked.
    fireEvent.click(screen.getByRole('checkbox', { name: 'a' }));
    expect(onValue).toHaveBeenLastCalledWith(
      expect.objectContaining({ features: ['a', 'b', 'gone'] }),
    );
    fireEvent.click(gone);
    expect(onValue).toHaveBeenLastCalledWith(
      expect.objectContaining({ features: ['a', 'b'] }),
    );
    expect(screen.getByRole('checkbox', { name: 'gone' })).toHaveProperty(
      'checked',
      false,
    );
  });

  it('keeps a choice checked while unfolded in view when folded again', () => {
    render(<Harness prop={features} initial={['b']} />);
    fireEvent.click(screen.getByRole('button', { name: '7 more' }));
    fireEvent.click(screen.getByRole('checkbox', { name: 'e' }));
    fireEvent.click(screen.getByRole('button', { name: 'Fewer' }));
    expect(
      screen
        .getAllByRole('checkbox')
        .map((box) => box.getAttribute('aria-label')),
    ).toEqual(['b', 'e']);
    expect(screen.getByRole('button', { name: '6 more' })).toBeTruthy();
  });

  it('hands the focus of a removed chip on to the next, else the one before, else the box', () => {
    render(
      <Harness
        prop={container}
        groupsOpen
        initial={{ ...containerDefaults, bridgePorts: [1, 2] }}
      />,
    );
    const remove = (label: string) => {
      const button = screen.getByRole('button', { name: label });
      button.focus();
      fireEvent.click(button);
    };
    remove('Delete 1');
    expect(document.activeElement).toBe(
      screen.getByRole('button', { name: 'Delete 2' }),
    );
    remove('Delete 2');
    expect(document.activeElement).toBe(
      screen.getByRole('textbox', { name: 'Add to Reach host services' }),
    );
  });

  it('hands focus to the chip before the last one removed', () => {
    render(
      <Harness
        prop={container}
        groupsOpen
        initial={{ ...containerDefaults, bridgePorts: [1, 2] }}
      />,
    );
    const last = screen.getByRole('button', { name: 'Delete 2' });
    last.focus();
    fireEvent.click(last);
    expect(document.activeElement).toBe(
      screen.getByRole('button', { name: 'Delete 1' }),
    );
  });

  it('counts no blank row as a change', () => {
    render(<Harness prop={container} groupsOpen />);
    fireEvent.click(screen.getByRole('button', { name: 'Add directory' }));
    expect(screen.getByTestId('group-summary').textContent).toBe('Defaults');
    expect(
      screen.queryByRole('button', {
        name: 'Reset Directories to its default',
      }),
    ).toBeNull();
  });

  it('counts a number typed as its default as no change', () => {
    render(
      <Harness
        prop={{
          type: 'object',
          title: 'Limits',
          properties: { cpus: { type: 'integer', title: 'CPUs', default: 2 } },
        }}
        groupsOpen
      />,
    );
    const cpus = screen.getByRole('spinbutton', { name: 'CPUs' });
    fireEvent.change(cpus, { target: { value: '4' } });
    expect(screen.getByTestId('group-summary').textContent).toBe('CPUs: 4');
    fireEvent.change(cpus, { target: { value: '2' } });
    expect(screen.getByTestId('group-summary').textContent).toBe('Defaults');
  });
});

describe('SchemaField group without fields', () => {
  const empty: SchemaProp = {
    type: 'object',
    title: 'Extras (not reported)',
    description: 'Nothing to set here this time.',
    properties: {},
  };

  it('shows its title and description, with nothing to fold or count', () => {
    render(<Harness prop={empty} />);
    const group = screen.getByRole('group', { name: 'Extras (not reported)' });
    expect(
      within(group).getByText('Nothing to set here this time.'),
    ).toBeTruthy();
    expect(within(group).queryByRole('button')).toBeNull();
    expect(screen.queryByTestId('group-summary')).toBeNull();
  });

  it('is described by its description, then a refusal blaming it', () => {
    render(
      <Harness prop={empty} errors={{ container: 'could not be applied' }} />,
    );
    const group = screen.getByRole('group', { name: 'Extras (not reported)' });
    expect(descriptionOf(group)).toBe(
      'Nothing to set here this time. Could not be applied',
    );
  });
});
