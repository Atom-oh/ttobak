// The interpret screen's in-app copy of the floating caption windows: one
// list per caption language, kept in memory only (never stored or logged).

/** The caption fields the list needs; the rest passes through. */
export interface CaptionLine {
  id: string;
  order: number;
  original: string;
  translated: string;
  status: string;
}

/** Most lines kept per language. */
export const MAX_CAPTION_LINES = 200;

/**
 * Adds or replaces one caption line. A skipped line, and a failed or empty
 * one, is removed, as in the caption window. Lines stay in speaking order.
 */
export function upsertCaptionLine<Line extends CaptionLine>(lines: Line[], line: Line): Line[] {
  const rest = lines.filter((item) => item.id !== line.id);
  if (line.status === 'skipped' || line.status === 'error' || !line.translated) {
    return rest.length === lines.length ? lines : rest;
  }
  return [...rest, line].sort((left, right) => left.order - right.order).slice(-MAX_CAPTION_LINES);
}
