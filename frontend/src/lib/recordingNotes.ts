import { codePointLength, MAX_MEETING_NOTES } from './meetingReferences';
import type { SavedMeetingNotes } from './meetingNotes';

interface NotesPort {
  supportsComparison: (meetingId: string) => Promise<boolean>;
  write: (meetingId: string, notes: string, expected: string, expectedRevision: string, signal: AbortSignal) => Promise<{ notesRevision?: string }>;
  read: (meetingId: string) => Promise<SavedMeetingNotes>;
}

interface NotesState {
  meetingId: string;
  acknowledged: string;
  revision: string;
  supported: boolean;
  uncertain: boolean;
  conflict?: SavedMeetingNotes;
}

/** One notes writer shared by recording autosave, finalization and upload retry. */
export class RecordingNotes {
  private state?: NotesState;
  private queue: Promise<void> = Promise.resolve();
  private controller?: AbortController;

  constructor(private readonly port: NotesPort, private readonly timeoutMs = 15000) {}

  initialize(meetingId: string, acknowledged: string, supported: boolean, revision = '') {
    this.controller?.abort();
    this.state = { meetingId, acknowledged, supported, revision, uncertain: false };
  }

  reset() {
    this.controller?.abort();
    this.state = undefined;
  }

  conflict(meetingId: string): string | undefined {
    return this.state?.meetingId === meetingId ? this.state.conflict?.notes : undefined;
  }

  /** Called only after the user explicitly opens the current-versus-draft comparison. */
  adoptConflict(meetingId: string) {
    const state = this.state;
    if (state?.meetingId === meetingId && state.conflict !== undefined) {
      state.acknowledged = state.conflict.notes;
      state.revision = state.conflict.notesRevision;
      state.conflict = undefined;
      // A same-text save must still fence any earlier ambiguous request.
      state.uncertain = true;
    }
  }

  persist(meetingId: string, notes: string): Promise<void> {
    const scope = this.state;
    const result = this.queue.catch(() => {}).then(() => this.write(meetingId, notes, scope));
    // Keep the queue usable, while the returned promise still reports every failure.
    this.queue = result;
    return result;
  }

  private async write(meetingId: string, notes: string, state?: NotesState) {
    if (codePointLength(notes) > MAX_MEETING_NOTES) throw new Error('메모는 32,000자까지 저장할 수 있습니다. 메모를 줄여 다시 저장해 주세요.');
    if (!state || this.state !== state || state.meetingId !== meetingId) throw new Error('녹음 세션이 변경되었습니다.');
    // A confirmed notes write is independent of the audio upload. Retrying
    // audio must not resend that old value over another editor's newer notes.
    if (state.acknowledged === notes && !state.uncertain) return;
    if (!state.supported) {
      state.supported = await this.port.supportsComparison(meetingId);
      if (!state.supported) throw new Error('메모 저장 기능을 업데이트 중입니다. 녹음은 보관되어 있으니 잠시 후 다시 저장해 주세요.');
    }
    if (this.state !== state) throw new Error('녹음 세션이 변경되었습니다.');
    const controller = new AbortController();
    this.controller = controller;
    const timer = setTimeout(() => controller.abort(), this.timeoutMs);
    try {
      const response = await this.port.write(meetingId, notes, state.acknowledged, state.revision, controller.signal);
      if (!response.notesRevision || response.notesRevision === state.revision) {
        throw new Error('메모 저장 버전을 확인하지 못했습니다. 다시 저장해 주세요.');
      }
      if (this.state === state) {
        state.acknowledged = notes; state.revision = response.notesRevision;
        state.conflict = undefined; state.uncertain = false;
      }
    } catch (error) {
      if (this.state !== state) throw error;
      state.uncertain = true;
      // Abort is not rollback. Read back an uncertain commit without silently
      // adopting another editor's value as our next comparison baseline.
      try {
        const current = await this.port.read(meetingId);
        if (this.state !== state) throw error;
        if (current.notes === notes && current.notesRevision !== state.revision) {
          state.acknowledged = notes; state.revision = current.notesRevision;
          state.conflict = undefined; state.uncertain = false; return;
        }
        if (current.notes !== state.acknowledged || current.notesRevision !== state.revision) state.conflict = current;
      } catch { /* The original write failure remains visible to the caller. */ }
      throw error;
    } finally {
      clearTimeout(timer);
      if (this.controller === controller) this.controller = undefined;
    }
  }
}
