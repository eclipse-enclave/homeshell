import { dotStateLabel, stateLabel } from './labels';
import { EFFECTIVE_STATES, SESSION_STATES } from './types';

describe('stateLabel', () => {
  it('has a human-readable label for every session state', () => {
    for (const state of SESSION_STATES) {
      expect(stateLabel(state)).toBeTruthy();
    }
  });
});

describe('dotStateLabel', () => {
  it('has a human-readable label for every effective state', () => {
    for (const state of EFFECTIVE_STATES) {
      expect(dotStateLabel(state)).toBeTruthy();
    }
  });

  it('labels session states the same as stateLabel', () => {
    for (const state of SESSION_STATES) {
      expect(dotStateLabel(state)).toBe(stateLabel(state));
    }
  });

  it('labels an ended session Ended', () => {
    expect(dotStateLabel('ended')).toBe('Ended');
  });
});
