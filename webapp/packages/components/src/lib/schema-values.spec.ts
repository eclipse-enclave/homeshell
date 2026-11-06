import {
  type SchemaProp,
  buildConfig,
  changedPaths,
  changesOf,
  defaultsOf,
  fieldsOf,
  isDefault,
  itemNounOf,
  kindOf,
  pathHolds,
  pathOf,
  summaryOf,
} from './schema-values';

const sandbox: SchemaProp = {
  type: 'object',
  title: 'Sandbox',
  properties: {
    features: {
      type: 'array',
      title: 'Features',
      uniqueItems: true,
      items: {
        type: 'string',
        oneOf: [
          { const: 'devtools', title: 'devtools' },
          { const: 'playwright', title: 'playwright' },
          { const: 'python-dev', title: 'python-dev' },
        ],
      },
      default: ['devtools', 'python-dev'],
    },
    dirs: {
      type: 'array',
      title: 'Directories',
      items: {
        type: 'object',
        required: ['path'],
        properties: {
          path: { type: 'string', title: 'Path', format: 'directory' },
          readonly: { type: 'boolean', title: 'Read-only', default: true },
        },
      },
      default: [],
    },
    bridgePorts: {
      type: 'array',
      title: 'Reach host services',
      items: { type: 'integer' },
      default: [],
    },
    allowDomains: {
      type: 'array',
      title: 'Extra allowed domains',
      items: { type: 'string' },
      default: [],
    },
    hostConfig: {
      type: 'boolean',
      title: 'Pass through my host agent config',
      default: false,
    },
  },
};

const children = sandbox.properties ?? {};

const schema = {
  type: 'object',
  properties: {
    model: { type: 'string', enum: ['opus', 'sonnet'], default: 'sonnet' },
    sandbox,
  },
};

describe('kindOf', () => {
  it.each([
    ['group', sandbox],
    ['checklist', children.features],
    ['checklist', { type: 'array', items: { enum: ['a', 'b'] } }],
    ['rows', children.dirs],
    ['chips', children.bridgePorts],
    ['scalar', { type: 'string' }],
    ['scalar', { type: 'object' }],
  ] as const)('is %s', (kind, prop) => {
    expect(kindOf(prop as SchemaProp)).toBe(kind);
  });
});

describe('defaultsOf', () => {
  it('fills a group with its children’s defaults', () => {
    expect(defaultsOf(schema)).toEqual({
      model: 'sonnet',
      sandbox: {
        features: ['devtools', 'python-dev'],
        dirs: [],
        bridgePorts: [],
        allowDomains: [],
        hostConfig: false,
      },
    });
  });

  it('merges overrides into a group per child', () => {
    const values = defaultsOf(schema, {
      sandbox: { allowDomains: ['api.example.com'], hostConfig: true },
    });
    expect(values.sandbox).toMatchObject({
      features: ['devtools', 'python-dev'],
      allowDomains: ['api.example.com'],
      hostConfig: true,
    });
  });

  // Shown locked already, they would show again as removable, and be sent.
  it('leaves out of a list the values it inherits', () => {
    const prop: SchemaProp = {
      type: 'object',
      properties: {
        ports: {
          type: 'array',
          items: { type: 'string' },
          'x-inherited': { values: ['3000'], source: 'project config' },
        },
        dirs: {
          type: 'array',
          items: { type: 'object', properties: { path: { type: 'string' } } },
          'x-inherited': { values: [{ path: '/shared' }] },
        },
      },
    };
    const values = defaultsOf(prop, {
      ports: ['3000', '8080'],
      dirs: [{ path: '/shared' }, { path: '/data' }],
    });
    expect(values).toEqual({ ports: ['8080'], dirs: [{ path: '/data' }] });
    expect(buildConfig(fieldsOf(prop), values)).toEqual({
      ports: ['8080'],
      dirs: [{ path: '/data' }],
    });
  });

  it('keeps a locked checkbox at its default over a stored value', () => {
    const locked: SchemaProp = {
      type: 'object',
      properties: {
        telemetry: { type: 'boolean', default: true, readOnly: true },
        hostConfig: { type: 'boolean', default: false },
      },
    };
    expect(
      defaultsOf(locked, { telemetry: false, hostConfig: true }),
    ).toEqual({ telemetry: true, hostConfig: true });
  });
});

