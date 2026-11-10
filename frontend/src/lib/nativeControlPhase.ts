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
}

export function nativeControlPhase<Step extends string>(
  { recording, step, startInFlight, importing }: NativeControlPhaseInput<Step>,
): Step | 'recording' | 'uploading' | 'starting' | 'idle' {
  if (recording) return 'recording';
  if (step) return step;
  // A file import uploads through the browser, so no native upload is counted.
  if (importing) return 'uploading';
  return startInFlight ? 'starting' : 'idle';
}
