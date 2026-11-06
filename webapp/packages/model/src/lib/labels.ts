import type { EffectiveState, SessionState } from './types';

const SESSION_STATE_LABELS: Record<SessionState, string> = {
  idle: 'Idle',
  running: 'Running',
  waiting: 'Waiting for input',
  starting: 'Starting',
  unknown: 'Unknown',
  stopped: 'Stopped',
  done: 'Done',
  error: 'Error',
};

/** Human-readable label for a session state, for badges and tooltips. */
export function stateLabel(state: SessionState): string {
  return SESSION_STATE_LABELS[state];
}

const EFFECTIVE_STATE_LABELS: Record<EffectiveState, string> = {
  idle: 'Idle',
  running: 'Running',
  waiting: 'Waiting for input',
  done: 'Done',
  stopped: 'Stopped',
  stopping: 'Stopping',
  starting: 'Starting',
  creating: 'Creating',
  error: 'Error',
  unknown: 'Unknown',
};

/** Human-readable label for an effective state (runtime + session combined). */
export function effectiveStateLabel(state: EffectiveState): string {
  return EFFECTIVE_STATE_LABELS[state] ?? state;
}
