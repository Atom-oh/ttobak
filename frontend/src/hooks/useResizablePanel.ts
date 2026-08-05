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
 */
export function useResizablePanel(
  storageKey: string,
  defaultWidth: number,
  min: number,
  max: number,
  anchor: 'left' | 'right' = 'right',
) {
  const [width, setWidth] = useState(defaultWidth);
  const widthRef = useRef(defaultWidth);

  useEffect(() => {
    const stored = localStorage.getItem(storageKey);
    const n = stored ? parseInt(stored, 10) : NaN;
    if (!isNaN(n)) {
      const clamped = clamp(n, min, max);
      widthRef.current = clamped;
      setWidth(clamped);
    }
    // Only read the stored value once on mount.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [storageKey]);

  const startDrag = (e: React.MouseEvent) => {
    e.preventDefault();
    const startX = e.clientX;
    const startWidth = widthRef.current;

    const onMove = (ev: MouseEvent) => {
      const delta = ev.clientX - startX;
      const next = clamp(anchor === 'right' ? startWidth - delta : startWidth + delta, min, max);
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
