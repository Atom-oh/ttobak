/** Server-side processing state of a meeting, as shown on list cards. */
export type ProcessingBadge = { label: string; tone: 'progress' | 'error' };

/** Transcription or notes still running on the server. */
export function isProcessing(status?: string): boolean {
  return status === 'transcribing' || status === 'summarizing';
}

/** Card badge for a processing or failed meeting; `null` for other states. */
export function processingBadge(status?: string): ProcessingBadge | null {
  switch (status) {
    case 'transcribing': return { label: '전사 중', tone: 'progress' };
    case 'summarizing': return { label: '요약 중', tone: 'progress' };
    case 'error': return { label: '처리 실패', tone: 'error' };
    default: return null;
  }
}

/** The home list refreshes processing meetings this often while visible. */
export const PROCESSING_REFRESH_MS = 15_000;
/** Refreshing stops this long after it started; a reload resumes it. */
export const PROCESSING_REFRESH_LIMIT_MS = 30 * 60_000;

export function refreshListed<T extends { meetingId: string; status?: string }>(
  listed: T[], fresh: T[], uploadedId?: string,
): T[] {
  const byId = new Map(fresh.map(meeting => [meeting.meetingId, meeting]));
  const result = listed.map(meeting => byId.get(meeting.meetingId) ?? meeting);
  const uploaded = uploadedId ? byId.get(uploadedId) : undefined;
  if (uploaded && !result.some(meeting => meeting.meetingId === uploadedId)) result.unshift(uploaded);
  return result.map(meeting => meeting.meetingId === uploadedId && meeting.status === 'recording'
    ? { ...meeting, status: 'transcribing' } : meeting);
}
