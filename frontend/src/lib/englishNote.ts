import type { EnglishNote } from '@/types/meeting';

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
