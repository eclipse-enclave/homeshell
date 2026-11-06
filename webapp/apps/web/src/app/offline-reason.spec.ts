import { offlineReason } from './offline-reason';

describe('offlineReason', () => {
  it('blocks nothing while the daemon is connected', () => {
    expect(offlineReason('connected')).toBeUndefined();
  });

  // Not just 'offline': until the first snapshot arrives the daemon has not
  // answered, and a request fired into that window fails the same way.
  it('blocks while offline and while still connecting', () => {
    expect(offlineReason('offline')).toBeTruthy();
    expect(offlineReason('connecting')).toBeTruthy();
  });

  it('says which of the two it is, so the tooltip is not a guess', () => {
    expect(offlineReason('connecting')).not.toBe(offlineReason('offline'));
    expect(offlineReason('connecting')).toMatch(/connecting/i);
    expect(offlineReason('offline')).toMatch(/offline/i);
  });
});
