import { EventEmitter } from 'node:events';
import type { Request } from '@playwright/test';
import { describe, expect, it } from 'vitest';
import {
  ATTEMPT_TIMEOUT_MS,
  MAX_NETWORK_CHANGED_RETRIES,
  NETWORK_CHANGED_ANNOTATION,
  SLOW_LOAD_ANNOTATION,
  interfaceFingerprint,
  isNetworkChangedLoad,
  retryOnNetworkChange,
  retryPauseMs,
  RETRY_PAUSE_STEP_MS,
  type RequestFailedSource,
} from './page-load-retry';

function failedRequest(resourceType: string, errorText: string | null, url = 'http://x/') {
  return {
    resourceType: () => resourceType,
    failure: () => (errorText === null ? null : { errorText }),
    url: () => url,
  } as unknown as Request;
}

const changed = 'net::ERR_NETWORK_CHANGED';

describe('isNetworkChangedLoad', () => {
  it.each([
    ['document', changed, true],
    ['script', changed, true],
    ['fetch', changed, false],
    ['eventsource', changed, false],
    ['stylesheet', changed, false],
    ['script', 'net::ERR_CONNECTION_REFUSED', false],
    ['document', 'net::ERR_ABORTED', false],
    ['script', null, false],
  ])('%s failing with %s → %s', (type, errorText, want) => {
    expect(isNetworkChangedLoad(failedRequest(type, errorText))).toBe(want);
  });
});

describe('interfaceFingerprint', () => {
  it('is stable while nothing changes', () => {
    expect(interfaceFingerprint()).toBe(interfaceFingerprint());
  });
});

describe('retryPauseMs', () => {
  it.each([
    [1, 0, 1],
    [1, 0.999, 1.999],
    [4, 0, 4],
    [4, 0.5, 4.5],
  ])('attempt %i with random %f → %f steps', (attempt, random, steps) => {
    expect(retryPauseMs(attempt, () => random)).toBeCloseTo(steps * RETRY_PAUSE_STEP_MS);
  });
});

class TimeoutError extends Error {
  override name = 'TimeoutError';
}

type Step = {
  /** Requests that fail during this attempt. */
  failures?: Request[];
  /** What the attempt throws, if anything. */
  throws?: 'aborted' | 'timeout' | 'other';
  /** The host's interfaces change during this attempt. */
  interfacesChange?: boolean;
};

/** A page whose navigations follow `steps`, one per attempt (the last one repeats). */
function fakePage(steps: Step[]) {
  const emitter = new EventEmitter();
  const page = emitter as unknown as RequestFailedSource;
  const timeouts: number[] = [];
  let calls = 0;
  let generation = 0;
  const notes: string[] = [];
  const navigate = async (timeout: number) => {
    const step = steps[Math.min(calls, steps.length - 1)];
    calls++;
    timeouts.push(timeout);
    if (step.interfacesChange) generation++;
    for (const r of step.failures ?? []) emitter.emit('requestfailed', r);
    if (step.throws === 'aborted') throw new Error(`page.goto: ${changed}`);
    if (step.throws === 'timeout') throw new TimeoutError('page.goto: Timeout exceeded');
    if (step.throws === 'other') throw new Error('page.goto: net::ERR_CONNECTION_REFUSED');
    return `response ${calls}`;
  };
  const opts = {
    annotate: (type: string, description: string) =>
      notes.push(type === NETWORK_CHANGED_ANNOTATION ? description : `${type}: ${description}`),
    fingerprint: () => `gen${generation}`,
    pause: () => 0,
    sampleMs: 5,
  };
  const run = () => retryOnNetworkChange(page, navigate, opts);
  return {
    run,
    notes,
    timeouts,
    calls: () => calls,
    listeners: () => emitter.listenerCount('requestfailed'),
  };
}

