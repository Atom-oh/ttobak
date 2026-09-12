// Hand-authored backend response snapshots. No source selection, slicing,
// cursor encoding, hashing, or time-window implementation lives in this fixture.
export const CURSOR = 'server:v2/opaque+cursor?x=1&y=%20';
export const REVISION = 'opaque-server-source-revision-v3';
export const HUGE_MEETING = {
  meetingId: 'huge', notes: '정정😀\n',
  transcriptA: 'a'.repeat(4 * 1024 * 1024),
  transcriptB: 'b'.repeat(3 * 1024 * 1024),
};

export const NOTES_PAGES = [
  {
    meetingId: 'notes', title: '현재 회의', permission: 'read', source: 'notes',
    notes: '정정😀\n', revision: REVISION, metadataTruncated: ['participants'],
    availableCodePoints: { notes: 7, summary: 6 },
    actionItems: [{ id: 'task', text: '확인', completed: true }],
    actionItemsAnalysis: { status: 'failed', errorCode: 'SOURCE_CHANGED', runId: 'run-current' },
    actionItemsPreview: { available: true, totalItems: 9, complete: false, metadataTruncated: ['actionItems[0].text'], readWithSection: 'actionItems' },
    page: { unit: 'unicode_code_points', startOffset: 0, endOffset: 4, totalCodePoints: 7, matchingCodePoints: 7, complete: false, nextCursor: CURSOR },
  },
  {
    meetingId: 'notes', source: 'notes', notes: '완료.', revision: REVISION,
    actionItems: [], actionItemsAnalysis: { status: 'unknown' },
    page: { unit: 'unicode_code_points', startOffset: 4, endOffset: 7, totalCodePoints: 7, matchingCodePoints: 7, complete: true, nextCursor: null },
  },
];

export const TRANSCRIPT_PAGE = {
  meetingId: 'transcript', title: '현재 회의', source: 'B', selectedSource: 'B',
  requestedSource: 'selected', revision: REVISION, mode: 'segments',
  provenance: { field: 'transcriptB', segmentEvidence: 'full_text_match', sttProvider: 'whisper', providerScope: 'meeting' },
  timeRange: { startTime: 60, endTime: 120 }, completenessScope: 'requested_time_range',
  chunks: [{
    text: '발화😀\n', startOffset: 40, endOffset: 44,
    segment: { index: 4, id: 'seg-4', speaker: '김', startTime: 59.5, endTime: 61.25,
      timingScope: 'whole_segment', partial: true, metadataTruncated: [] },
  }],
  page: { unit: 'unicode_code_points', startOffset: 40, endOffset: 44, totalCodePoints: 100, matchingCodePoints: 8, complete: false, nextCursor: CURSOR },
};

export function responseFixture(id, query) {
  const next = query.get('cursor');
  if (id === 'notes' || id.startsWith('deny')) return { ...NOTES_PAGES[next ? 1 : 0], meetingId: id };
  if (id === 'transcript') return {
    ...TRANSCRIPT_PAGE,
    ...(next ? {
      chunks: [{ ...TRANSCRIPT_PAGE.chunks[0], text: '다음.\n', startOffset: 44, endOffset: 48 }],
      page: { ...TRANSCRIPT_PAGE.page, startOffset: 44, endOffset: 48, complete: true, nextCursor: null },
    } : {}),
  };
  if (id === 'explicit') return {
    ...TRANSCRIPT_PAGE, meetingId: id, source: 'A', requestedSource: 'A', mode: 'text',
    provenance: { field: 'transcriptA', segmentEvidence: 'unavailable', sttProvider: 'whisper', providerScope: 'meeting' },
    timeRange: null, completenessScope: 'source',
    chunks: [{ text: '원문', startOffset: 0, endOffset: 2 }],
    page: { unit: 'unicode_code_points', startOffset: 0, endOffset: 2, totalCodePoints: 2, matchingCodePoints: 2, complete: true, nextCursor: null },
  };
  const page = {
    ...NOTES_PAGES[1], meetingId: id,
    page: { ...NOTES_PAGES[1].page, startOffset: 0, endOffset: 3, totalCodePoints: 3, matchingCodePoints: 3 },
  };
  if (id === 'summary') {
    delete page.notes;
    Object.assign(page, { source: 'summary', content: '회의 요약.', page: { ...page.page, endOffset: 6, totalCodePoints: 6, matchingCodePoints: 6 } });
  }
  if (id === 'actions') { delete page.notes; Object.assign(page, {
    source: 'actionItems', actionItemsJson: next ? '"확인","completed":true}]' : '[{"id":"task","text":',
    actionItemsAnalysis: { status: 'succeeded', runId: 'done-run' },
    page: { ...page.page, startOffset: next ? 21 : 0, endOffset: next ? 44 : 21,
      totalCodePoints: 44, matchingCodePoints: 44, complete: !!next, nextCursor: next ? null : CURSOR },
  }); }
  if (id === 'legacy') delete page.actionItemsAnalysis;
  if (id.startsWith('analysis-')) page.actionItemsAnalysis = { status: id.slice(9), runId: 'run-1', leaseUntil: 1789178400000 };
  if (id === 'huge') Object.assign(page, {
    notes: HUGE_MEETING.notes, page: { ...page.page, endOffset: 4, totalCodePoints: 4, matchingCodePoints: 4 },
  });
  if (id === 'wrong-identity') page.meetingId = 'another-meeting';
  if (id === 'invalid-page') page.page = { complete: true, nextCursor: CURSOR };
  if (id === 'full-response') return { meetingId: id, notes: 'legacy full response', transcriptA: 'must not forward' };
  if (id === 'wrapper-overflow') page.notes = '"'.repeat(13_000);
  if (id === 'oversized-stream') page.notes = 'NEVER_FORWARD_OVERSIZED_DATA' + 'x'.repeat(200_000);
  if (id === 'oversized-utf8') page.notes = '한'.repeat(11_000);
  return page;
}
