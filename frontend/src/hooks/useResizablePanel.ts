'use client';

import { useEffect, useRef, useState } from 'react';

function clamp(n: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, n));
}

/**
 * Drag-to-resize width for a panel. `anchor: 'right'` (default) is for a
 * panel pinned to the right edge (dragging the divider left grows it, e.g.
 * the meeting detail page's reference sidebar); `anchor: 'left'` is for a
 * panel pinned to the left edge (dragging right grows it, e.g. the AI
 * summary column next to action items). Width persists to localStorage
 * across meetings/sessions.
 *
 * `containerRef`+`maxRatio` (optional) additionally cap the width to a
 * fraction of the containing row's *current* rendered width, re-measured on
 * mount and at drag start. Without this, `max` is a static px ceiling that
 * doesn't know about the viewport -- on a narrower lg screen the panel can
 * overflow its row or (if a CSS-side clamp is layered on top instead) create
 * a drag dead-zone where the stored px value already exceeds what CSS
 * allows, so moving the mouse does nothing.
 */
export function useResizablePanel(
  storageKey: string,
  defaultWidth: number,
  min: number,
  max: number,
  anchor: 'left' | 'right' = 'right',
  containerRef?: React.RefObject<HTMLElement | null>,
  maxRatio?: number,
) {
  const [width, setWidth] = useState(defaultWidth);
  const widthRef = useRef(defaultWidth);

  const effectiveMax = () => {
    if (containerRef?.current && maxRatio) {
      return Math.min(max, containerRef.current.getBoundingClientRect().width * maxRatio);
    }
    return max;
  };

  useEffect(() => {
    const stored = localStorage.getItem(storageKey);
    const n = stored ? parseInt(stored, 10) : NaN;
    const clamped = clamp(isNaN(n) ? defaultWidth : n, min, effectiveMax());
    widthRef.current = clamped;
    setWidth(clamped);
    // Only read the stored value / re-clamp to the container once on mount.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [storageKey]);

  const startDrag = (e: React.MouseEvent) => {
    e.preventDefault();
    const startX = e.clientX;
    const dragMax = effectiveMax();
    const startWidth = clamp(widthRef.current, min, dragMax);
    widthRef.current = startWidth;
    setWidth(startWidth);

    const onMove = (ev: MouseEvent) => {
      const delta = ev.clientX - startX;
      const next = clamp(anchor === 'right' ? startWidth - delta : startWidth + delta, min, dragMax);
      widthRef.current = next;
      setWidth(next);
    };
    const onUp = () => {
      window.removeEventListener('mousemove', onMove);
      window.removeEventListener('mouseup', onUp);
      localStorage.setItem(storageKey, String(widthRef.current));
    };
    window.addEventListener('mousemove', onMove);
    window.addEventListener('mouseup', onUp);
  };

  return { width, startDrag };
}
