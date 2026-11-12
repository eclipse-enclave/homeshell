import { dotStateLabel } from './labels';
import type { Session, SessionState } from './types';
import { isSessionEnded } from './types';

const COUNT_ORDER = [
  'waiting',
  'done',
  'error',
  'running',
  'idle',
  'ended',
  'unknown',
] as const;

export type CountKind = (typeof COUNT_ORDER)[number];

/** How many of a workstream's sessions are of one kind. */
export interface SessionCount {
  kind: CountKind;
  count: number;
}

/** Epoch ms of an end time, or undefined when it is missing or unparseable. */
function parseEndedAt(endedAt: string | undefined): number | undefined {
  const time = endedAt ? Date.parse(endedAt) : Number.NaN;
  return Number.isNaN(time) ? undefined : time;
}

/** Splits into live sessions (input order) and ended ones, newest end first, undated last. */
export function splitSessions(sessions: readonly Session[]): {
  live: Session[];
  ended: Session[];
} {
  const live = sessions.filter((s) => !isSessionEnded(s));
  // Array.prototype.sort is stable, so equal end times (and undated ones) keep input order.
  const ended = sessions
    .filter(isSessionEnded)
    .map((session) => ({ session, time: parseEndedAt(session.endedAt) }))
    .sort((a, b) => {
      if (a.time === b.time) return 0;
      if (a.time === undefined) return 1;
      if (b.time === undefined) return -1;
      return b.time - a.time;
    })
    .map(({ session }) => session);
  return { live, ended };
}

/** Per-kind session counts in display order; ended sessions count as `ended` whatever they show. */
export function sessionCounts(
  sessions: readonly Session[],
  displayState: (s: Session) => SessionState,
): SessionCount[] {
  const totals = new Map<CountKind, number>();
  for (const s of sessions) {
    const kind = isSessionEnded(s) ? 'ended' : countKind(displayState(s));
    totals.set(kind, (totals.get(kind) ?? 0) + 1);
  }
  return COUNT_ORDER.filter((kind) => totals.has(kind)).map((kind) => ({
    kind,
    count: totals.get(kind) as number,
  }));
}

function countKind(state: SessionState): CountKind {
  switch (state) {
    case 'starting':
      return 'running';
    case 'stopped':
      return 'ended';
    default:
      return state;
  }
}

/** E.g. "1 waiting for input, 1 running, 5 ended"; empty for no counts. */
export function countsSummary(counts: readonly SessionCount[]): string {
  return counts
    .map((c) => `${c.count} ${dotStateLabel(c.kind).toLowerCase()}`)
    .join(', ');
}

/** Relative end time, or undefined when the end time is missing or unparseable. */
export function endedAgo(
  endedAt: string | undefined,
  now: Date,
): string | undefined {
  const time = parseEndedAt(endedAt);
  if (time === undefined) return undefined;
  const minutes = Math.floor((now.getTime() - time) / 60_000);
  if (minutes < 1) return 'just now';
  if (minutes < 60) return `${minutes} min ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours} h ago`;
  if (hours < 48) return 'yesterday';
  return `${Math.floor(hours / 24)} days ago`;
}
