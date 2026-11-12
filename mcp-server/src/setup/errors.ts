// Result codes of `ttobak-mcp setup`. Messages carry codes, rule names, skill
// and server names only: never file contents or command output.

export const SETUP_CODES = [
  'INVALID_ARGUMENT',
  'PACKAGE_INVALID',
  'PACKAGE_DOWNLOAD',
  'PACKAGE_INTEGRITY',
  'SKILL_PATH_UNSAFE',
  'SKILL_WRITE_FAILED',
  'TOOLBOX_MISSING',
  'AUTH_EXPIRED_MIDWAY',
  'INSTALL_FAILED',
  'RUN_IN_PROGRESS',
  'TTOBAK_LOGIN_REQUIRED',
  'TTOBAK_UNAVAILABLE',
] as const;

export type SetupCode = typeof SETUP_CODES[number];

export class SetupError extends Error {
  constructor(readonly code: SetupCode, detail = '') {
    super(detail ? `${code}: ${detail}` : code);
  }
}
