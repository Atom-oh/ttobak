// The interpret screen's in-app copy of the floating caption windows: one
// list per caption language, kept in memory only (never stored or logged).

/** The caption fields the list needs; the rest passes through. */
export interface CaptionLine {
  id: string;
  translated: string;
  status: string;
}

/** Most lines kept per language. */
export const MAX_CAPTION_LINES = 200;

/**
 * Adds or replaces one caption line, as the caption window does: a skipped or
 * failed line is removed, and an update with no text yet (a final waiting for
 * its translation) leaves the shown draft alone.
 *
 * Lines stay in arrival order: a known ID is replaced in place and a new one
 * is appended. The engine's own `order` is not used, because closing and
 * reopening a caption window recreates its engine and restarts that counter.
 */
export function upsertCaptionLine<Line extends CaptionLine>(lines: Line[], line: Line): Line[] {
  const index = lines.findIndex((item) => item.id === line.id);
  if (line.status === 'skipped' || line.status === 'error') {
    return index < 0 ? lines : lines.filter((item) => item.id !== line.id);
  }
  if (!line.translated) return lines;
  if (index >= 0) return lines.map((item, at) => (at === index ? line : item));
  return [...lines, line].slice(-MAX_CAPTION_LINES);
}
