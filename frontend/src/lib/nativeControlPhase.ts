// The record page's phase reported to the Mac app (`control_report_state`).
// The app treats starting/recording/notes/creating/saving/uploading/redirecting
// as busy (update_policy.rs), so nothing installs or restarts mid-flow.

export interface NativeControlPhaseInput<Step extends string> {
  /** Native or browser recording is running. */
  recording: boolean;
  /** The post-recording step, when one is in progress. */
  step: Step | null;
  /** RecordButton's start is in flight. */
  startInFlight: boolean;
  /** An audio file import (browser upload) is in flight. */
  importing: boolean;
  /** A stopped browser recording is still being finalized (backup, blob
   * hand-off) before the post-recording step begins. */
  finalizing: boolean;
  /** The page's own meeting-flow reservation (`flowBusy`): draft creation,
   * a leftover upload across its confirmation, a backup restore. */
  flowBusy: boolean;
  /** The post-recording hook still holds the only copy of browser audio
   * (`hasPendingAudio`), for example at step `error` awaiting a retry. */
  pendingAudio: boolean;
}

export function nativeControlPhase<Step extends string>(
  { recording, step, startInFlight, importing, finalizing, flowBusy, pendingAudio }: NativeControlPhaseInput<Step>,
): Step | 'recording' | 'uploading' | 'saving' | 'creating' | 'starting' | 'idle' {
  if (recording) return 'recording';
  // A failed step keeps the unsaved Blob in memory for retry; a restart would lose it.
  if (pendingAudio && (step === null || step === 'error')) return 'saving';
  if (step) return step;
  // A file import uploads through the browser, so no native upload is counted.
  if (importing) return 'uploading';
  if (finalizing) return 'saving';
  if (flowBusy) return 'creating';
  return startInFlight ? 'starting' : 'idle';
}
