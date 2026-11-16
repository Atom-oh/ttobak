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

/** Lines another speaker must lead by to take over as the main speaker. */
export const MAIN_SPEAKER_LEAD = 3;

/**
 * The speaker who said the most remembered lines. Transcribe numbers voices
 * in the order it first hears them, so label "0" is often not the person who
 * talks most; the main speaker is the one shown in the default colour. The
 * current main speaker keeps the role until another leads by
 * MAIN_SPEAKER_LEAD lines, so two speakers taking turns do not swap colours
 * on every line. A tie goes to the lower label.
 */
export function mainSpeaker(speakers: Record<string, string>, current?: string): string | undefined {
  const counts = new Map<string, number>();
  for (const label of Object.values(speakers)) {
    if (speakerNumber(label) !== null) counts.set(label, (counts.get(label) ?? 0) + 1);
  }
  let best: string | undefined;
  let bestCount = 0;
  for (const [label, count] of counts) {
    if (count > bestCount || (count === bestCount && best !== undefined && Number(label) < Number(best))) {
      best = label;
      bestCount = count;
    }
  }
  const held = current === undefined ? 0 : counts.get(current) ?? 0;
  return held > 0 && bestCount - held < MAIN_SPEAKER_LEAD ? current : best;
}

/**
 * Colour slot of a line: null (the default colour) for the main speaker and
 * for an unlabelled line, otherwise the speaker's own slot.
 */
export function speakerColorSlot(label: string | undefined, main: string | undefined): number | null {
  return label !== undefined && label === main ? null : speakerColorIndex(label);
}

/**
 * Speaker number sent to a floating caption window, which gives slot 0 the
 * colour chosen in its settings: the main speaker takes slot 0 and the
 * speaker labelled 0 takes the main speaker's number. The window reduces the
 * number modulo its six colours, so the two stay distinct unless the main
 * speaker's number is a multiple of six (seven or more voices).
 */
export function windowSpeakerSlot(number: number, main: number | undefined): number {
  if (main === undefined || main === 0) return number;
  if (number === main) return 0;
  return number === 0 ? main : number;
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
