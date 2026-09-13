import { meetingsApi } from './api';

export interface SavedMeetingNotes { notes: string; notesRevision: string }

/** Follow server cursors without hydrating either transcript or accepting a mixed revision. */
export async function readSavedMeetingSection(meetingId: string, section: 'notes' | 'summary'): Promise<string> {
  return (await readSection(meetingId, section)).content;
}

export async function readSavedMeetingNotes(meetingId: string): Promise<SavedMeetingNotes> {
  const result = await readSection(meetingId, 'notes');
  if (result.notesRevision === undefined) throw new Error('메모 버전을 확인하지 못했습니다. 잠시 후 다시 불러와 주세요.');
  return { notes: result.content, notesRevision: result.notesRevision };
}

async function readSection(meetingId: string, section: 'notes' | 'summary') {
  let cursor: string | undefined;
  let content = '';
  let revision: string | undefined;
  let notesRevision: string | undefined;
  let offset = 0;
  const cursors = new Set<string>();
  for (let pageIndex = 0; pageIndex < 64; pageIndex++) {
    const page = await (section === 'notes' ? meetingsApi.readNotes(meetingId, cursor) : meetingsApi.readSummary(meetingId, cursor));
    const text = 'notes' in page ? page.notes : page.content;
    if (page.meetingId !== meetingId || page.source !== section || typeof text !== 'string' || !page.page ||
        !page.revision || (revision && page.revision !== revision) || page.page.startOffset !== offset ||
        page.page.endOffset - page.page.startOffset !== Array.from(text).length) {
      throw new Error('저장된 메모의 페이지 정보를 확인하지 못했습니다. 다시 불러와 주세요.');
    }
    if (section === 'notes') {
      const version = 'notesRevision' in page ? page.notesRevision : undefined;
      if (version !== undefined && typeof version !== 'string' || pageIndex > 0 && version !== notesRevision) {
        throw new Error('메모 버전이 변경되었습니다. 다시 불러와 주세요.');
      }
      notesRevision = version;
    }
    revision = page.revision;
    offset = page.page.endOffset;
    content += text;
    if (new TextEncoder().encode(content).byteLength > 280 * 1024) {
      throw new Error('후속 문서에 담기에는 내용이 너무 큽니다. 필요한 부분을 발췌해 주세요.');
    }
    const next = page.page.nextCursor || undefined;
    if (!next) {
      if (offset !== page.page.totalCodePoints) throw new Error('저장된 내용 일부를 불러오지 못했습니다.');
      return { content, notesRevision };
    }
    if (cursors.has(next)) throw new Error('메모 페이지가 반복되어 불러오기를 중단했습니다.');
    cursors.add(next);
    cursor = next;
  }
  throw new Error('메모 페이지가 너무 많습니다. 필요한 부분을 발췌해 주세요.');
}
