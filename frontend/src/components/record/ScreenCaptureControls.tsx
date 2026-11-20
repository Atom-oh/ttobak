'use client';

import { useEffect, useRef, useState } from 'react';
import {
  captureScreenFrame,
  ScreenCaptureCancelled,
  startSlideCapture,
  supportsScreenCapture,
  type SlideCaptureSession,
} from '@/lib/screenCapture';
import { MAX_SLIDES_PER_RECORDING } from '@/lib/slideDetect';

const LIMIT_NOTICE = `이번 녹음의 슬라이드 캡처 한도(${MAX_SLIDES_PER_RECORDING}장)에 도달해 자동 캡처를 멈췄습니다.`;

interface ScreenCaptureControlsProps {
  /** Receives each captured image; the parent uploads it as an attachment. */
  onCaptureImage?: (file: File) => void;
  disabled?: boolean;
}

/**
 * Desktop recording controls: a one-shot screen capture and an automatic slide
 * capture that follows a shared screen (for example a Zoom window) and uploads a
 * thumbnail whenever the slide changes. Unmounting (recording ended) stops it.
 */
export function ScreenCaptureControls({ onCaptureImage, disabled }: ScreenCaptureControlsProps) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [autoActive, setAutoActive] = useState(false);
  const [slideCount, setSlideCount] = useState(0);
  const [notice, setNotice] = useState<string | null>(null);
  const sessionRef = useRef<SlideCaptureSession | null>(null);
  const onCaptureRef = useRef(onCaptureImage);
  // Uploads so far in this recording, across every auto-capture session.
  const totalSlidesRef = useRef(0);
  // False after unmount, so a picker that resolves later is stopped at once.
  const mountedRef = useRef(true);

  useEffect(() => {
    onCaptureRef.current = onCaptureImage;
  }, [onCaptureImage]);
  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      sessionRef.current?.stop();
      sessionRef.current = null;
    };
  }, []);

  const ensureSupported = () => {
    if (supportsScreenCapture()) return true;
    setError('이 브라우저는 화면 캡처를 지원하지 않습니다.');
    return false;
  };

  const handleSingleCapture = async () => {
    if (busy) return;
    setError(null);
    if (!ensureSupported()) return;
    setBusy(true);
    try {
      const file = await captureScreenFrame();
      if (mountedRef.current) onCaptureRef.current?.(file);
    } catch (err) {
      if (!(err instanceof ScreenCaptureCancelled)) {
        console.error('Screen capture failed:', err);
        setError('화면을 캡처하지 못했습니다.');
      }
    } finally {
      setBusy(false);
    }
  };

  const handleAutoToggle = async () => {
    if (sessionRef.current) {
      sessionRef.current.stop();
      return;
    }
    if (busy) return;
    setError(null);
    setNotice(null);
    if (!ensureSupported()) return;
    const remaining = MAX_SLIDES_PER_RECORDING - totalSlidesRef.current;
    if (remaining <= 0) {
      setNotice(LIMIT_NOTICE);
      return;
    }
    setBusy(true);
    try {
      let ended = false;
      const session = await startSlideCapture(
        {
          onSlide: (file) => {
            totalSlidesRef.current += 1;
            setSlideCount(totalSlidesRef.current);
            onCaptureRef.current?.(file);
          },
          onEnd: (reason) => {
            ended = true;
            sessionRef.current = null;
            if (!mountedRef.current) return;
            setAutoActive(false);
            if (reason === 'limit') setNotice(LIMIT_NOTICE);
            if (reason === 'error') setError('슬라이드 캡처 중 오류가 발생해 멈췄습니다.');
          },
        },
        remaining,
      );
      if (!mountedRef.current) {
        // Recording ended while the picker was open: never keep capturing.
        session.stop();
        return;
      }
      if (ended) return;
      sessionRef.current = session;
      setSlideCount(totalSlidesRef.current);
      setAutoActive(true);
    } catch (err) {
      if (!(err instanceof ScreenCaptureCancelled)) {
        console.error('Slide capture failed to start:', err);
        setError('슬라이드 자동 캡처를 시작하지 못했습니다.');
      }
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="hidden lg:flex flex-col items-center justify-center gap-2">
      <div className="flex items-center gap-2">
        <button
          onClick={handleSingleCapture}
          disabled={disabled || busy}
          className="flex items-center gap-2 px-4 py-2 bg-primary/10 rounded-lg hover:bg-primary/20 transition-colors text-primary font-semibold text-sm disabled:opacity-50"
        >
          <span className="material-symbols-outlined">screenshot</span>
          Capture Screen
        </button>
        <button
          onClick={handleAutoToggle}
          disabled={disabled || (busy && !autoActive)}
          aria-pressed={autoActive}
          className={`flex items-center gap-2 px-4 py-2 rounded-lg transition-colors font-semibold text-sm disabled:opacity-50 ${
            autoActive ? 'bg-primary text-white hover:bg-primary/90' : 'bg-primary/10 text-primary hover:bg-primary/20'
          }`}
        >
          <span className="material-symbols-outlined">{autoActive ? 'stop' : 'slideshow'}</span>
          {autoActive ? '슬라이드 자동 캡처 중지' : '슬라이드 자동 캡처'}
        </button>
      </div>
      {autoActive && (
        <p className="text-xs text-slate-500 dark:text-slate-400 flex items-center gap-1">
          <span className="w-2 h-2 rounded-full bg-red-500 animate-pulse" />
          공유 화면의 슬라이드가 바뀔 때마다 캡처합니다 · {slideCount}장
        </p>
      )}
      {!autoActive && notice && <p className="text-xs text-slate-500 dark:text-slate-400">{notice}</p>}
      {error && <p className="text-xs text-red-500">{error}</p>}
    </div>
  );
}
