// Per-browser layout preferences for the record page. Every storage access is
// guarded: storage can be missing or throw (private windows, blocked data).

export const SIDE_PANEL_KEY = 'ttobak-record-side-panel';
export const CAPTION_PANEL_KEY = 'ttobak-caption-panel-expanded';

export type SidePanelPref = 'open' | 'closed';

type StorageLike = Pick<Storage, 'getItem' | 'setItem'>;

function defaultStorage(): StorageLike | null {
  try {
    return typeof window === 'undefined' ? null : window.localStorage;
  } catch {
    return null;
  }
}

function read(key: string, storage?: StorageLike | null): string | null {
  try {
    const store = storage === undefined ? defaultStorage() : storage;
    return store ? store.getItem(key) : null;
  } catch {
    return null;
  }
}

function write(key: string, value: string, storage?: StorageLike | null): void {
  try {
    const store = storage === undefined ? defaultStorage() : storage;
    if (store) store.setItem(key, value);
  } catch {
    // Preference is a convenience only.
  }
}

export function readSidePanelPref(storage?: StorageLike | null): SidePanelPref {
  return read(SIDE_PANEL_KEY, storage) === 'closed' ? 'closed' : 'open';
}

export function writeSidePanelPref(pref: SidePanelPref, storage?: StorageLike | null): void {
  write(SIDE_PANEL_KEY, pref === 'closed' ? 'closed' : 'open', storage);
}

// Returns null when nothing valid is stored so the caller keeps its default.
export function readCaptionPanelExpanded(storage?: StorageLike | null): boolean | null {
  const value = read(CAPTION_PANEL_KEY, storage);
  return value === '1' ? true : value === '0' ? false : null;
}

export function writeCaptionPanelExpanded(expanded: boolean, storage?: StorageLike | null): void {
  write(CAPTION_PANEL_KEY, expanded ? '1' : '0', storage);
}

// Desktop recording cards. The Q&A/reference aside keeps its own key above
// (SIDE_PANEL_KEY) so the menu and the aside buttons share one source of truth.
export const RECORD_CARDS_KEY = 'ttobak-record-cards';

export type MainCard = 'translation' | 'summary' | 'notes' | 'transcript';
export type LayoutMenuKey = MainCard | 'sidePanel';
export type RecordCardPrefs = Readonly<Record<MainCard, boolean>>;

export const MAIN_CARDS: readonly MainCard[] = ['translation', 'summary', 'notes', 'transcript'];
export const DEFAULT_CARD_PREFS: RecordCardPrefs = Object.freeze({
  translation: true, summary: true, notes: true, transcript: true,
});

const CARD_LABELS: Record<LayoutMenuKey, string> = {
  translation: '통역',
  summary: '실시간 요약',
  notes: '메모',
  transcript: '실시간 자막',
  sidePanel: 'Q&A·자료',
};

function isShown(prefs: RecordCardPrefs, card: MainCard, translationEnabled: boolean): boolean {
  return card === 'translation' ? translationEnabled && prefs.translation : prefs[card];
}

export function visibleMainCount(prefs: RecordCardPrefs, translationEnabled: boolean): number {
  return MAIN_CARDS.filter((card) => isShown(prefs, card, translationEnabled)).length;
}

// Unknown keys and non-boolean values fall back per card; corrupt JSON or a
// stored state with every main card off falls back to the defaults.
export function readRecordCardPrefs(storage?: StorageLike | null): RecordCardPrefs {
  const raw = read(RECORD_CARDS_KEY, storage);
  if (!raw) return DEFAULT_CARD_PREFS;
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return DEFAULT_CARD_PREFS;
  }
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return DEFAULT_CARD_PREFS;
  const source = parsed as Record<string, unknown>;
  const result = { ...DEFAULT_CARD_PREFS };
  for (const card of MAIN_CARDS) {
    if (typeof source[card] === 'boolean') result[card] = source[card] as boolean;
  }
  return visibleMainCount(result, true) === 0 ? DEFAULT_CARD_PREFS : result;
}

export function writeRecordCardPrefs(prefs: RecordCardPrefs, storage?: StorageLike | null): void {
  const value: Record<MainCard, boolean> = { ...DEFAULT_CARD_PREFS };
  for (const card of MAIN_CARDS) value[card] = prefs[card] === true;
  write(RECORD_CARDS_KEY, JSON.stringify(value), storage);
}

