import { networkInterfaces } from 'node:os';
import type { Page, Request } from '@playwright/test';

/**
 * Retrying a page load that a host network change broke.
 *
 * Any host link or address change (a Docker network coming or going, an IPv6
 * address rotation) makes Chromium abort its in-flight requests with
 * `net::ERR_NETWORK_CHANGED`, 127.0.0.1 included. If that hits while Vite
 * serves the app's modules, an import fails and the app never mounts; now and
 * then a request stalls instead and the load never finishes. Only page loads
 * are covered: a mid-test fetch or EventSource abort still fails the test.
 */

/** Extra attempts per navigation; after that the last result or error stands. */
export const MAX_NETWORK_CHANGED_RETRIES = 4;

/** Pause step: the pause before extra attempt n is n steps, plus up to one step at random. */
export const RETRY_PAUSE_STEP_MS = 500;

/**
 * The pause before extra attempt `attempt` (1-based). Why growing and jittered:
 * a fixed pause can fall into step with a network that changes at a steady
 * rate, and then every attempt lands on the next change.
 */
export function retryPauseMs(attempt: number, random: () => number = Math.random): number {
  return RETRY_PAUSE_STEP_MS * (attempt + random());
}

/**
 * Timeout of one attempt when the caller set none. Why: a load stalled by a
 * network change would otherwise use up the whole test timeout.
 */
export const ATTEMPT_TIMEOUT_MS = 10_000;

/** How often the host's interfaces are compared during an attempt. */
export const INTERFACE_SAMPLE_MS = 250;

/** Annotation type each retry adds to the test. */
export const NETWORK_CHANGED_ANNOTATION = 'network-changed-retry';

const NETWORK_CHANGED = 'net::ERR_NETWORK_CHANGED';

/** The part of a failed request the retry decision reads. */
export type FailedRequest = Pick<Request, 'resourceType' | 'failure' | 'url'>;

function failedWithNetworkChange(request: FailedRequest): boolean {
  return request.failure()?.errorText.includes(NETWORK_CHANGED) ?? false;
}

/** True when a network change aborted a request the page needs to mount: the document or a script. */
export function isNetworkChangedLoad(request: FailedRequest): boolean {
  const type = request.resourceType();
  return (type === 'document' || type === 'script') && failedWithNetworkChange(request);
}

/** The host's interface names and addresses, to tell that the network changed. */
export function interfaceFingerprint(): string {
  return Object.entries(networkInterfaces())
    .map(([name, addrs]) => `${name}=${(addrs ?? []).map((a) => a.address).join(',')}`)
    .sort()
    .join(';');
}

/** The part of a page the wrapper listens on. */
export type RequestFailedSource = Pick<Page, 'on' | 'off'>;

export interface RetryOptions {
  /** Gets one description per retry. */
  annotate: (description: string) => void;
  fingerprint?: () => string;
  pause?: (attempt: number) => number;
  sampleMs?: number;
}

function isTimeout(error: unknown): boolean {
  return error instanceof Error && error.name === 'TimeoutError';
}

/**
 * Runs `navigate`, and runs it again (at most {@link MAX_NETWORK_CHANGED_RETRIES}
 * more times, {@link retryPauseMs} apart) when a network change broke it:
 * the document or a script was aborted, or the attempt timed out while the
 * host's interfaces changed or some request failed with a network change.
 * `navigate` gets the timeout to use when its caller set none.
 */
export async function retryOnNetworkChange<T>(
  page: RequestFailedSource,
  navigate: (timeout: number) => Promise<T>,
  {
    annotate,
    fingerprint = interfaceFingerprint,
    pause = retryPauseMs,
    sampleMs = INTERFACE_SAMPLE_MS,
  }: RetryOptions,
): Promise<T> {
  for (let attempt = 1; ; attempt++) {
    const aborted: string[] = [];
    let changed = false;
    const onFailed = (request: Request) => {
      if (isNetworkChangedLoad(request)) aborted.push(request.url());
      else if (failedWithNetworkChange(request)) changed = true;
    };
    let last = fingerprint();
    const sampler = setInterval(() => {
      const now = fingerprint();
      if (now !== last) changed = true;
      last = now;
    }, sampleMs);
    page.on('requestfailed', onFailed);
    let result: T | undefined;
    let error: unknown;
    let failed = false;
    try {
      result = await navigate(ATTEMPT_TIMEOUT_MS);
    } catch (e) {
      error = e;
      failed = true;
    } finally {
      page.off('requestfailed', onFailed);
      clearInterval(sampler);
    }
    if (fingerprint() !== last) changed = true;
    const stalled = failed && isTimeout(error) && changed;
    if ((aborted.length === 0 && !stalled) || attempt > MAX_NETWORK_CHANGED_RETRIES) {
      if (failed) throw error;
      return result as T;
    }
    annotate(
      aborted.length > 0
        ? `attempt ${attempt} aborted by ${NETWORK_CHANGED}: ${aborted.join(', ')}`
        : `attempt ${attempt} timed out during a network change`,
    );
    await new Promise((resolve) => setTimeout(resolve, pause(attempt)));
  }
}
