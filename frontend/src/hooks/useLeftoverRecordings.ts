'use client';

import { useCallback, useEffect, useState } from 'react';
import { isTauri, listLeftoverRecordings, type TauriLeftoverRecording } from '@/lib/tauri';

/**
 * Leftover native recordings the mac app adopted at startup from a PREVIOUS
 * run (crash / force quit) — see `TauriLeftoverRecording` and ADR-024.
 *
 * Lives on the /record page rather than in a global provider on purpose:
 * the only thing that can act on a leftover (create a meeting, run the
 * notes step, upload through the existing native flow) is that page's
 * `usePostRecording` instance, and every other `isTauri()` branch already
 * lives there. Consequence: leftovers are surfaced when the user opens
 * /record, not at app launch — which is also where a mac-app user goes to
 * record anyway.
 *
 * Outside Tauri this never calls anything and stays empty. Against an
 * installed Rust build that predates the command, `listLeftoverRecordings`
 * resolves to `[]` (nothing the SPA could do about those files anyway).
 */
export function useLeftoverRecordings() {
  const [leftovers, setLeftovers] = useState<TauriLeftoverRecording[]>([]);

  const refresh = useCallback(async () => {
    if (!isTauri()) return;
    try {
      setLeftovers(await listLeftoverRecordings());
    } catch (err) {
      // Non-fatal: the card simply doesn't appear. The files stay on disk
      // and are re-offered on the next launch (or deleted after 48h).
      console.warn('listLeftoverRecordings failed', err);
    }
  }, []);

  useEffect(() => {
    // Async fetch with a cancel flag (not a bare `refresh()` call): the
    // setState lands in the promise callback, never synchronously inside the
    // effect, and an unmount before the IPC round-trip resolves is ignored.
    if (!isTauri()) return;
    let cancelled = false;
    listLeftoverRecordings()
      .then((items) => {
        if (!cancelled) setLeftovers(items);
      })
      .catch((err) => {
        console.warn('listLeftoverRecordings failed', err);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const remove = useCallback((path: string) => {
    setLeftovers((prev) => prev.filter((item) => item.path !== path));
  }, []);

  const dismissAll = useCallback(() => setLeftovers([]), []);

  return { leftovers, refresh, remove, dismissAll };
}
