/** Longest app error detail shown with a failed microphone switch. */
const MAX_DETAIL_CHARS = 300;

/** The message for a failed mid-recording microphone switch (ADR-061):
 * Korean guidance first, then the app's own reason. `captureLost` means the
 * recording no longer captures anything and must be stopped to save it. */
export function nativeMicSwitchError(err: unknown, captureLost: boolean): string {
  const raw = typeof err === 'string' ? err : err instanceof Error ? err.message : '';
  const detail = raw.trim().slice(0, MAX_DETAIL_CHARS);
  const guidance = captureLost
    ? '마이크를 바꾸지 못해 녹음이 중단됐습니다. 지금 녹음을 종료해 저장한 뒤 다시 시작해 주세요.'
    : '마이크를 바꾸지 못했습니다. 장치 연결을 확인하고, 소리가 들어오지 않으면 녹음을 종료해 저장한 뒤 다시 시작해 주세요.';
  return detail ? `${guidance} (${detail})` : guidance;
}
