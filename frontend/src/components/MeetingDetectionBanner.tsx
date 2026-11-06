'use client';

import { useEffect, useRef, useState } from 'react';
import { useAuth } from '@/components/auth/AuthProvider';
import {
  getMeetingDetection,
  isTauri,
  onNativeMeetingDetected,
  onNativeMeetingEnded,
  onNativeMeetingResumed,
  respondMeetingDetection,
  type NativeMeetingDetection,
} from '@/lib/tauri';

type Prompt = { phase: 'detected' | 'ended'; detection: NativeMeetingDetection };

/** After a meeting ends, the running recording stops and uploads after this
 * many seconds unless the user keeps recording. */
const AUTO_STOP_SECONDS = 30;

function meetingTitle(label: string): string {
  const when = new Intl.DateTimeFormat('ko-KR', {
    month: 'long',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  }).format(new Date());
  return `${label} 회의 · ${when}`;
}

/** Mac app only: offers to record when the app detects a meeting, and when
 * that meeting ends stops the running recording after a short countdown the
 * user can cancel (ADR-061). Nothing records until the user confirms; start
 * and stop follow the normal control path to the record page. Renders
 * nothing in a browser or an older app build. */
export function MeetingDetectionBanner() {
  const { isAuthenticated } = useAuth();
  const [prompt, setPrompt] = useState<Prompt | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // Countdown to the automatic stop, bound to one ended detection.
  const [countdown, setCountdown] = useState<{ id: string; remaining: number } | null>(null);
  // Detections whose automatic stop already ran: it is attempted once only.
  const autoStopped = useRef(new Set<string>());

  const endedId = prompt?.phase === 'ended' ? prompt.detection.detectionId : null;
  useEffect(() => {
    if (!endedId) {
      setCountdown(null);
      return;
    }
    setCountdown({ id: endedId, remaining: AUTO_STOP_SECONDS });
    const timer = setInterval(() => {
      setCountdown((c) => (c && c.id === endedId && c.remaining > 0 ? { ...c, remaining: c.remaining - 1 } : c));
    }, 1000);
    return () => clearInterval(timer);
  }, [endedId]);

  // The automatic path can only stop, and only the ended detection it counted
  // for; it never confirms any other prompt (ADR-061: no unconfirmed start).
  useEffect(() => {
    if (!countdown || countdown.remaining > 0 || countdown.id !== endedId) return;
    if (autoStopped.current.has(countdown.id)) return;
    autoStopped.current.add(countdown.id);
    const id = countdown.id;
    setBusy(true);
    void respondMeetingDetection(id, 'stop')
      .then((reply) => {
        if ('error' in reply && !reply.ok) setError(reply.error.message);
        else setPrompt((current) => (current?.detection.detectionId === id ? null : current));
      })
      .catch((err) => setError(err instanceof Error ? err.message : String(err)))
      .finally(() => setBusy(false));
  }, [countdown, endedId]);

  useEffect(() => {
    if (!isTauri() || !isAuthenticated) return;
    let cancelled = false;
    const offDetected = onNativeMeetingDetected((detection) => {
      setError(null);
      setPrompt({ phase: 'detected', detection });
    });
    // The meeting's audio came back during the countdown: keep recording.
    const offResumed = onNativeMeetingResumed(() => {
      setPrompt((current) => (current?.phase === 'ended' ? null : current));
    });
    const offEnded = onNativeMeetingEnded((detection) => {
      setError(null);
      setPrompt((current) => {
        if (detection.recording) return { phase: 'ended', detection };
        // A prompt for a meeting that already ended is no longer useful.
        return current?.detection.detectionId === detection.detectionId ? null : current;
      });
    });
    // A meeting announced before this page loaded; read after subscribing so
    // an announcement in between is not lost. Live events take precedence.
    void getMeetingDetection().then((state) => {
      if (!cancelled && state?.enabled && state.active) {
        setPrompt((current) => current ?? { phase: 'detected', detection: state.active! });
      }
    });
    return () => {
      cancelled = true;
      offDetected();
      offEnded();
      offResumed();
    };
  }, [isAuthenticated]);

  if (!prompt) return null;
  const { phase, detection } = prompt;

  const close = (action: 'dismiss' | null) => {
    if (action) void respondMeetingDetection(detection.detectionId, action).catch(() => undefined);
    setPrompt(null);
    setError(null);
  };

  const confirm = async (): Promise<void> => {
    setBusy(true);
    setError(null);
    try {
      const reply =
        phase === 'detected'
          ? await respondMeetingDetection(detection.detectionId, 'start', meetingTitle(detection.label))
          : await respondMeetingDetection(detection.detectionId, 'stop');
      if ('error' in reply && !reply.ok) {
        setError(reply.error.message);
      } else {
        setPrompt(null);
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  const heading =
    phase === 'detected' ? `${detection.label} 회의가 감지됐어요` : `${detection.label} 회의가 끝난 것 같아요`;
  const body =
    phase === 'detected'
      ? '녹음을 시작하면 시스템 소리와 내 마이크를 함께 녹음합니다. 참석자에게 녹음 사실을 알려주세요.'
      : countdown && countdown.id === detection.detectionId && countdown.remaining > 0
        ? `${countdown.remaining}초 후 녹음을 종료하고 업로드합니다.`
        : '녹음을 종료하고 업로드하는 중입니다.';

  return (
    <div
      role="dialog"
      aria-live="polite"
      aria-label={heading}
      className="fixed right-4 bottom-24 lg:bottom-6 z-50 w-[min(22rem,calc(100vw-2rem))] rounded-xl border border-slate-200 dark:border-white/10 bg-white dark:bg-surface shadow-2xl p-4 flex flex-col gap-3"
    >
      <div className="flex items-start gap-3">
        <span className="material-symbols-outlined text-primary">{phase === 'detected' ? 'mic' : 'stop'}</span>
        <div className="flex flex-col gap-1">
          <p className="text-sm font-bold text-slate-900 dark:text-text-main">{heading}</p>
          <p className="text-xs text-slate-600 dark:text-text-secondary">{body}</p>
          {error && <p className="text-xs text-red-600 dark:text-red-400">{error}</p>}
        </div>
      </div>
      <div className="flex justify-end gap-2">
        <button
          type="button"
          onClick={() => close(phase === 'detected' ? 'dismiss' : null)}
          disabled={busy}
          className="min-h-9 px-3 rounded-lg text-sm text-slate-600 dark:text-text-secondary hover:bg-slate-100 dark:hover:bg-white/5 disabled:opacity-50"
        >
          {phase === 'detected' ? '무시' : '계속 녹음'}
        </button>
        <button
          type="button"
          onClick={() => void confirm()}
          disabled={busy}
          className="min-h-9 px-3 rounded-lg text-sm font-bold text-white bg-primary hover:bg-primary-hover disabled:opacity-50"
        >
          {busy ? '처리 중…' : phase === 'detected' ? '녹음 시작' : '지금 종료'}
        </button>
      </div>
    </div>
  );
}
