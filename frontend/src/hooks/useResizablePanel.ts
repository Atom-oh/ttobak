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
 * `containerRef`+`reserve` (optional) additionally cap the width to the
 * container's *current* rendered width minus `reserve` px (space owed to the
 * divider, gaps, and the sibling column's own min-width) -- re-measured via
 * ResizeObserver rather than once at mount, since the container may not be
 * rendered yet (behind a loading early-return) when the mount effect first
 * runs, and the viewport can resize at any time afterward. The computed
 * ceiling is always floored at `min` (`Math.max(min, ...)`) so a narrow
 * container never makes max < min -- that inversion is what previously
 * turned clamp() into a permanent dead-zone, since
 * `Math.min(max, Math.max(min, n))` with max < min always returns max
 * regardless of n.
 */
export function useResizablePanel(
  storageKey: string,
  defaultWidth: number,
  min: number,
  max: number,
  anchor: 'left' | 'right' = 'right',
  containerRef?: React.RefObject<HTMLElement | null>,
  reserve?: number,
) {
  const [width, setWidth] = useState(defaultWidth);
  const widthRef = useRef(defaultWidth);

  const effectiveMax = () => {
    if (containerRef?.current && reserve !== undefined) {
      const available = containerRef.current.getBoundingClientRect().width - reserve;
      return Math.max(min, Math.min(max, available));
    }
    return max;
  };

  useEffect(() => {
    const stored = localStorage.getItem(storageKey);
    const n = stored ? parseInt(stored, 10) : NaN;
    const clamped = clamp(isNaN(n) ? defaultWidth : n, min, effectiveMax());
    widthRef.current = clamped;
    setWidth(clamped);
    // Only read the stored value once on mount; re-clamping to the
    // container's size is handled by the ResizeObserver effect below.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [storageKey]);

  useEffect(() => {
    const el = containerRef?.current;
    if (!el || reserve === undefined) return;
    const reclamp = () => {
      const next = clamp(widthRef.current, min, effectiveMax());
      if (next !== widthRef.current) {
        widthRef.current = next;
        setWidth(next);
      }
    };
    reclamp();
    const ro = new ResizeObserver(reclamp);
    ro.observe(el);
    return () => ro.disconnect();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [containerRef?.current, reserve]);

  const activeDragCleanup = useRef<(() => void) | null>(null);

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
      activeDragCleanup.current = null;
      localStorage.setItem(storageKey, String(widthRef.current));
    };
    window.addEventListener('mousemove', onMove);
    window.addEventListener('mouseup', onUp);
    activeDragCleanup.current = onUp;
  };

  // If the component unmounts mid-drag (route change while dragging), the
  // mousemove/mouseup listeners above would otherwise outlive it and keep
  // calling setWidth on an unmounted component.
  useEffect(() => {
    return () => activeDragCleanup.current?.();
  }, []);

  return { width, startDrag };
}
