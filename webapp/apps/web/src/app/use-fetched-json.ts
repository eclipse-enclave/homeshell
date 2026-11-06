import { useEffect, useState } from 'react';

export interface FetchedJson<T> {
  /** Null until the body arrives, and when it does not. */
  value: T | null;
  /** Whether the daemon could not be reached, or answered with no `T`. */
  failed: boolean;
}

/**
 * Fetches a JSON body from the daemon once on mount, keeping it if `isValue`
 * accepts it. Pass an `isValue` that keeps its identity across renders.
 */
export function useFetchedJson<T>(
  url: string,
  isValue: (body: unknown) => body is T,
): FetchedJson<T> {
  const [fetched, setFetched] = useState<FetchedJson<T>>({
    value: null,
    failed: false,
  });

  useEffect(() => {
    let cancelled = false;
    const settle = (result: FetchedJson<T>) => {
      if (!cancelled) setFetched(result);
    };
    fetch(url)
      .then((res) => (res.ok ? res.json() : null))
      .then((body) =>
        settle(
          isValue(body)
            ? { value: body, failed: false }
            : { value: null, failed: true },
        ),
      )
      .catch(() => settle({ value: null, failed: true }));
    return () => {
      cancelled = true;
    };
  }, [url, isValue]);

  return fetched;
}
