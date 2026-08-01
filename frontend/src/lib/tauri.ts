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

/** Single source of truth for the version-skew message — used at both the
 * preflight (before recording starts, where there is no recording to lose
 * yet) and the post-recording upload catch (`usePostRecording.ts`), so the
 * two never drift into different wording. The recovery path only applies
 * post-recording (the WAV already exists on disk then), so it's appended
 * there rather than baked into this shared string. */
export const VERSION_SKEW_MESSAGE =
  '설치된 Mac 앱 버전이 오래되어 업로드 명령이 없습니다 — 앱을 업데이트해주세요.';

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

/**
 * Resolves once the browser reports `online`, or immediately if already
 * online. Bounded by `remainingMs` (the caller's *cumulative* budget, not a
 * fresh timer per call — see `uploadRecordingWithRetry`) and cancellable
 * via `signal` — both reject, so a caller that's abandoning the flow
 * (unmount/reset) or has run out of budget doesn't leave this pending
 * forever with the `online` listener attached.
 */
function waitForOnline(remainingMs: number, signal: AbortSignal): Promise<void> {
  if (signal.aborted) return Promise.reject(new Error('Upload cancelled'));
  if (typeof navigator === 'undefined' || navigator.onLine) return Promise.resolve();
  if (remainingMs <= 0) return Promise.reject(new Error('Offline wait budget exhausted'));
  return new Promise((resolve, reject) => {
    const cleanup = () => {
      window.removeEventListener('online', onOnline);
      signal.removeEventListener('abort', onAbort);
      clearTimeout(timer);
    };
    const onOnline = () => { cleanup(); resolve(); };
    const onAbort = () => { cleanup(); reject(new Error('Upload cancelled while waiting for network')); };
    const timer = setTimeout(() => {
      cleanup();
      reject(new Error(`Still offline after ${Math.round(remainingMs / 1000)}s of budget — giving up`));
    }, remainingMs);
    window.addEventListener('online', onOnline);
    signal.addEventListener('abort', onAbort);
  });
}

/**
 * Wraps `uploadRecording` with network-aware retry. Native mode has no
 * retry of its own (unlike `putWithProgress` for browser blob uploads).
 *
 * `getUploadUrl` is called before EVERY attempt, not just the first — a
 * presigned PUT URL is only valid for its TTL (1h server-side default), and
 * an offline wait can outlast that, so re-presigning is what actually keeps
 * each attempt valid rather than retrying with a URL that's silently
 * expired. Resolves to `{status, key}` — the S3 key the upload actually
 * landed at (from the LAST successful `getUploadUrl` call) alongside the
 * upload's HTTP status, so the caller's own bookkeeping doesn't need a
 * separate variable capture or non-null assertion.
 *
 * `totalDeadlineMs` (default 45 min) is a *cumulative* wall-clock budget
 * across the whole call, not reset on every offline-wait — without that, a
 * flapping connection (offline → briefly online → offline again) could
 * re-arm a fresh wait every cycle and never actually give up. `signal` lets
 * the caller abort mid-wait or mid-backoff (navigated away, reset) instead
 * of resuming a stale flow later.
 */
/** `setTimeout` that also rejects immediately on abort, so a caller waiting
 * out a backoff notices cancellation within milliseconds instead of only at
 * the next loop iteration (up to `ms` late). */
function delay(ms: number, signal: AbortSignal): Promise<void> {
  if (signal.aborted) return Promise.reject(new Error('Upload cancelled'));
  return new Promise((resolve, reject) => {
    const onAbort = () => { clearTimeout(timer); reject(new Error('Upload cancelled')); };
    const timer = setTimeout(() => { signal.removeEventListener('abort', onAbort); resolve(); }, ms);
    signal.addEventListener('abort', onAbort, { once: true });
  });
}

export async function uploadRecordingWithRetry(
  path: string,
  getUploadUrl: () => Promise<{ uploadUrl: string; key: string }>,
  contentType: string,
  signal: AbortSignal,
  maxRetries = 2,
  totalDeadlineMs = 45 * 60_000,
): Promise<{ status: number; key: string }> {
  const deadline = Date.now() + totalDeadlineMs;
  let attempt = 0;
  while (true) {
    if (signal.aborted) throw new Error('Upload cancelled');
    await waitForOnline(deadline - Date.now(), signal);
    try {
      const { uploadUrl, key } = await getUploadUrl();
      // Re-check here, not just at the top of the loop: getUploadUrl's own
      // network round-trip (up to 15s) is an await the caller could abort
      // during, and without this a cancelled flow would still kick off the
      // full-file PUT right after.
      if (signal.aborted) throw new Error('Upload cancelled');
      const status = await uploadRecording(path, uploadUrl, contentType);
      return { status, key };
    } catch (err) {
      if (signal.aborted) throw err;
      if (Date.now() >= deadline) throw err;
      if (typeof navigator !== 'undefined' && !navigator.onLine) {
        // Offline again -- loop back to waitForOnline. Still backs off
        // (rather than tight-looping presign+upload calls) so a rapidly
        // flapping connection can't hammer the presign endpoint; doesn't
        // consume an `attempt` since the failure was environmental, not
        // this attempt's fault.
        await delay(2000, signal);
        continue;
      }
      if (attempt >= maxRetries) throw err;
      attempt++;
      await delay(2000 * attempt, signal);
    }
  }
}

/**
 * Preflight the existence of the `upload_recording` command BEFORE a
 * recording starts, so an installed-app/SPA version skew is caught at
 * click time instead of only surfacing after the whole recording is done —
 * one incident lost 83 minutes of System Audio that way (the recording was
 * fine on disk, but every upload attempt was rejected instantly by Tauri's
 * IPC as an unknown command).
 *
 * Invoking with an empty path is the cheapest existence check, relying on
 * an implicit contract with the Rust side (see ADR-024): if the command
 * exists, `validate_recording_path`'s `canonicalize("")` fails with
 * `AppError::Io` before any FS/network work, so "some other error" means
 * the command IS there and we stay quiet. If that Rust-side validation
 * order ever changes, this degrades silently (a genuinely missing command
 * is still isCommandNotFound; other cases just pass through) — logged so
 * it isn't invisible.
 */
export async function assertUploadRecordingAvailable(): Promise<void> {
  try {
    await invoke<number>('upload_recording', { path: '', uploadUrl: '', contentType: '' });
  } catch (err) {
    if (isCommandNotFound(err)) {
      throw new Error(VERSION_SKEW_MESSAGE);
    }
    // console.debug is effectively invisible in production -- this is
    // meant to be seen if the Rust-side validation order this probe
    // depends on (see ADR-024) ever changes silently.
    console.warn('assertUploadRecordingAvailable: non-command-not-found rejection, treating command as present', err);
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
