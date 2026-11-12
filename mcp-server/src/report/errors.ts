// Result codes of `ttobak-mcp report run`. Messages carry codes and field names
// only: never model output, tool input or host configuration values.

import { IntakeError } from '../intake/errors.js';

export const REPORT_CODES = [
  'INVALID_ARGUMENT',
  'CLAUDE_CLI_MISSING',
  'CLAUDE_CLI_AUTH',
  'CLAUDE_CLI_BEDROCK',
  'SKILL_MISSING',
  'SKILL_OUTDATED',
  'REPORT_INVALID',
  'REPORT_NO_SFDC',
  'REPORT_TIMEOUT',
  'REPORT_CANCELLED',
  'REPORT_PERIOD_EXPIRED',
  'REPORT_SANDBOX_UNAVAILABLE',
  'RUN_IN_PROGRESS',
  'TTOBAK_LOGIN_REQUIRED',
  'TTOBAK_UNAVAILABLE',
] as const;

export type ReportCode = typeof REPORT_CODES[number];

export class ReportError extends Error {
  constructor(readonly code: ReportCode, detail = '') {
    super(detail ? `${code}: ${detail}` : code);
  }
}

/** Maps any failure to a report code; shared intake codes keep their meaning. */
export function reportCode(error: unknown): ReportCode {
  if (error instanceof ReportError) return error.code;
  if (error instanceof IntakeError) {
    if (error.code === 'LOCKED' || error.code === 'RUN_IN_PROGRESS') return 'RUN_IN_PROGRESS';
    if (error.code === 'TTOBAK_LOGIN_REQUIRED') return 'TTOBAK_LOGIN_REQUIRED';
    if (error.code === 'INVALID_ARGUMENT') return 'INVALID_ARGUMENT';
    return 'TTOBAK_UNAVAILABLE';
  }
  const message = error instanceof Error ? error.message : '';
  if (message.startsWith('TTOBAK_LOGIN_REQUIRED')) return 'TTOBAK_LOGIN_REQUIRED';
  return 'TTOBAK_UNAVAILABLE';
}
