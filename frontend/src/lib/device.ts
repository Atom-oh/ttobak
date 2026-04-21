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
