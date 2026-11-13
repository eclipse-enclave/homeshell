import {
  displaySessionState,
  hasSessionRuntime,
  isSessionActive,
  isSessionTerminal,
  sessionCapabilities,
} from './types';

describe('displaySessionState', () => {
  it('shows an unseen idle session as done ("finished, go look")', () => {
    expect(displaySessionState('idle', false)).toBe('done');
  });

  it('shows a seen idle session as idle', () => {
    expect(displaySessionState('idle', true)).toBe('idle');
  });

  it('passes every non-idle state through unchanged, regardless of seen', () => {
    for (const seen of [true, false]) {
      expect(displaySessionState('running', seen)).toBe('running');
      expect(displaySessionState('waiting', seen)).toBe('waiting');
      expect(displaySessionState('unknown', seen)).toBe('unknown');
      expect(displaySessionState('stopped', seen)).toBe('stopped');
      expect(displaySessionState('done', seen)).toBe('done');
    }
  });
});

describe('isSessionActive', () => {
  it('is true only while running or waiting', () => {
    expect(isSessionActive('running')).toBe(true);
    expect(isSessionActive('waiting')).toBe(true);
    expect(isSessionActive('idle')).toBe(false);
    expect(isSessionActive('unknown')).toBe(false);
    expect(isSessionActive('stopped')).toBe(false);
    expect(isSessionActive('done')).toBe(false);
  });
});

describe('hasSessionRuntime', () => {
  it.each([
    ['running', true],
    ['waiting', true],
    ['idle', true],
    ['unknown', true],
    ['starting', false],
    ['stopped', false],
    ['done', false],
    ['error', false],
  ] as const)('%s: %s', (state, has) => {
    expect(hasSessionRuntime(state)).toBe(has);
  });
});

describe('isSessionTerminal', () => {
  it('is true when stopped, done, or failed to start (no live terminal)', () => {
    expect(isSessionTerminal('stopped')).toBe(true);
    expect(isSessionTerminal('done')).toBe(true);
    expect(isSessionTerminal('error')).toBe(true);
    // idle/running/waiting/unknown all still have a live terminal.
    expect(isSessionTerminal('idle')).toBe(false);
    expect(isSessionTerminal('running')).toBe(false);
    expect(isSessionTerminal('waiting')).toBe(false);
    expect(isSessionTerminal('unknown')).toBe(false);
  });
});

// A create that failed. The row stands for a session that never existed, so
// there is nothing to drive and nothing that will change on its own — which is
// what keeps the send box and the state overlay off it.
describe('the error state', () => {
  it('is not active — there was never a runtime to stop or type into', () => {
    expect(isSessionActive('error')).toBe(false);
  });

  it('is terminal, mirroring the daemon: it holds no concurrency slot', () => {
    expect(isSessionTerminal('error')).toBe(true);
  });

  it('is never overlaid with done, seen or not', () => {
    expect(displaySessionState('error', false)).toBe('error');
    expect(displaySessionState('error', true)).toBe('error');
  });
});

// `starting` is the placeholder state: the provider is still bringing the session
// up, so it is neither something to drive nor something that has ended. It sits
// outside both predicates, and that is what keeps Stop/Start/Remove and the send
// box off a row with no runtime behind it.
describe('the starting state', () => {
  it('is not active — there is nothing live to stop or type into', () => {
    expect(isSessionActive('starting')).toBe(false);
  });

  it('is not terminal either — it has not ended, it has not begun', () => {
    expect(isSessionTerminal('starting')).toBe(false);
  });

  it('passes through displaySessionState untouched (the done overlay is idle-only)', () => {
    expect(displaySessionState('starting', false)).toBe('starting');
    expect(displaySessionState('starting', true)).toBe('starting');
  });
});

describe('sessionCapabilities', () => {
  it('defaults to a live session with no take-over and no warning', () => {
    expect(sessionCapabilities({})).toEqual({ live: true, takeOver: false });
  });

  it('keeps what the session declares', () => {
    const capabilities = {
      live: false,
      takeOver: true,
      stopWarning: 'Careful.',
    };
    expect(sessionCapabilities({ capabilities })).toEqual(capabilities);
  });
});
