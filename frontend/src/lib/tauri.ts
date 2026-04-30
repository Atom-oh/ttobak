'use client';

interface TauriGlobal {
  core: {
    invoke: <T>(cmd: string, args?: Record<string, unknown>) => Promise<T>;
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

export function readNativeRecordingBytes(path: string): Promise<number[]> {
  return invoke<number[]>('read_recording_bytes', { path });
}
