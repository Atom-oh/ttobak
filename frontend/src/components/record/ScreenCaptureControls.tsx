'use client';

import { useEffect, useRef, useState } from 'react';
import {
  captureScreenFrame,
  ScreenCaptureCancelled,
  startSlideCapture,
  supportsScreenCapture,
  type SlideCaptureSession,
} from '@/lib/screenCapture';

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

  useEffect(() => {
    onCaptureRef.current = onCaptureImage;
  }, [onCaptureImage]);
  useEffect(() => () => sessionRef.current?.stop(), []);

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
      onCaptureRef.current?.(await captureScreenFrame());
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
    setBusy(true);
    try {
      const session = await startSlideCapture({
        onSlide: (file, index) => {
          setSlideCount(index);
          onCaptureRef.current?.(file);
        },
        onEnd: (reason) => {
          sessionRef.current = null;
          setAutoActive(false);
          if (reason === 'limit') setNotice('슬라이드 캡처 한도에 도달해 자동 캡처를 멈췄습니다.');
          if (reason === 'error') setError('슬라이드 캡처 중 오류가 발생해 멈췄습니다.');
        },
      });
      sessionRef.current = session;
      setSlideCount(0);
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
