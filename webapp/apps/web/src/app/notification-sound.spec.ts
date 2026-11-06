import { playNotificationSound } from './notification-sound';

/**
 * The named sounds are short synthesized tones (no bundled audio assets), so
 * what a name means is a frequency and a duration. jsdom has no Web Audio, so
 * these tests supply a stub AudioContext and assert on the oscillators the
 * module asks it for.
 */

interface StartedTone {
  frequency: number;
  type: string;
}

/** Oscillators started since the last reset, across all stub contexts. */
let started: StartedTone[] = [];

class StubOscillator {
  frequency = { value: 0 };
  type = '';
  connect() {
    /* the graph shape is not what these tests are about */
  }
  start() {
    started.push({ frequency: this.frequency.value, type: this.type });
  }
  stop() {
    /* no-op */
  }
}

class StubAudioContext {
  currentTime = 0;
  destination = {};
  resume() {
    return Promise.resolve();
  }
  createOscillator() {
    return new StubOscillator();
  }
  createGain() {
    // The envelope is shaped on this node; these tests care only about the
    // oscillator, so the calls are recorded nowhere.
    const ignore = () => undefined;
    return {
      gain: { setValueAtTime: ignore, exponentialRampToValueAtTime: ignore },
      connect: ignore,
    };
  }
}

describe('playNotificationSound', () => {
  beforeEach(() => {
    started = [];
    // The module caches the first context it builds, so the stub has to be in
    // place before any call — every test in this file shares one instance.
    vi.stubGlobal('AudioContext', StubAudioContext);
  });

  it('plays a tone for a named sound', () => {
    playNotificationSound('chime');
    expect(started).toHaveLength(1);
    expect(started[0].frequency).toBeGreaterThan(0);
  });

  it('gives different sounds different tones', () => {
    playNotificationSound('chime');
    playNotificationSound('glass');
    expect(started[0].frequency).not.toBe(started[1].frequency);
  });

  // "Silent" is a real choice, not a missing one: it must reach the speakers as
  // nothing at all, which is also what makes it an honest preview.
  it.each(['none', 'silent', 'None'])('plays nothing for %s', (name) => {
    playNotificationSound(name);
    expect(started).toEqual([]);
  });

  // An unknown name (an older daemon's sound, a hand-edited settings file) is
  // audible rather than silent — a notification that cannot be heard reads as a
  // broken notification.
  it.each([undefined, 'brontosaurus'])('falls back to the default for %s', (name) => {
    playNotificationSound(name);
    expect(started).toHaveLength(1);
  });

  it('never throws when the browser has no Web Audio', () => {
    vi.stubGlobal('AudioContext', undefined);
    vi.stubGlobal('webkitAudioContext', undefined);
    expect(() => playNotificationSound('chime')).not.toThrow();
  });
});
