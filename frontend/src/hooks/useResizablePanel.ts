'use client';

import { useEffect, useRef, useState } from 'react';

function clamp(n: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, n));
}

/**
 * Drag-to-resize width for a panel anchored to the right edge (dragging the
 * divider left grows the panel). Width persists to localStorage across
 * meetings/sessions.
 */
export function useResizablePanel(storageKey: string, defaultWidth: number, min: number, max: number) {
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
      const next = clamp(startWidth - (ev.clientX - startX), min, max);
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
