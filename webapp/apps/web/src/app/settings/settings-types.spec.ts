import {
  DEFAULT_SETTINGS,
  mergeSettings,
  resolveDefault,
} from './settings-types';

describe('resolveDefault', () => {
  it('returns the fixed value when last-used is off', () => {
    expect(resolveDefault({ value: 'in-place', useLastUsed: false, lastUsed: 'copy' })).toBe(
      'in-place',
    );
  });

  it('returns the last-used value when enabled and recorded', () => {
    expect(resolveDefault({ value: 'in-place', useLastUsed: true, lastUsed: 'copy' })).toBe(
      'copy',
    );
  });

  it('falls back to the fixed value when last-used is enabled but unset', () => {
    expect(resolveDefault({ value: 'in-place', useLastUsed: true })).toBe('in-place');
  });
});

describe('mergeSettings', () => {
  it('defaults to half-second polling without overriding an explicit interval', () => {
    expect(DEFAULT_SETTINGS.providers.refreshIntervalSec).toBe(0.5);
    expect(mergeSettings({}).providers.refreshIntervalSec).toBe(0.5);
    expect(
      mergeSettings({ providers: { refreshIntervalSec: 5 } }).providers.refreshIntervalSec,
    ).toBe(5);
  });

  it('returns the defaults for empty or non-object input', () => {
    expect(mergeSettings(null)).toEqual(DEFAULT_SETTINGS);
    expect(mergeSettings('nonsense')).toEqual(DEFAULT_SETTINGS);
  });

  it('deep-merges a partial object over the defaults', () => {
    const merged = mergeSettings({ projects: { sidebarSort: 'name' } });
    expect(merged.projects.sidebarSort).toBe('name');
    // Untouched groups keep their defaults.
    expect(merged.sessions.previewLines).toBe(
      DEFAULT_SETTINGS.sessions.previewLines,
    );
  });

  it('preserves dynamic optional keys not present in the defaults', () => {
    const merged = mergeSettings({
      workstreams: {
        defaultStrategy: { value: 'in-place', useLastUsed: true, lastUsed: 'worktree' },
      },
    });
    expect(merged.workstreams.defaultStrategy.lastUsed).toBe('worktree');
    expect(resolveDefault(merged.workstreams.defaultStrategy)).toBe('worktree');
  });

  it('discards stored values whose type does not match the default', () => {
    const merged = mergeSettings({ sessions: { previewLines: 'not-a-number' } });
    expect(merged.sessions.previewLines).toBe(DEFAULT_SETTINGS.sessions.previewLines);
  });

  it('resets an out-of-enum sidebar sort to the default', () => {
    // A stored string of the right type but outside the closed enum would leave
    // a Select/RadioGroup with no matching option; it must fall back.
    const merged = mergeSettings({ projects: { sidebarSort: 'sideways' } });
    expect(merged.projects.sidebarSort).toBe(
      DEFAULT_SETTINGS.projects.sidebarSort,
    );
  });

  it('keeps a valid stored notification style and sound', () => {
    const merged = mergeSettings({
      notifications: { style: 'native', sound: 'chime' },
    });
    expect(merged.notifications.style).toBe('native');
    expect(merged.notifications.sound).toBe('chime');
  });

  it('resets an out-of-enum notification style to the default', () => {
    const merged = mergeSettings({ notifications: { style: 'telepathy' } });
    expect(merged.notifications.style).toBe(
      DEFAULT_SETTINGS.notifications.style,
    );
  });

  it('keeps the per-event notification defaults when a stored document predates them', () => {
    // Settings written before the per-event switches existed carry only the
    // style and sound. Reading them as "every event off" would silence the app.
    const merged = mergeSettings({
      notifications: { style: 'in-app', sound: 'chime' },
    });
    expect(merged.notifications.waiting).toEqual({ notify: true, sound: true });
    expect(merged.notifications.finished).toEqual({ notify: true, sound: true });
    expect(merged.notifications.quietSeconds).toBe(
      DEFAULT_SETTINGS.notifications.quietSeconds,
    );
  });

  it('merges one event switch without disturbing the rest', () => {
    const merged = mergeSettings({
      notifications: { finished: { sound: false }, quietSeconds: 30 },
    });
    expect(merged.notifications.finished).toEqual({
      notify: true,
      sound: false,
    });
    expect(merged.notifications.waiting).toEqual({ notify: true, sound: true });
    expect(merged.notifications.quietSeconds).toBe(30);
  });
});
