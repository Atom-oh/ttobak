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
 * A line's place is fixed when its ID is first seen, even with no text yet,
 * so finals whose translations finish out of order still read in speaking
 * order. The engine's own `order` is not used: closing and reopening a
 * caption window recreates its engine and restarts that counter. Lines with
 * no text are placeholders; `visibleCaptionLines` leaves them out.
 */
export function upsertCaptionLine<Line extends CaptionLine>(lines: Line[], line: Line): Line[] {
  const index = lines.findIndex((item) => item.id === line.id);
  if (line.status === 'skipped' || line.status === 'error') {
    return index < 0 ? lines : lines.filter((item) => item.id !== line.id);
  }
  if (index < 0) return [...lines, line].slice(-MAX_CAPTION_LINES);
  if (!line.translated) return lines;
  return lines.map((item, at) => (at === index ? line : item));
}

/** The lines that have text to show. */
export function visibleCaptionLines<Line extends CaptionLine>(lines: Line[]): Line[] {
  return lines.filter((item) => item.translated);
}
