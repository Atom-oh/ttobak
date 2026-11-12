// Korean copy for skill package settings (ADR-003, local intake). No imports,
// so node:test can load this file in isolation. Rule names match
// backend/internal/service/skill_package_zip.go; fixed copy keys are in
// docs/superpowers/plans/2026-11-12-one-click-intake-setup-ui-copy.json.

export const SKILL_PACKAGE_MAX_BYTES = 20 * 1024 * 1024;

export const SKILL_PACKAGE_COPY = {
  sectionTitle: '스킬 패키지',
  none: '없음',
  sourceOrg: '조직',
  sourcePersonal: '개인',
  uploadPersonal: '개인 zip 올리기',
  deletePersonal: '개인 패키지 삭제',
  uploadOrg: '조직 zip 올리기',
  rollback: '되돌리기',
} as const;

const RULE_TEXT: Record<string, string> = {
  'not-zip': 'zip 파일로 읽을 수 없어요',
  'zip-too-large': 'zip은 20MB 이하만 올릴 수 있어요',
  'too-many-entries': 'zip 안의 파일이 500개를 넘어요',
  'uncompressed-too-large': '압축을 풀면 50MB를 넘어요',
  'unsafe-path': 'zip 안에 허용되지 않는 경로가 있어요',
  'not-regular-file': 'zip 안에 링크나 특수 파일이 있어요',
  'top-level': 'zip 안에는 최상위 폴더가 하나만 있어야 해요',
  'no-skills': 'skills 폴더에 스킬이 없어요',
  'skill-missing-md': '이 스킬에 SKILL.md가 없어요',
  'bad-skill-name': '스킬 폴더 이름은 영문 소문자, 숫자, -만 쓸 수 있어요',
  'bad-server-name': 'MCP 서버 이름은 영문 소문자, 숫자, -만 쓸 수 있어요',
  'bad-registry': 'toolbox 레지스트리는 s3:// 주소여야 해요',
  'bad-http-url': 'HTTP MCP 서버 주소는 https://여야 해요',
  'report-skill-layout': 'sa-performance-report 스킬에 필요한 파일이 없어요',
  'bad-manifest': 'ttobak-manifest.json 형식이 올바르지 않아요',
  'bad-server-list': '설치 스크립트의 서버 목록을 읽을 수 없어요',
};

const CODE_TEXT: Record<string, string> = {
  CONFLICT: '다른 곳에서 패키지가 바뀌었어요. 새로고침한 뒤 다시 시도하세요.',
  FORBIDDEN: '조직 패키지는 관리자만 올릴 수 있어요.',
  NOT_FOUND: '패키지를 찾지 못했어요. 새로고침한 뒤 다시 시도하세요.',
  BAD_REQUEST: '요청이 올바르지 않아요. 다시 시도하세요.',
  UPLOAD_FAILED: 'zip을 올리지 못했어요. 다시 시도하세요.',
};

const FALLBACK = '패키지를 처리하지 못했어요. 잠시 후 다시 시도하세요.';

/** Korean text for an API error. Only the rule's detail (a zip entry or
 * skill name) is shown from the server message; other text is fixed copy. */
export function packageErrorText(code: string, message: string): string {
  if (code === 'LOCAL_FILE') return message;
  if (code === 'INVALID_PACKAGE') {
    const separator = message.indexOf(':');
    const rule = separator < 0 ? message : message.slice(0, separator);
    const detail = separator < 0 ? '' : message.slice(separator + 1).trim().slice(0, 120);
    const text = Object.prototype.hasOwnProperty.call(RULE_TEXT, rule) ? RULE_TEXT[rule] : null;
    if (text) return detail ? `${text}: ${detail}` : `${text}.`;
  }
  return Object.prototype.hasOwnProperty.call(CODE_TEXT, code) ? CODE_TEXT[code] : FALLBACK;
}

/** Checked before any request: a nonempty .zip of at most 20 MiB. */
export function localFileError(file: { size: number; name: string }): string | null {
  if (!file.name.toLowerCase().endsWith('.zip')) return 'zip 파일만 올릴 수 있어요.';
  if (file.size === 0) return 'zip 파일이 비어 있어요.';
  if (file.size > SKILL_PACKAGE_MAX_BYTES) return 'zip은 20MB 이하만 올릴 수 있어요.';
  return null;
}
