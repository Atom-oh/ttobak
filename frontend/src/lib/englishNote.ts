import type { EnglishNote } from '@/types/meeting';

/** Korean messages for POST /notes/en error codes. */
export const englishRequestErrors: Record<string, string> = {
  MEETING_BUSY: '다른 작업이 진행 중입니다. 잠시 후 다시 시도해 주세요.',
  SOURCE_NOT_READY: '첨부 문서의 텍스트 추출을 먼저 완료해 주세요.',
  NO_SUMMARY_SOURCE: '영문 회의록을 만들 저장 자료가 없습니다.',
  SHARING_REGEN_REQUIRED: '회의록을 먼저 다시 생성해 주세요.',
  SUMMARY_TOO_LARGE: '영문 회의록을 만들 자료가 처리 한도를 초과했습니다.',
  SUMMARY_UNAVAILABLE: '영문 회의록 작업을 전달하지 못했습니다. 다시 시도해 주세요.',
  CONFLICT: '저장된 자료가 변경되었습니다. 다시 시도해 주세요.',
  FORBIDDEN: '영문 회의록을 만들 편집 권한이 없습니다.',
};

/** Korean messages for failed English run error codes. */
export const englishFailures: Record<string, string> = {
  SOURCE_NOT_READY: englishRequestErrors.SOURCE_NOT_READY,
  NO_SUMMARY_SOURCE: englishRequestErrors.NO_SUMMARY_SOURCE,
  OUTPUT_TOO_LARGE: '생성된 영문 회의록이 저장 한도를 초과했습니다.',
  INVALID_OUTPUT: '생성된 영문 회의록의 형식이 올바르지 않아 적용하지 않았습니다.',
  PROVIDER_FAILED: 'AI 모델이 영문 회의록을 만들지 못했습니다. 잠시 후 다시 시도해 주세요.',
  PERSISTENCE_FAILED: '영문 회의록을 저장하지 못했습니다.',
  PUBLISH_FAILED: '영문 회의록 작업을 전달하지 못했습니다.',
  INTERRUPTED: '영문 회의록 작업이 완료되기 전에 중단되었습니다.',
  SOURCE_CHANGED: '저장된 자료가 변경되어 결과를 적용하지 않았습니다.',
  ACCESS_REVOKED: '요청자의 편집 권한이 변경되어 결과를 적용하지 않았습니다.',
  TIMEOUT: '영문 회의록 처리 시간이 초과되었습니다.',
  SOURCE_MISSING: '회의 자료를 찾을 수 없어 영문 회의록을 만들지 못했습니다.',
  SOURCE_TOO_LARGE: '영문 회의록을 만들 자료가 처리 한도를 초과했습니다.',
  SOURCE_UNAVAILABLE: '회의 자료를 읽지 못해 영문 회의록을 만들지 못했습니다.',
};

export interface EnglishNoteView {
  showToggle: boolean;
  action: 'generate' | 'regenerate' | 'retry' | null;
  badge: 'generating' | 'failed' | 'stale' | null;
}

/** Derives the English tab, action and badge from the note status and edit access. */
export function englishNoteView(note: EnglishNote | null, canEdit: boolean): EnglishNoteView {
  const hasContent = !!note?.content;
  const pending = note?.status === 'queued' || note?.status === 'running';
  const failed = note?.status === 'failed';
  const badge = pending ? 'generating' : failed ? 'failed' : hasContent && note?.stale ? 'stale' : null;
  const action = !canEdit || pending ? null : failed ? 'retry' : hasContent ? 'regenerate' : 'generate';
  return { showToggle: hasContent, action, badge };
}

/**
 * The state read is one authorization check plus one row read, so it runs on
 * every visit: a first run may be pending, or may have failed with no stored note.
 * `hasEnglishNote` is not a gate.
 */
export function shouldLoadEnglishNote(enabled: boolean): boolean {
  return enabled;
}
