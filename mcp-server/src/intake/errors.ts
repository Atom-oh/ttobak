// Error classes for local intake. Messages carry codes, counts and server names
// only: never host configuration values, provider output or event content.

export const INTAKE_CODES = [
  'INVALID_ARGUMENT',
  'HOST_CONFIG_INVALID',
  'SERVER_NOT_CONFIGURED',
  'SERVER_NOT_SUPPORTED',
  'SERVER_START_FAILED',
  'SOURCE_TIMEOUT',
  'SOURCE_ERROR',
  'AUTH_EXPIRED_MIDWAY',
  'SCHEMA_CHANGED',
  'PARSE_FAILED',
  'SENSITIVITY_UNKNOWN',
  'NOT_SELECTED',
  'LOCKED',
  'STORE_INVALID',
  'TTOBAK_LOGIN_REQUIRED',
  'TTOBAK_CONFLICT',
  'TTOBAK_REJECTED',
  'TTOBAK_UNAVAILABLE',
] as const;

export type IntakeCode = typeof INTAKE_CODES[number];

export class IntakeError extends Error {
  constructor(readonly code: IntakeCode, detail = '') {
    super(detail ? `${code}: ${detail}` : code);
  }
}

export function intakeCode(error: unknown): IntakeCode {
  if (error instanceof IntakeError) return error.code;
  const message = error instanceof Error ? error.message : '';
  if (message.startsWith('TTOBAK_LOGIN_REQUIRED')) return 'TTOBAK_LOGIN_REQUIRED';
  return 'TTOBAK_UNAVAILABLE';
}
