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
 * summary column next to action items). Width persists to localStorage
 * across meetings/sessions.
 *
 * `reserve` (optional, needs the returned `containerRef` attached to the
 * row element) caps the width to that row's *current* rendered width minus
 * `reserve` px (space owed to the divider, gaps, and the sibling column's
 * own min-width) -- re-measured via ResizeObserver. `containerRef` is a
 * callback ref (not a plain RefObject) so the hook's own state -- not a
 * `.current` mutation React doesn't consider a dependency change -- drives
 * when that effect (re)attaches; a `useRef` passed in from the caller and
 * read via `ref.current` in a dependency array can silently never fire if
 * the element wasn't there yet on the render where deps were last diffed
 * (e.g. behind a loading early-return). The computed ceiling is always
 * floored at `min` (`Math.max(min, ...)`) so a narrow container never makes
 * max < min -- that inversion is what previously turned clamp() into a
 * permanent dead-zone, since `Math.min(max, Math.max(min, n))` with
 * max < min always returns max regardless of n.
 */
export function useResizablePanel(
  storageKey: string,
  defaultWidth: number,
  min: number,
  max: number,
  anchor: 'left' | 'right' = 'right',
  reserve?: number,
) {
  const [width, setWidth] = useState(defaultWidth);
  const widthRef = useRef(defaultWidth);
  const [container, setContainer] = useState<HTMLElement | null>(null);
  const containerRef = useCallback((el: HTMLElement | null) => setContainer(el), []);

  const effectiveMax = () => {
    if (container && reserve !== undefined) {
      const available = container.getBoundingClientRect().width - reserve;
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
    if (!container || reserve === undefined) return;
    const reclamp = () => {
      const next = clamp(widthRef.current, min, effectiveMax());
      if (next !== widthRef.current) {
        widthRef.current = next;
        setWidth(next);
      }
    };
    reclamp();
    const ro = new ResizeObserver(reclamp);
    ro.observe(container);
    return () => ro.disconnect();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [container, reserve]);

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

  return { width, startDrag, containerRef };
}
