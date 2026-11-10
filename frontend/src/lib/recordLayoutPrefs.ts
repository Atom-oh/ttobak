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
