'use client';

import { GRID_H, GRID_W, SlideChangeDetector, signatureFromRgba } from '@/lib/slideDetect';

/** True when the browser can prompt for a screen, window or tab to capture. */
export function supportsScreenCapture(): boolean {
  return typeof navigator !== 'undefined' && typeof navigator.mediaDevices?.getDisplayMedia === 'function';
}

/** Thrown when the user dismisses the screen picker; callers stay silent. */
export class ScreenCaptureCancelled extends Error {
  constructor() {
    super('Screen capture cancelled');
    this.name = 'ScreenCaptureCancelled';
  }
}

/** Longest edge of a saved slide thumbnail; enough for the model to read text. */
export const SLIDE_MAX_EDGE = 1280;
const SAMPLE_INTERVAL_MS = 1000;
const SAMPLE_SCALE = 4; // signature cells are averaged from 4x4 sample pixels

/** Opens the screen picker. Must be called from a user gesture. */
async function openDisplayStream(): Promise<MediaStream> {
  try {
    // selfBrowserSurface/surfaceSwitching are Chromium hints; others ignore them.
    const options = {
      video: { frameRate: { ideal: 5, max: 10 } },
      audio: false,
      selfBrowserSurface: 'exclude',
      surfaceSwitching: 'include',
    } as DisplayMediaStreamOptions;
    return await navigator.mediaDevices.getDisplayMedia(options);
  } catch (err) {
    if (err instanceof DOMException && (err.name === 'NotAllowedError' || err.name === 'AbortError')) {
      throw new ScreenCaptureCancelled();
    }
    throw err;
  }
}

/** A detached video element that keeps decoding the capture stream. */
async function attachVideo(stream: MediaStream): Promise<HTMLVideoElement> {
  const video = document.createElement('video');
  video.muted = true;
  video.playsInline = true;
  video.srcObject = stream;
  await new Promise<void>((resolve, reject) => {
    const timer = window.setTimeout(() => reject(new Error('Screen capture timed out')), 5000);
    video.onloadeddata = () => {
      window.clearTimeout(timer);
      resolve();
    };
    video.play().catch((e) => {
      window.clearTimeout(timer);
      reject(e);
    });
  });
  return video;
}

function encodeFrame(video: HTMLVideoElement, maxEdge: number | null, quality: number, name: string): Promise<File> {
  const srcW = video.videoWidth;
  const srcH = video.videoHeight;
  if (!srcW || !srcH) return Promise.reject(new Error('Screen frame unavailable'));
  const scale = maxEdge ? Math.min(1, maxEdge / Math.max(srcW, srcH)) : 1;
  const canvas = document.createElement('canvas');
  canvas.width = Math.round(srcW * scale);
  canvas.height = Math.round(srcH * scale);
  const ctx = canvas.getContext('2d');
  if (!ctx) return Promise.reject(new Error('Screen frame unavailable'));
  ctx.imageSmoothingQuality = 'high';
  ctx.drawImage(video, 0, 0, canvas.width, canvas.height);
  return new Promise((resolve, reject) => {
    canvas.toBlob(
      (blob) => (blob ? resolve(new File([blob], name, { type: 'image/jpeg' })) : reject(new Error('Screen frame encoding failed'))),
      'image/jpeg',
      quality,
    );
  });
}

/**
 * Prompts for a screen, window or tab, grabs a single frame as a JPEG and
 * immediately stops the capture stream. The stream is separate from any
 * recording stream, so stopping it never affects audio capture.
 */
export async function captureScreenFrame(): Promise<File> {
  const stream = await openDisplayStream();
  let video: HTMLVideoElement | null = null;
  try {
    video = await attachVideo(stream);
    // A short timer, not requestAnimationFrame: rAF never fires in a hidden tab.
    await new Promise((r) => window.setTimeout(r, 100));
    return await encodeFrame(video, null, 0.92, `screen_${Date.now()}.jpg`);
  } finally {
    stream.getTracks().forEach((t) => t.stop());
    if (video) video.srcObject = null;
  }
}

export interface SlideCaptureHandlers {
  /** A new stable slide; the file name starts with "slide_". */
  onSlide: (file: File, index: number) => void;
  /** Capture ended: user stopped sharing, stop() was called, cap reached or error. */
  onEnd: (reason: 'stopped' | 'limit' | 'error') => void;
}

export interface SlideCaptureSession {
  stop: () => void;
}

/**
 * Keeps one picked screen/window open and uploads a downscaled frame each time
 * the shown content changes and settles (see SlideChangeDetector). Must be
 * started from a user gesture.
 */
export async function startSlideCapture(handlers: SlideCaptureHandlers, maxCaptures: number): Promise<SlideCaptureSession> {
  const stream = await openDisplayStream();
  let video: HTMLVideoElement;
  try {
    video = await attachVideo(stream);
  } catch (err) {
    stream.getTracks().forEach((t) => t.stop());
    throw err;
  }

  const detector = new SlideChangeDetector({ maxCaptures });
  const sampler = document.createElement('canvas');
  sampler.width = GRID_W * SAMPLE_SCALE;
  sampler.height = GRID_H * SAMPLE_SCALE;
  const sctx = sampler.getContext('2d', { willReadFrequently: true });
  let ended = false;
  let encoding = false;
  const timer = { id: undefined as number | undefined };

  const finish = (reason: 'stopped' | 'limit' | 'error') => {
    if (ended) return;
    ended = true;
    if (timer.id !== undefined) window.clearInterval(timer.id);
    stream.getTracks().forEach((t) => t.stop());
    video.srcObject = null;
    handlers.onEnd(reason);
  };

  const sample = () => {
    if (ended || encoding || !sctx || !video.videoWidth) return;
    sctx.drawImage(video, 0, 0, sampler.width, sampler.height);
    const { data } = sctx.getImageData(0, 0, sampler.width, sampler.height);
    const decision = detector.push(signatureFromRgba(data, sampler.width, sampler.height), Date.now());
    if (decision === 'limit') {
      finish('limit');
      return;
    }
    if (decision !== 'new-slide') return;
    const index = detector.captureCount;
    encoding = true;
    encodeFrame(video, SLIDE_MAX_EDGE, 0.8, `slide_${String(index).padStart(3, '0')}_${Date.now()}.jpg`)
      .then((file) => {
        if (!ended) handlers.onSlide(file, index);
      })
      .catch(() => finish('error'))
      .finally(() => {
        encoding = false;
      });
  };

  stream.getVideoTracks()[0]?.addEventListener('ended', () => finish('stopped'));
  timer.id = window.setInterval(sample, SAMPLE_INTERVAL_MS);
  return { stop: () => finish('stopped') };
}