describe('retryOnNetworkChange', () => {
  it('navigates once when nothing failed', async () => {
    const f = fakePage([{}]);
    await expect(f.run()).resolves.toBe('response 1');
    expect(f.calls()).toBe(1);
    expect(f.notes).toEqual([]);
    expect(f.listeners()).toBe(0);
  });

  it('hands navigate the per-attempt timeout', async () => {
    const f = fakePage([{}]);
    await f.run();
    expect(f.timeouts).toEqual([ATTEMPT_TIMEOUT_MS]);
  });

  it('navigates again after a script was aborted by a network change', async () => {
    const f = fakePage([
      { failures: [failedRequest('script', changed, 'http://x/src/main.tsx')] },
      {},
    ]);
    await expect(f.run()).resolves.toBe('response 2');
    expect(f.calls()).toBe(2);
    expect(f.notes).toHaveLength(1);
    expect(f.notes[0]).toContain('http://x/src/main.tsx');
    expect(f.listeners()).toBe(0);
  });

  it('navigates again when the aborted document made the navigation throw', async () => {
    const f = fakePage([{ failures: [failedRequest('document', changed)], throws: 'aborted' }, {}]);
    await expect(f.run()).resolves.toBe('response 2');
  });

  it('navigates again after a timeout while the interfaces changed', async () => {
    const f = fakePage([{ throws: 'timeout', interfacesChange: true }, {}]);
    await expect(f.run()).resolves.toBe('response 2');
    expect(f.notes).toEqual(['attempt 1 timed out during a network change']);
  });

  it('navigates again after a timeout while a fetch failed with a network change', async () => {
    const f = fakePage([{ throws: 'timeout', failures: [failedRequest('fetch', changed)] }, {}]);
    await expect(f.run()).resolves.toBe('response 2');
  });

  it('loads once more without the cap after a timeout without a network change', async () => {
    const f = fakePage([{ throws: 'timeout' }, {}]);
    await expect(f.run()).resolves.toBe('response 2');
    expect(f.timeouts).toEqual([ATTEMPT_TIMEOUT_MS, 0]);
    expect(f.notes).toEqual([
      `${SLOW_LOAD_ANNOTATION}: attempt 1 timed out after ${ATTEMPT_TIMEOUT_MS / 1000} s ` +
        'with no network change; loading again without the cap',
    ]);
  });

  it('rethrows when the uncapped load also times out', async () => {
    const f = fakePage([{ throws: 'timeout' }]);
    await expect(f.run()).rejects.toThrow('Timeout exceeded');
    expect(f.calls()).toBe(2);
    expect(f.listeners()).toBe(0);
  });

  it('goes back to the cap after the uncapped load hit a network change', async () => {
    const f = fakePage([
      { throws: 'timeout' },
      { failures: [failedRequest('script', changed)] },
      {},
    ]);
    await expect(f.run()).resolves.toBe('response 3');
    expect(f.timeouts).toEqual([ATTEMPT_TIMEOUT_MS, 0, ATTEMPT_TIMEOUT_MS]);
  });

  it('keeps the cap for a stall during a network change', async () => {
    const f = fakePage([{ throws: 'timeout', interfacesChange: true }, {}]);
    await f.run();
    expect(f.timeouts).toEqual([ATTEMPT_TIMEOUT_MS, ATTEMPT_TIMEOUT_MS]);
  });

  it('does not retry a network change that broke nothing', async () => {
    const f = fakePage([
      { interfacesChange: true, failures: [failedRequest('fetch', changed)] },
    ]);
    await expect(f.run()).resolves.toBe('response 1');
    expect(f.calls()).toBe(1);
  });

  it('does not retry other failures', async () => {
    const f = fakePage([{ failures: [failedRequest('script', 'net::ERR_FAILED')] }]);
    await expect(f.run()).resolves.toBe('response 1');
    expect(f.calls()).toBe(1);
  });

  it('does not retry a navigation that threw for another reason', async () => {
    const f = fakePage([{ throws: 'other', interfacesChange: true }]);
    await expect(f.run()).rejects.toThrow('ERR_CONNECTION_REFUSED');
    expect(f.calls()).toBe(1);
  });

  it('gives up after the retry budget and surfaces the last result', async () => {
    const f = fakePage([{ failures: [failedRequest('script', changed)] }]);
    await expect(f.run()).resolves.toBe(`response ${MAX_NETWORK_CHANGED_RETRIES + 1}`);
    expect(f.calls()).toBe(MAX_NETWORK_CHANGED_RETRIES + 1);
    expect(f.notes).toHaveLength(MAX_NETWORK_CHANGED_RETRIES);
    expect(f.listeners()).toBe(0);
  });

  it('gives up after the retry budget and rethrows the last error', async () => {
    const f = fakePage([{ throws: 'timeout', interfacesChange: true }]);
    await expect(f.run()).rejects.toThrow('Timeout exceeded');
    expect(f.calls()).toBe(MAX_NETWORK_CHANGED_RETRIES + 1);
    expect(f.listeners()).toBe(0);
  });
});
