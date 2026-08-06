'use client';

import { useCallback, useEffect, useRef, useState } from 'react';

function clamp(n: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, n));
}

/**
 * Drag-to-resize width for a panel. `anchor: 'right'` (default) is for a
 * panel pinned to the right edge (dragging the divider left grows it, e.g.
 * the meeting detail page's reference sidebar); `anchor: 'left'` is for a
 * panel pinned to the left edge (dragging right grows it, e.g. the AI
 * summary column next to action items).
 *
 * The returned `width` is a *derived* value, never itself persisted or
 * mutated directly: `preferred` (the user's actual intent, clamped to the
 * static [min, max] and the only thing written to localStorage) is combined
 * at render time with `ceiling` (the container's current width minus
 * `reserve`, re-measured via ResizeObserver on the element the returned
 * `containerRef` callback ref is attached to) to produce
 * `width = clamp(preferred, min, ceiling)`. Deriving instead of reclamping a
 * stored width in place avoids a ratchet: if `ceiling` shrinks and later
 * grows back, `preferred` was never overwritten, so `width` recovers on its
 * own instead of staying stuck at whatever the narrow moment forced it to.
 *
 * `fits` reports whether the measured container can hold `min` plus
 * `reserve` at all. Callers doing a side-by-side vs stacked layout switch
 * should branch on it instead of a viewport breakpoint: a breakpoint knows
 * only the viewport, not what else (app sidebar, another resizable panel)
 * is already claiming width at that viewport, so any fixed breakpoint is
 * wrong for some sidebar/aside combination. `fits` starts false until the
 * container is actually measured -- a first paint briefly stacked is
 * harmless, a first paint briefly side-by-side on a too-narrow container
 * visibly overflows.
 */
export function useResizablePanel(
  storageKey: string,
  defaultWidth: number,
  min: number,
  max: number,
  anchor: 'left' | 'right' = 'right',
  reserve?: number,
) {
  const [preferred, setPreferred] = useState(defaultWidth);
  const preferredRef = useRef(defaultWidth);
  const [container, setContainer] = useState<HTMLElement | null>(null);
  const containerRef = useCallback((el: HTMLElement | null) => setContainer(el), []);
  // 0, not max: until the container is actually measured, report "doesn't
  // fit" (see the fits doc note above) rather than assuming it does.
  const [ceiling, setCeiling] = useState(reserve === undefined ? max : 0);

  const measureCeiling = () => {
    if (container && reserve !== undefined) {
      return Math.min(max, Math.max(0, container.getBoundingClientRect().width - reserve));
    }
    return max;
  };

  useEffect(() => {
    const stored = localStorage.getItem(storageKey);
    const n = stored ? parseInt(stored, 10) : NaN;
    const initial = clamp(isNaN(n) ? defaultWidth : n, min, max);
    preferredRef.current = initial;
    setPreferred(initial);
    // Only read the stored value once on mount.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [storageKey]);

  useEffect(() => {
    if (!container || reserve === undefined) {
      setCeiling(max);
      return;
    }
    const remeasure = () => setCeiling(measureCeiling());
    remeasure();
    const ro = new ResizeObserver(remeasure);
    ro.observe(container);
    return () => ro.disconnect();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [container, reserve, max]);

  const width = clamp(preferred, min, Math.max(min, ceiling));
  const fits = reserve === undefined || ceiling >= min;

  const activeDragCleanup = useRef<(() => void) | null>(null);

  const startDrag = (e: React.MouseEvent) => {
    e.preventDefault();
    activeDragCleanup.current?.(); // re-entry: end any drag already active
    const startX = e.clientX;
    const startWidth = width;

    const onMove = (ev: MouseEvent) => {
      if (ev.buttons === 0) return onUp(); // mouseup happened outside the window
      const delta = ev.clientX - startX;
      const next = clamp(anchor === 'right' ? startWidth - delta : startWidth + delta, min, max);
      preferredRef.current = next;
      setPreferred(next);
    };
    const onUp = () => {
      window.removeEventListener('mousemove', onMove);
      window.removeEventListener('mouseup', onUp);
      activeDragCleanup.current = null;
      localStorage.setItem(storageKey, String(preferredRef.current));
    };
    window.addEventListener('mousemove', onMove);
    window.addEventListener('mouseup', onUp);
    activeDragCleanup.current = onUp;
  };

  // If the component unmounts mid-drag (route change while dragging), the
  // mousemove/mouseup listeners above would otherwise outlive it and keep
  // calling setState on an unmounted component.
  useEffect(() => {
    return () => activeDragCleanup.current?.();
  }, []);

  return { width, startDrag, containerRef, fits };
}
