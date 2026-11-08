'use client';

import { useEffect, useState } from 'react';
import { listNativeInputDevices, type NativeInputDevice } from '@/lib/tauri';

/** Per-viewer convenience: the last microphone picked in the Mac app. */
const STORAGE_KEY = 'ttobak.nativeInputDevice';
const REFRESH_MS = 3_000;

export function readNativeInputPreference(): string {
  try {
    return localStorage.getItem(STORAGE_KEY) ?? '';
  } catch {
    return '';
  }
}

function saveNativeInputPreference(uid: string): void {
  try {
    if (uid) localStorage.setItem(STORAGE_KEY, uid);
    else localStorage.removeItem(STORAGE_KEY);
  } catch {
    // Storage may be unavailable; the choice still applies to this page.
  }
}

interface NativeMicSelectorProps {
  value: string;
  onChange: (uid: string) => void;
  disabled?: boolean;
  errorId?: string;
}

/** Mac app System Audio mode: which microphone to mix with system audio
 * (ADR-061). "Automatic" follows the meeting app's microphone, so the
 * recording hears what Zoom/Teams/Meet hears. Hidden on app builds without
 * the device list. */
export function NativeMicSelector({ value, onChange, disabled, errorId }: NativeMicSelectorProps) {
  const [devices, setDevices] = useState<NativeInputDevice[] | null>(null);

  useEffect(() => {
    if (disabled) return;
    let cancelled = false;
    const refresh = () => {
      void listNativeInputDevices().then((list) => {
        if (!cancelled) setDevices(list);
      });
    };
    refresh();
    // Meeting apps and plugged devices change while the page is open.
    const timer = setInterval(refresh, REFRESH_MS);
    return () => {
      cancelled = true;
      clearInterval(timer);
    };
  }, [disabled]);

  if (!devices) return null;
  // Virtual loopbacks are hidden unless something actually uses them as a
  // microphone (the default input or a meeting app's input).
  const shown = devices.filter((d) => !d.is_virtual || d.is_default || d.used_by.length > 0 || d.uid === value);

  const meetingDevice = devices.find((d) => d.used_by.length > 0);
  const defaultDevice = devices.find((d) => d.is_default);
  const autoTarget = meetingDevice
    ? `${meetingDevice.used_by.join(', ')}의 마이크 → ${meetingDevice.name}`
    : defaultDevice
      ? `회의 앱이 없으면 기본 입력 → ${defaultDevice.name}`
      : '회의 앱의 마이크를 따라갑니다';
  const selectedMissing = value !== '' && !devices.some((d) => d.uid === value);

  const select = (uid: string) => {
    saveNativeInputPreference(uid);
    onChange(uid);
  };

  return (
    <div className="flex flex-col gap-1.5 w-full max-w-xl">
      <label htmlFor="native-mic" className="text-xs font-bold text-slate-600 dark:text-text-secondary">
        녹음할 마이크
      </label>
      <select
        id="native-mic"
        value={selectedMissing ? '' : value}
        onChange={(e) => select(e.target.value)}
        disabled={disabled}
        aria-describedby={errorId}
        aria-invalid={errorId ? true : undefined}
        className="min-h-10 rounded-lg border border-slate-200 dark:border-white/10 bg-white dark:bg-surface px-3 text-sm text-slate-900 dark:text-text-main disabled:opacity-60"
      >
        <option value="">자동 — 회의 앱 마이크 따라가기</option>
        {shown.map((d) => {
          const notes = [d.used_by.length ? `${d.used_by.join(', ')} 사용 중` : '', d.is_default ? '기본' : '']
            .filter(Boolean)
            .join(' · ');
          return (
            <option key={d.uid} value={d.uid}>
              {notes ? `${d.name} (${notes})` : d.name}
            </option>
          );
        })}
      </select>
      <p className="text-xs text-slate-500 dark:text-text-muted">
        {value === '' || selectedMissing ? autoTarget : '선택한 마이크를 시스템 소리와 함께 녹음합니다.'}
        {selectedMissing && ' (이전에 선택한 마이크가 연결되어 있지 않아 자동으로 고릅니다)'}
      </p>
    </div>
  );
}
