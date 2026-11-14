// Speaker labels for the interpret screen's captions. Transcribe Streaming
// numbers the voices it tells apart ("0", "1", ...) per stream; the labels
// are kept in memory only, like the captions themselves.

/** Most speaker labels remembered in one run. */
export const MAX_SPEAKER_ENTRIES = 1000;

/** Distinct speaker colours before they repeat. */
export const SPEAKER_COLOR_COUNT = 6;

/**
 * The speaker who said most of one result. Transcribe labels each word; a
 * result can hold a few words of a second voice at its edge, and words with
 * no label (punctuation, partial results) do not count.
 */
export function dominantSpeaker(items: ReadonlyArray<{ Speaker?: string }> | undefined): string | undefined {
  const counts = new Map<string, number>();
  for (const item of items ?? []) {
    if (item.Speaker) counts.set(item.Speaker, (counts.get(item.Speaker) ?? 0) + 1);
  }
  let best: string | undefined;
  let bestCount = 0;
  for (const [speaker, count] of counts) {
    if (count > bestCount) {
      best = speaker;
      bestCount = count;
    }
  }
  return best;
}

/** Zero-based speaker number of a Transcribe label, or null when it is not one. */
export function speakerNumber(label: string | undefined): number | null {
  if (!label || !/^\d{1,3}$/.test(label)) return null;
  return Number(label);
}

/** Colour slot of a speaker label, or null for an unlabelled line. */
export function speakerColorIndex(label: string | undefined): number | null {
  const number = speakerNumber(label);
  return number === null ? null : number % SPEAKER_COLOR_COUNT;
}

/**
 * Records one segment's speaker, dropping the oldest entries past the cap.
 * Returns the same object when nothing changes.
 */
export function rememberSpeaker(
  speakers: Record<string, string>, id: string, speaker: string | undefined, max = MAX_SPEAKER_ENTRIES,
): Record<string, string> {
  if (!speaker || speakers[id] === speaker) return speakers;
  const entries = Object.entries(speakers).filter(([key]) => key !== id);
  entries.push([id, speaker]);
  return Object.fromEntries(entries.slice(-max));
}
