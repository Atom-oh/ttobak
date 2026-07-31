'use client';

interface TauriEvent<T> {
  payload: T;
}

interface TauriGlobal {
  core: {
    invoke: <T>(cmd: string, args?: Record<string, unknown>) => Promise<T>;
  };
  event: {
    listen: <T>(
      eventName: string,
      handler: (event: TauriEvent<T>) => void,
    ) => Promise<() => void>;
  };
}

declare global {
  interface Window {
    __TAURI__?: TauriGlobal;
    __TAURI_INTERNALS__?: unknown;
  }
}

export function isTauri(): boolean {
  return typeof window !== 'undefined' && '__TAURI_INTERNALS__' in window;
}

function invoke<T>(cmd: string, args?: Record<string, unknown>): Promise<T> {
  if (!window.__TAURI__?.core) {
    return Promise.reject(new Error('Tauri API not available'));
  }
  return window.__TAURI__.core.invoke<T>(cmd, args);
}

/**
 * True if `err` looks like Tauri's IPC rejection for an unregistered
 * command — i.e. the SPA and the installed mac app binary are out of sync
 * (a new SPA deploy landed before the app was rebuilt, or vice versa).
 * Tauri's exact rejection text isn't a stable API, so this is a heuristic
 * used only to pick a friendlier error message — a false negative here just
 * falls through to the generic error path, never breaks anything.
 */
export function isCommandNotFound(err: unknown): boolean {
  const message = err instanceof Error ? err.message : String(err);
  return /not found|not allowed|unknown command/i.test(message);
}

export interface TauriStartResponse {
  temp_path: string;
}

export interface TauriStopResponse {
  temp_path: string;
  duration_ms: number;
  byte_size: number;
  /** True if ScreenCaptureKit's stop didn't return within the Rust-side
   * timeout. The WAV up to the last periodic flush checkpoint is still
   * valid and playable — this is a soft warning, not a failure. */
  stop_timed_out: boolean;
}

export interface TauriStatusResponse {
  recording: boolean;
  temp_path: string | null;
  elapsed_ms: number;
  /** True while a stop's background finalize is still writing the WAV.
   * `recording` flips false the moment the stop command takes the handle,
   * so after `stop_timed_out` this — not `recording` — is what signals the
   * file is safe to upload. Optional: older Rust builds don't send it. */
  finalizing?: boolean;
}

export interface TauriUploadProgress {
  loaded: number;
  total: number;
}

export function startNativeRecording(meetingId: string): Promise<TauriStartResponse> {
  return invoke<TauriStartResponse>('start_recording', { meetingId });
}

export function stopNativeRecording(): Promise<TauriStopResponse> {
  return invoke<TauriStopResponse>('stop_recording');
}

export function getNativeRecordingStatus(): Promise<TauriStatusResponse> {
  return invoke<TauriStatusResponse>('recording_status');
}

/**
 * Stream the recorded WAV at `path` straight from disk to `uploadUrl` (a
 * presigned S3 PUT URL) from the Rust side — replaces the old
 * `readRecordingBytes` (removed), which pulled the whole file through the
 * Tauri IPC bridge into the WebView and crashed JavaScriptCore on a large
 * recording. See `mac-app/src-tauri/src/upload.rs`. Resolves with the HTTP
 * status code on success; rejects on any non-2xx response or a stalled
 * transfer. Never deletes the source file — call `cleanupRecording` only
 * after the caller's own upload-complete notification succeeds.
 */
export function uploadRecording(
  path: string,
  uploadUrl: string,
  contentType: string,
): Promise<number> {
  return invoke<number>('upload_recording', { path, uploadUrl, contentType });
}

function waitForOnline(): Promise<void> {
  if (typeof navigator === 'undefined' || navigator.onLine) return Promise.resolve();
  return new Promise((resolve) => {
    const handler = () => {
      window.removeEventListener('online', handler);
      resolve();
    };
    window.addEventListener('online', handler);
  });
}

/**
 * Wraps `uploadRecording` with network-aware retry. Native mode has no
 * retry of its own (unlike `putWithProgress` for browser blob uploads) —
 * this checks connectivity before every attempt via `navigator.onLine`
 * (rung 4: native platform feature, no new dependency) and, while
 * offline, keeps waiting for the browser's `online` event instead of
 * giving up. Once online, falls back to a small bounded retry (mirrors
 * putWithProgress's maxRetries pattern) so a non-network failure (bad
 * presigned URL, server error) doesn't loop forever.
 */
