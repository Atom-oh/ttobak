export const SHARE_FALLBACK_MESSAGE = '고객 연결과 팀 공유를 완료하지 못했습니다. 다시 시도해 주세요.';
export const SHARE_UNSAFE_MESSAGE =
  '기존 회의록은 공유용으로 확인되지 않았습니다. 요약 영역에서 "공유용 회의록 재생성"을 완료한 뒤 다시 공유해 주세요.';

// Maps a link-and-share failure to user-facing text. The unsafe-summary code needs
// an owner action, not a retry, so it gets its own guidance.
export function shareFailureMessage(failure: unknown): string {
  const code = typeof failure === 'object' && failure !== null ? (failure as { code?: unknown }).code : undefined;
  if (code === 'SUMMARY_SHARING_UNSAFE') return SHARE_UNSAFE_MESSAGE;
  return failure instanceof Error && failure.message ? failure.message : SHARE_FALLBACK_MESSAGE;
}
