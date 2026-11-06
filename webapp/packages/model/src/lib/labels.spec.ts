import { effectiveStateLabel, stateLabel } from './labels';
import { EFFECTIVE_STATES, SESSION_STATES } from './types';

describe('stateLabel', () => {
  it('has a human-readable label for every session state', () => {
    for (const state of SESSION_STATES) {
      expect(stateLabel(state)).toBeTruthy();
    }
  });

  it('labels waiting as needing input', () => {
    expect(stateLabel('waiting')).toBe('Waiting for input');
  });
});

describe('effectiveStateLabel', () => {
  it('has a human-readable label for every effective state', () => {
    for (const state of EFFECTIVE_STATES) {
      expect(effectiveStateLabel(state)).toBeTruthy();
    }
  });

  it('labels runtime states distinctly', () => {
    expect(effectiveStateLabel('stopped')).toBe('Stopped');
    expect(effectiveStateLabel('error')).toBe('Error');
    expect(effectiveStateLabel('creating')).toBe('Creating');
    expect(effectiveStateLabel('starting')).toBe('Starting');
    expect(effectiveStateLabel('stopping')).toBe('Stopping');
    expect(effectiveStateLabel('unknown')).toBe('Unknown');
  });

  it('labels session states the same as stateLabel', () => {
    for (const state of SESSION_STATES) {
      expect(effectiveStateLabel(state)).toBe(stateLabel(state));
    }
  });
});

describe('the starting state label', () => {
  it('reads Starting, matching the workstream runtime state of the same name', () => {
    expect(stateLabel('starting')).toBe('Starting');
    expect(effectiveStateLabel('starting')).toBe('Starting');
  });
});
