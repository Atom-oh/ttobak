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

export interface TauriStartResponse {
  temp_path: string;
}

export interface TauriStopResponse {
  temp_path: string;
  duration_ms: number;
  byte_size: number;
}

export interface TauriStatusResponse {
  recording: boolean;
  temp_path: string | null;
  elapsed_ms: number;
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

export function readRecordingBytes(path: string): Promise<ArrayBuffer> {
  return invoke<ArrayBuffer>('read_recording_bytes', { path });
}

export function cleanupRecording(path: string): Promise<void> {
  return invoke<void>('cleanup_recording', { path });
}

/**
 * Subscribe to the `native-audio-level` event emitted from `audio.rs` while
 * a System Audio recording is in progress. Payload is a normalized 0–1 RMS
 * value (~30 Hz). Returns a teardown function. Safe to call when not running
 * inside Tauri — returns a no-op unsubscriber.
 */
export function onNativeAudioLevel(handler: (level: number) => void): () => void {
  if (typeof window === 'undefined' || !window.__TAURI__?.event?.listen) {
    return () => {};
  }
  let unlisten: (() => void) | null = null;
  let cancelled = false;
  window.__TAURI__.event
    .listen<number>('native-audio-level', (event) => handler(event.payload))
    .then((fn) => {
      if (cancelled) {
        fn();
      } else {
        unlisten = fn;
      }
    })
    .catch(() => {
      // listen() may reject if Tauri internals aren't ready yet — best effort
    });
  return () => {
    cancelled = true;
    unlisten?.();
  };
}
