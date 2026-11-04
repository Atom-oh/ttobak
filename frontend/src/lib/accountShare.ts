export const SHARE_FALLBACK_MESSAGE = '고객 연결과 팀 공유를 완료하지 못했습니다. 다시 시도해 주세요.';
export const SHARE_UNSAFE_MESSAGE =
  '현재 회의록의 팀 공유를 확인하지 못했습니다. 회의록 생성이 완료되었는지 확인하고, 페이지를 새로고침한 뒤 다시 시도해 주세요.';

// Maps a link-and-share failure to user-facing text. The unsafe-summary code needs
// completion/current sharing checks, so it gets its own guidance.
export function shareFailureMessage(failure: unknown): string {
  const code = typeof failure === 'object' && failure !== null ? (failure as { code?: unknown }).code : undefined;
  if (code === 'SUMMARY_SHARING_UNSAFE') return SHARE_UNSAFE_MESSAGE;
  return failure instanceof Error && failure.message ? failure.message : SHARE_FALLBACK_MESSAGE;
}