describe('buildConfig', () => {
  it('sends a group whole, coercing items and dropping blank rows', () => {
    const values = defaultsOf(schema, {
      sandbox: {
        bridgePorts: ['5432'],
        dirs: [
          { path: '/data', readonly: true },
          { path: '  ', readonly: true },
        ],
      },
    });
    expect(buildConfig(fieldsOf(schema), values)).toEqual({
      model: 'sonnet',
      sandbox: {
        features: ['devtools', 'python-dev'],
        dirs: [{ path: '/data', readonly: true }],
        bridgePorts: [5432],
        allowDomains: [],
        hostConfig: false,
      },
    });
  });

  it('sends an untouched group at its defaults', () => {
    expect(buildConfig(fieldsOf(schema), defaultsOf(schema))).toEqual({
      model: 'sonnet',
      sandbox: {
        features: ['devtools', 'python-dev'],
        dirs: [],
        bridgePorts: [],
        allowDomains: [],
        hostConfig: false,
      },
    });
  });

  it('fills a group that is unset with its defaults', () => {
    const group: SchemaProp = {
      type: 'object',
      required: ['name'],
      properties: {
        name: { type: 'string', default: 'main' },
        verbose: { type: 'boolean' },
      },
    };
    const fields = fieldsOf({ properties: { group } });
    expect(buildConfig(fields, {})).toEqual({
      group: { name: 'main', verbose: false },
    });
  });
});

describe('summaryOf', () => {
  it('says Defaults when nothing changed, in any order', () => {
    expect(summaryOf(changesOf(sandbox, defaultsOf(sandbox)))).toBe(
      'Defaults',
    );
    expect(
      isDefault(children.features, ['python-dev', 'devtools']),
    ).toBe(true);
  });

  it('lists what changed by field title', () => {
    const value = {
      ...defaultsOf(sandbox),
      features: ['devtools', 'playwright'],
      dirs: [{ path: '/a', readonly: true }],
      allowDomains: ['a.example', 'b.example'],
      hostConfig: true,
    };
    expect(changesOf(sandbox, value)).toEqual([
      'Features +playwright −python-dev',
      'Directories +1',
      'Extra allowed domains +2',
      'Pass through my host agent config',
    ]);
    expect(summaryOf(changesOf(sandbox, value))).toBe(
      'Features +playwright −python-dev · Directories +1 · Extra allowed domains +2 · Pass through my host agent config',
    );
  });

  it('says a boolean switched off from a true default', () => {
    const prop: SchemaProp = {
      type: 'object',
      properties: { on: { type: 'boolean', title: 'On', default: true } },
    };
    expect(summaryOf(changesOf(prop, { on: false }))).toBe('On off');
  });

  // As buildConfig sends them: without blank rows, numbers as numbers.
  it('counts neither a blank row nor a number typed as text as a change', () => {
    const blank = {
      ...defaultsOf(sandbox),
      dirs: [{ path: ' ', readonly: true }],
    };
    expect(changesOf(sandbox, blank)).toEqual([]);
    expect(isDefault(children.dirs, blank.dirs)).toBe(true);
    expect(
      changesOf(sandbox, {
        ...blank,
        dirs: [...blank.dirs, { path: '/a', readonly: true }],
      }),
    ).toEqual(['Directories +1']);

    const cpus: SchemaProp = { type: 'integer', title: 'CPUs', default: 2 };
    expect(isDefault(cpus, '2')).toBe(true);
    expect(isDefault(cpus, '3')).toBe(false);
    // Emptied, it is left out, so the default applies.
    expect(isDefault(cpus, '')).toBe(true);
    expect(changesOf({ properties: { cpus } }, { cpus: '' })).toEqual([]);
  });
});

describe('itemNounOf', () => {
  it.each([
    [{ title: 'Directory' }, 'directory'],
    [{ title: 'URL' }, 'URL'],
    [{}, 'item'],
  ])('%o → %s', (items, noun) => {
    expect(itemNounOf({ type: 'array', items })).toBe(noun);
  });
});

describe('paths', () => {
  it('escapes keys as a JSON pointer does', () => {
    expect(pathOf('sandbox', 'a/b')).toBe('sandbox/a~1b');
  });

  it('holds a pointer to the field or into it', () => {
    expect(pathHolds('sandbox/ports', '/sandbox/ports')).toBe(true);
    expect(pathHolds('sandbox/ports', '/sandbox/ports/1')).toBe(true);
    expect(pathHolds('sandbox/ports', '/sandbox/portsX')).toBe(false);
    expect(pathHolds('sandbox', '/sandbox/ports/1')).toBe(true);
  });
});

describe('changedPaths', () => {
  it('names the deepest keys of a group that changed', () => {
    expect(
      changedPaths(
        'sandbox',
        { ports: ['3000'], net: { allow: ['a'], deny: [] }, ro: false },
        { ports: ['3000'], net: { allow: ['a', 'b'], deny: [] }, ro: true },
      ),
    ).toEqual(['sandbox/net/allow', 'sandbox/ro']);
  });

  it('names the path itself for a value that is not a group', () => {
    expect(changedPaths('name', 'a', 'b')).toEqual(['name']);
    expect(changedPaths('tags', ['a'], ['a', 'b'])).toEqual(['tags']);
    expect(changedPaths('group', { a: 1 }, 'x')).toEqual(['group']);
  });

  it('is empty when nothing changed, and escapes keys as paths do', () => {
    expect(changedPaths('group', { a: [1] }, { a: [1] })).toEqual([]);
    expect(changedPaths('group', { 'a/b': 1 }, { 'a/b': 2 })).toEqual([
      'group/a~1b',
    ]);
  });
});