export async function uploadRecordingWithRetry(
  path: string,
  uploadUrl: string,
  contentType: string,
  maxRetries = 2,
): Promise<number> {
  let attempt = 0;
  while (true) {
    await waitForOnline();
    try {
      return await uploadRecording(path, uploadUrl, contentType);
    } catch (err) {
      if (typeof navigator !== 'undefined' && !navigator.onLine) {
        continue; // offline again -- loop back to waitForOnline
      }
      if (attempt >= maxRetries) throw err;
      attempt++;
      await new Promise((r) => setTimeout(r, 2000 * attempt));
    }
  }
}

/**
 * Preflight the existence of the `upload_recording` command BEFORE a
 * recording starts, so an installed-app/SPA version skew costs 5 seconds
 * instead of the whole meeting. Without this, the skew only surfaces after
 * the user has already recorded — one incident lost 83 minutes of System
 * Audio that way (the recording was fine on disk, but every upload attempt
 * was rejected instantly by Tauri's IPC as an unknown command).
 *
 * ponytail: invoking with an empty path is the cheapest existence check. If
 * the command exists, `validate_recording_path`'s `canonicalize("")` fails
 * with `AppError::Io` ("No such file or directory") before any FS/network
 * work — so "some other error" means the command IS there, and we stay
 * quiet. No side effects: it takes the lock, fails validation, returns; no
 * emit, no upload.
 */
export async function assertUploadRecordingAvailable(): Promise<void> {
  try {
    await invoke<number>('upload_recording', { path: '', uploadUrl: '', contentType: '' });
  } catch (err) {
    if (isCommandNotFound(err)) {
      throw new Error('설치된 Mac 앱 버전이 오래되어 녹음 업로드 명령이 없습니다 — 앱을 업데이트한 뒤 녹음을 시작해주세요.');
    }
  }
}

export function cleanupRecording(path: string): Promise<void> {
  return invoke<void>('cleanup_recording', { path });
}

/**
 * Subscribe to a Tauri event, decoding each payload with `decode`. Returns a
 * teardown function; safe to call when not running inside Tauri (returns a
 * no-op unsubscriber). A `listen()` rejection — e.g. because
 * `capabilities/*.json` doesn't grant this remote origin event access — is
 * logged loudly rather than swallowed: a silent failure here is exactly what
 * let the desktop waveform sit flat for an entire meeting with no error
 * anywhere.
 */
function subscribeTauriEvent<Raw, T>(
  eventName: string,
  decode: (raw: Raw) => T,
  handler: (value: T) => void,
): () => void {
  if (typeof window === 'undefined' || !window.__TAURI__?.event?.listen) {
    return () => {};
  }
  let unlisten: (() => void) | null = null;
  let cancelled = false;
  window.__TAURI__.event
    .listen<Raw>(eventName, (event) => handler(decode(event.payload)))
    .then((fn) => {
      if (cancelled) {
        fn();
      } else {
        unlisten = fn;
      }
    })
    .catch((err) => {
      console.warn(
        `[tauri] listen("${eventName}") rejected — the "${eventName}" data will never arrive. ` +
          'This usually means capabilities/*.json is missing a "remote" grant for this ' +
          "origin (plugin:event|listen is ACL-gated even though app commands aren't).",
        err,
      );
    });
  return () => {
    cancelled = true;
    unlisten?.();
  };
}

/**
 * Subscribe to the `native-audio-level` event emitted from `audio.rs` while
 * a System Audio recording is in progress. Payload is a normalized 0–1 RMS
 * value (~30 Hz).
 */
export function onNativeAudioLevel(handler: (level: number) => void): () => void {
  return subscribeTauriEvent<number, number>('native-audio-level', (v) => v, handler);
}

/** Subscribe to upload progress emitted by the `upload_recording` command. */
export function onNativeUploadProgress(
  handler: (progress: TauriUploadProgress) => void,
): () => void {
  return subscribeTauriEvent<TauriUploadProgress, TauriUploadProgress>(
    'native-upload-progress',
    (v) => v,
    handler,
  );
}

/**
 * Subscribe to `native-pcm-chunk` events: base64-encoded 16kHz mono
 * 16-bit PCM, ~1024 samples (~64ms) per chunk, emitted by the audio callback
 * in `audio.rs` alongside the WAV writer and level meter. Used to feed
 * Amazon Transcribe Streaming for live captions in System Audio mode (there
 * is no MediaStream in that mode, so this is the only source of audio for
 * `transcribeStreamingClient.ts`'s async-iterable bridge).
 */
export function onNativePcmChunk(handler: (chunk: Uint8Array) => void): () => void {
  return subscribeTauriEvent<string, Uint8Array>(
    'native-pcm-chunk',
    (base64) => {
      const binary = atob(base64);
      const bytes = new Uint8Array(binary.length);
      for (let i = 0; i < binary.length; i++) {
        bytes[i] = binary.charCodeAt(i);
      }
      return bytes;
    },
    handler,
  );
}
