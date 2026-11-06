/**
 * The API token the e2e daemon requires on every /api request (Bearer header or
 * hs_api_token cookie; see docs/API.md, "Authentication"). playwright.config.ts pins
 * HOMESHELL_API_TOKEN for the daemon and exposes it on process.env, so the
 * `request` fixture attaches it as a default header. Specs that bypass that
 * fixture — a raw fetch() reading an SSE stream, or a raw socket — attach it
 * with authHeader().
 */
export const API_TOKEN = process.env.HOMESHELL_API_TOKEN ?? '';

/** Returns an Authorization header carrying the API token, or {} when unset. */
export function authHeader(): Record<string, string> {
  return API_TOKEN ? { Authorization: `Bearer ${API_TOKEN}` } : {};
}
