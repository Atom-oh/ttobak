// Live-caption spoken-form replacement (import-free for node:test).
//
// The dictionary GET lists the Hangul spoken forms of the caller's vocabulary
// terms (`liveReplacements`, ADR-008). The custom vocabulary only raises the
// chance that Transcribe writes the term; when it still spells the spoken
// form in Hangul, the caption is rewritten here.

export interface LiveReplacement { heard: string; displayAs: string }

/** Hangul syllables in one or more space-separated words, two syllables or more. */
const HEARD = /^[가-힣]+(?: [가-힣]+)*$/;
const CONTROL = new RegExp('[\\u0000-\\u001F\\u007F-\\u009F\\u2028\\u2029]');
const MAX_PAIRS = 500;

/** Well-formed pairs from a dictionary response; undefined when there are none. */
export function liveReplacementsFrom(value: unknown): LiveReplacement[] | undefined {
  if (!Array.isArray(value)) return undefined;
  const pairs: LiveReplacement[] = [];
  for (const item of value) {
    if (pairs.length === MAX_PAIRS) break;
    const { heard, displayAs } = (item ?? {}) as { heard?: unknown; displayAs?: unknown };
    if (typeof heard !== 'string' || typeof displayAs !== 'string') continue;
    if (!HEARD.test(heard) || heard.replace(/ /g, '').length < 2) continue;
    if (!displayAs.trim() || displayAs.length > 256 || CONTROL.test(displayAs)) continue;
    pairs.push({ heard, displayAs });
  }
  return pairs.length ? pairs : undefined;
}

/**
 * A function that rewrites every spoken form in a caption to its term, or
 * undefined when there is nothing to rewrite. A form matches at a word start
 * (so not inside a longer word), with or without its spaces, and may be
 * followed by a particle. The longest form wins.
 */
export function compileLiveReplacements(pairs: readonly LiveReplacement[] | undefined): ((text: string) => string) | undefined {
  const byHeard = new Map<string, string>();
  for (const pair of pairs ?? []) {
    const key = pair.heard.replace(/ /g, '');
    if (HEARD.test(pair.heard) && key.length >= 2 && !byHeard.has(key)) byHeard.set(key, pair.displayAs);
  }
  if (!byHeard.size) return undefined;
  const forms = [...new Set((pairs ?? []).map((pair) => pair.heard).filter((heard) => byHeard.has(heard.replace(/ /g, ''))))]
    .sort((left, right) => right.length - left.length)
    .map((heard) => heard.split(' ').join('\\s*'));
  const pattern = new RegExp(`(^|[^\\uAC00-\\uD7A3A-Za-z0-9])(${forms.join('|')})`, 'g');
  return (text) => text.replace(pattern, (whole: string, before: string, heard: string) => {
    const term = byHeard.get(heard.replace(/\s/g, ''));
    return term === undefined ? whole : before + term;
  });
}
