'use client';

export function isIOS(): boolean {
  if (typeof window === 'undefined') return false;

  const userAgent = window.navigator.userAgent.toLowerCase();
  return /iphone|ipad|ipod/.test(userAgent);
}

export function isAndroid(): boolean {
  if (typeof window === 'undefined') return false;

  const userAgent = window.navigator.userAgent.toLowerCase();
  return /android/.test(userAgent);
}

export function isMobile(): boolean {
  if (typeof window === 'undefined') return false;

  return isIOS() || isAndroid() || window.innerWidth < 768;
}

export function isDesktop(): boolean {
  return !isMobile();
}

/**
 * True on iOS/Android specifically — NOT `isMobile()`'s broader
 * `window.innerWidth < 768`. Used to gate the Web Speech / mic-stream
 * conflict guard (SttManager.fallbackToWebSpeech and friends): the actual
 * risk is a mobile OS's SpeechRecognition implementation stealing the mic
 * track out from under a running MediaRecorder, which is a platform
 * property, not a viewport-width one — a narrow desktop browser window
 * has none of that risk and shouldn't lose Web Speech captions over it.
 *
 * `isIOS()`'s UA sniff alone misses iPadOS 13+: Safari there sends a
 * desktop-Safari UA (`Macintosh`, no `iPad` token at all) by default, so
 * the exact platform this guard exists for would otherwise slip through
 * on the iPad. `navigator.maxTouchPoints > 1` on a `Macintosh` UA is the
 * standard way to tell a touch-capable "Mac" (iPadOS) from a real one.
 */
export function hasMobileMicConflictRisk(): boolean {
  if (isIOS() || isAndroid()) return true;
  if (typeof window === 'undefined') return false;
  return /macintosh/.test(window.navigator.userAgent.toLowerCase())
    && navigator.maxTouchPoints > 1;
}

export function isSafari(): boolean {
  if (typeof window === 'undefined') return false;

  const userAgent = window.navigator.userAgent.toLowerCase();
  return /safari/.test(userAgent) && !/chrome/.test(userAgent);
}

export function supportsMediaRecorder(): boolean {
  if (typeof window === 'undefined') return false;

  return typeof MediaRecorder !== 'undefined';
}

export function getPreferredMimeType(): string {
  if (typeof MediaRecorder === 'undefined') return 'audio/webm';

  const types = [
    'audio/webm;codecs=opus',
    'audio/webm',
    'audio/mp4',
    'audio/ogg;codecs=opus',
  ];

  for (const type of types) {
    if (MediaRecorder.isTypeSupported(type)) {
      return type;
    }
  }

  return 'audio/webm';
}

export function supportsTabAudioCapture(): boolean {
  if (typeof window === 'undefined') return false;
  if (typeof navigator?.mediaDevices?.getDisplayMedia !== 'function') return false;
  return /Chrome|Edg/.test(navigator.userAgent) && !/Android/.test(navigator.userAgent);
}
