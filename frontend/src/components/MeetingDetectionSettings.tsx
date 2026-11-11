'use client';

import { useEffect, useState } from 'react';
import { getMeetingDetection, isTauri, setMeetingDetectionEnabled } from '@/lib/tauri';

/** Mac app only: turns meeting detection prompts on or off (ADR-017).
 * Hidden in a browser or an app build without detection. */
export function MeetingDetectionSettings() {
  const [enabled, setEnabled] = useState<boolean | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!isTauri()) return;
    let cancelled = false;
    void getMeetingDetection().then((state) => {
      if (!cancelled && state?.supported) setEnabled(state.enabled);
    });
    return () => {
      cancelled = true;
    };
  }, []);

  if (enabled === null) return null;

  const toggle = async () => {
    const next = !enabled;
    setError(null);
    try {
      await setMeetingDetectionEnabled(next);
      setEnabled(next);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  };

  return (
    <section className="lg:pb-8 lg:border-b lg:border-slate-200 dark:lg:border-white/10">
      <h3 className="section-header mb-4">회의 감지 (Mac 앱)</h3>
      <div className="flex items-start justify-between gap-4">
        <div className="flex flex-col gap-1">
          <p className="text-sm text-slate-700 dark:text-text-secondary">
            Zoom, Teams, Webex, Slack 허들, Google Meet 등이 마이크를 쓰기 시작하면 녹음을 제안합니다.
            직접 시작을 눌러야 녹음됩니다.
          </p>
          <p className="text-xs text-slate-500 dark:text-text-muted">
            브라우저 회의는 해당 브라우저에 대한 자동화 권한을 요청하며, 회의 주소만 확인합니다.
          </p>
          {error && <p className="text-xs text-red-600 dark:text-red-400">{error}</p>}
        </div>
        <button
          type="button"
          aria-pressed={enabled}
          aria-label="회의 감지"
          onClick={() => void toggle()}
          className={`relative inline-flex h-6 w-11 shrink-0 items-center rounded-full transition-colors ${
            enabled ? 'bg-primary' : 'bg-slate-300 dark:bg-white/20'
          }`}
        >
          <span
            className={`inline-block h-5 w-5 rounded-full bg-white shadow transition-transform ${
              enabled ? 'translate-x-5' : 'translate-x-0.5'
            }`}
          />
        </button>
      </div>
    </section>
  );
}
