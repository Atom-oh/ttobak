// Admin team sync: pure helpers, payload types and Korean UI copy.
// Kept free of React and `@/` imports so node:test can transpile it.

export const TEAM_SYNC_BUTTON_LABEL = '팀 동기화';
export const TEAM_COLUMN_LABEL = '팀';
export const TEAM_EMPTY = '—';
export const TEAM_SYNC_BUDGET_NOTE = 'cc-on-bedrock 부서별 예산은 이 값을 따릅니다.';
export const TEAM_SYNC_RUNNING_LABEL = '실행 중…';
export const TEAM_SYNC_CANCEL_LABEL = '취소';
export const TEAM_SYNC_CLOSE_LABEL = '닫기';
export const TEAM_SYNC_NOT_FOUND_LABEL = '디렉터리에서 찾지 못한 별칭';
export const TEAM_SYNC_UPDATED_LABEL = '업데이트됨';
export const TEAM_SYNC_CONFLICTS_LABEL = '충돌(그 사이 값이 바뀜)';
export const TEAM_SYNC_REJECTED_LABEL = '거부됨';
export const TEAM_SYNC_NO_CHANGES_LABEL = '변경할 항목이 없습니다.';

export const TEAM_SYNC_APPLY_PARTIAL_NOTE = '일부 변경이 이미 적용되었을 수 있습니다. 위 목록을 새로 불러왔습니다.';

export const teamSyncApplyLabel = (n: number): string => `${n}건 변경 적용`;
export const teamSyncUnchangedLabel = (n: number): string => `변경 없음 ${n}명`;

export const TEAM_SYNC_MESSAGES: Record<string, string> = {
  AUTH_EXPIRED_MIDWAY: '회사 로그인이 만료되었습니다. 터미널에서 mwinit을 실행한 뒤 다시 시도하세요.',
  NOT_ADMIN: '관리자로 로그인한 Mac 앱에서만 실행할 수 있습니다.',
  PREVIEW_EXPIRED: '미리보기가 만료되었습니다. 팀 동기화를 다시 눌러 주세요.',
  ADAPTER_OUTDATED: '로컬 어댑터가 오래되었습니다. 업데이트한 뒤 다시 시도하세요.',
  DIRECTORY_UNAVAILABLE: '사내 디렉터리(builder-mcp)에 연결할 수 없습니다. 설치 여부와 회사 네트워크 연결을 확인하세요.',
  RUN_IN_PROGRESS: '이미 실행 중입니다.',
};

export const teamSyncFallbackMessage = (code: string): string => `팀 동기화에 실패했습니다 (${code})`;

export function teamSyncMessage(code: string | null): string {
  if (!code) return '팀 동기화에 실패했습니다.';
  return Object.prototype.hasOwnProperty.call(TEAM_SYNC_MESSAGES, code)
    ? TEAM_SYNC_MESSAGES[code]
    : teamSyncFallbackMessage(code);
}

/** Cosmetic gate only; the API's RequireAdmin is the authorization. */
export function teamSyncVisible(isAdmin: boolean, hasCommand: boolean): boolean {
  return isAdmin && hasCommand;
}

export function canApply(preview: { changes: unknown[] } | null): boolean {
  return !!preview && Array.isArray(preview.changes) && preview.changes.length > 0;
}

export interface TeamSyncChange { userId: string; alias: string; from: string; to: string }
export interface TeamSyncPreview {
  ok: true;
  previewId: string;
  changes: TeamSyncChange[];
  unchanged: number;
  notFound: string[];
  skippedNonDirectory: number;
  truncated: boolean;
}
export interface TeamSyncRejection { userId: string; code: string }
export interface TeamSyncApplyResult {
  ok: true;
  updated: string[];
  unchanged: number;
  conflicts: string[];
  rejected: TeamSyncRejection[];
}
export interface TeamSyncFinished {
  mode: 'preview' | 'apply';
  exitCode: number | null;
  result: unknown;
  code: string | null;
}
export interface TeamSyncStateView {
  running: boolean;
  lastResult: TeamSyncFinished | null;
  progress: string[];
}

export type TeamSyncPhase = 'idle' | 'previewing' | 'preview' | 'applying' | 'done' | 'error';

export function isPreviewResult(value: unknown): value is TeamSyncPreview {
  const v = value as TeamSyncPreview | null;
  return !!v && v.ok === true && typeof v.previewId === 'string' && Array.isArray(v.changes);
}

export function isApplyResult(value: unknown): value is TeamSyncApplyResult {
  const v = value as TeamSyncApplyResult | null;
  return !!v && v.ok === true && Array.isArray(v.updated) && Array.isArray(v.conflicts);
}

/** Failure code of a finished run, or null when it succeeded. */
export function finishedFailureCode(finished: TeamSyncFinished): string | null {
  if (finished.code) return finished.code;
  const result = finished.result as { ok?: unknown; code?: unknown } | null;
  if (result && result.ok === false) return typeof result.code === 'string' ? result.code : 'UNKNOWN';
  if (!result) return 'UNKNOWN';
  return null;
}

/** An apply may have written values server-side however the run ended. */
export const shouldRefetchAfterFinish = (mode: string): boolean => mode === 'apply';

/** Only a preview is safe to cancel; a started apply keeps writing. */
export const canCancel = (phase: string): boolean => phase === 'previewing';

export const isCancelled = (code: string | null): boolean => code === 'TEAM_SYNC_CANCELLED';

export const teamValue = (value: string | null | undefined): string => (value ? value : TEAM_EMPTY);

/** Maps userIds to aliases via the preview; unknown ids fall back to the id. */
export function aliasLookup(preview: TeamSyncPreview | null): (userId: string) => string {
  const map = new Map<string, string>();
  for (const c of preview?.changes ?? []) map.set(c.userId, c.alias);
  return (userId) => map.get(userId) ?? userId;
}
