import type { DotState, SessionState } from './types';

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

const DOT_STATE_LABELS: Record<DotState, string> = {
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
  ended: 'Ended',
};

/** Human-readable label for a status dot: an effective state, or a session that ended. */
export function dotStateLabel(state: DotState): string {
  return DOT_STATE_LABELS[state] ?? state;
}
