import type { KeyboardEvent } from 'react';

/**
 * True for a keydown that belongs to an IME composition, such as the Enter
 * that commits a Hangul syllable. Enter handlers must ignore it, or they act
 * on half-composed text. `keyCode === 229` covers Safari (and the macOS app's
 * WKWebView), which clears `isComposing` before this keydown (WebKit 165004).
 */
export function isImeKey(event: KeyboardEvent): boolean {
  return event.nativeEvent.isComposing || event.keyCode === 229;
}
