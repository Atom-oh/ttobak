/**
 * Textareas grow with CSS `field-sizing: content` (`field-sizing-content`).
 * Browsers without it (Safari before 26.2, so older macOS app WebViews) get
 * the same result by measuring scrollHeight; supporting browsers skip it.
 */
export function fitToContent(el: HTMLTextAreaElement | null, maxPx = Infinity): void {
  if (!el || CSS.supports('field-sizing', 'content')) return;
  el.style.height = 'auto';
  el.style.height = `${Math.min(el.scrollHeight, maxPx)}px`;
}
