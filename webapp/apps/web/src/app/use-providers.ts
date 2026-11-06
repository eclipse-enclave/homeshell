import type { Provider } from '@homeshell/model';
import { useFetchedJson } from './use-fetched-json';

const NO_PROVIDERS: Provider[] = [];

function isProviders(body: unknown): body is Provider[] {
  return Array.isArray(body);
}

/** Fetches the provider catalogue from `GET /api/providers` once on mount. */
export function useProviders(): Provider[] {
  return useFetchedJson('/api/providers', isProviders).value ?? NO_PROVIDERS;
}