// What is actually shown: with translation off, a translation-only choice
// would leave nothing, so the summary/notes pair is shown instead.
export function effectiveCardPrefs(prefs: RecordCardPrefs, translationEnabled: boolean): RecordCardPrefs {
  if (visibleMainCount(prefs, translationEnabled) > 0) return prefs;
  return { ...prefs, summary: true, notes: true };
}

// Returns the same object when the change would hide the last visible main card.
export function toggleCard(
  prefs: RecordCardPrefs, card: MainCard, checked: boolean, translationEnabled: boolean,
): RecordCardPrefs {
  if (prefs[card] === checked) return prefs;
  const next = { ...prefs, [card]: checked };
  if (!checked && visibleMainCount(next, translationEnabled) === 0) return prefs;
  return next;
}

export interface LayoutMenuItem {
  key: LayoutMenuKey;
  label: string;
  checked: boolean;
  /** The last visible main card: shown checked and cannot be unchecked. */
  locked: boolean;
}

export function layoutMenuItems(
  prefs: RecordCardPrefs, sidePanelOpen: boolean, translationEnabled: boolean,
): LayoutMenuItem[] {
  const lastOne = visibleMainCount(prefs, translationEnabled) === 1;
  const items: LayoutMenuItem[] = MAIN_CARDS
    .filter((card) => card !== 'translation' || translationEnabled)
    .map((card) => {
      const checked = isShown(prefs, card, translationEnabled);
      return { key: card, label: CARD_LABELS[card], checked, locked: checked && lastOne };
    });
  items.push({ key: 'sidePanel', label: CARD_LABELS.sidePanel, checked: sidePanelOpen, locked: false });
  return items;
}

export interface RecordLayout {
  translationRow: string;
  pairRow: string;
  summaryCard: string;
  notesCard: string;
  transcriptRow: string;
  /** Whether the transcript body (not only its header strip) is shown. */
  transcriptBodyShown: boolean;
  /** The header chevron is offered only when other rows can take the height. */
  transcriptCollapsible: boolean;
  /** CSS height of the desktop column; see `recordColumnHeight`. */
  columnHeight: string;
}

/** Height each visible row keeps even on a short viewport. */
export const RECORD_ROW_MIN_REM = 22;
const RECORD_ROW_GAP_REM = 1;
const RECORD_STRIP_REM = 3.5;

/**
 * The desktop column fills the viewport, but never squeezes a row below
 * RECORD_ROW_MIN_REM: three rows on a 720px-tall window would otherwise leave
 * the transcript no room for text. When the rows need more than the viewport
 * the column grows and the page scrolls, as the fixed-height layout did.
 */
export function recordColumnHeight(rows: number, collapsedStrip: boolean): string {
  const count = Math.max(1, rows);
  const gaps = count - 1 + (collapsedStrip ? 1 : 0);
  const floor = count * RECORD_ROW_MIN_REM + gaps * RECORD_ROW_GAP_REM + (collapsedStrip ? RECORD_STRIP_REM : 0);
  return `max(calc(100dvh - 8rem), ${floor}rem)`;
}

// Visible rows share the column height (flex-1); hidden cards stay mounted and
// get `hidden`. A collapsed transcript is a header strip that takes no share.
export function recordLayout(
  prefs: RecordCardPrefs, translationEnabled: boolean, transcriptExpanded: boolean,
): RecordLayout {
  const translation = isShown(prefs, 'translation', translationEnabled);
  const { summary, notes, transcript } = prefs;
  const pair = summary || notes;
  const otherRows = translation || pair;
  const transcriptBodyShown = transcript && (transcriptExpanded || !otherRows);
  const rows = (translation ? 1 : 0) + (pair ? 1 : 0) + (transcriptBodyShown ? 1 : 0);
  return {
    translationRow: translation ? 'flex-1 min-h-0' : 'hidden',
    pairRow: pair ? `flex-1 min-h-0 grid grid-rows-1 ${summary && notes ? 'grid-cols-2' : 'grid-cols-1'} gap-4` : 'hidden',
    summaryCard: summary ? 'min-h-0' : 'hidden',
    notesCard: notes ? 'min-h-0' : 'hidden',
    transcriptRow: !transcript ? 'hidden' : transcriptBodyShown ? 'flex-1 min-h-0' : 'shrink-0',
    transcriptBodyShown,
    transcriptCollapsible: otherRows,
    columnHeight: recordColumnHeight(rows, transcript && !transcriptBodyShown),
  };
}
