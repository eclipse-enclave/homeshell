import { describeDaemonError, fieldLabel } from './field-errors';

describe('fieldLabel', () => {
  it('reads a pointer as the field it names', () => {
    expect(fieldLabel('/branch')).toBe('Branch');
    expect(fieldLabel('/path')).toBe('Path');
  });

  it('splits camelCase into words', () => {
    expect(fieldLabel('/baseRef')).toBe('Base ref');
  });

  it('takes the last segment of a nested pointer', () => {
    expect(fieldLabel('/config/branch')).toBe('Branch');
  });

  it('decodes RFC 6901 escapes, so a key with a slash still reads', () => {
    expect(fieldLabel('/weird~1key')).toBe('Weird/key');
    expect(fieldLabel('/weird~0key')).toBe('Weird~key');
  });

  // A schema reports a *missing* property against the object, not against the
  // key that is not there — so the root pointer names no field to blame.
  it('names no field for the root pointer', () => {
    expect(fieldLabel('')).toBe('');
    expect(fieldLabel('/')).toBe('');
  });
});

describe('describeDaemonError', () => {
  it('attributes a field error to its input and drops the pointer', () => {
    const message = describeDaemonError(
      {
        error: 'configuration is invalid',
        fields: { '/branch': 'branch name "my branch" contains illegal characters' },
      },
      'Create failed.',
    );
    expect(message).toBe(
      'Branch: branch name "my branch" contains illegal characters',
    );
    // The notation is gone — that is the whole point.
    expect(message).not.toContain('/branch');
  });

  it('drops the generic wrapper, which says nothing the reasons do not', () => {
    const message = describeDaemonError(
      { error: 'configuration is invalid', fields: { '/path': 'must be absolute' } },
      'Create failed.',
    );
    expect(message).not.toContain('configuration is invalid');
  });

  it('joins several offending fields', () => {
    const message = describeDaemonError(
      { error: 'configuration is invalid', fields: { '/branch': 'bad', '/path': 'worse' } },
      'Create failed.',
    );
    expect(message).toContain('Branch: bad');
    expect(message).toContain('Path: worse');
  });

  it('drops the empty label for a root-level reason', () => {
    // What a missing required property looks like: "/: missing properties" would
    // read as a field called "/".
    expect(
      describeDaemonError(
        {
          error: 'configuration is invalid',
          fields: { '/': "missing properties: 'branch'" },
        },
        'Create failed.',
      ),
    ).toBe("Missing properties: 'branch'");
  });

  it('keeps the daemon’s own message when no field is blamed, as a sentence', () => {
    expect(
      describeDaemonError({ error: 'provider is unavailable' }, 'Create failed.'),
    ).toBe('Provider is unavailable');
  });

  it('falls back when the body is unparseable or empty', () => {
    expect(describeDaemonError(null, 'Create failed.')).toBe('Create failed.');
    expect(describeDaemonError({}, 'Create failed.')).toBe('Create failed.');
    expect(describeDaemonError({ fields: {} }, 'Create failed.')).toBe(
      'Create failed.',
    );
  });
});
