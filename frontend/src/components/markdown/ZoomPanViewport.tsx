'use client';

import { useRef, useState, useCallback, useEffect, type ReactNode, type WheelEvent, type PointerEvent as ReactPointerEvent, type KeyboardEvent as ReactKeyboardEvent } from 'react';

const MIN_SCALE = 0.25;
const MAX_SCALE = 8;
const WHEEL_ZOOM_SENSITIVITY = 0.0015;

interface Transform {
  x: number;
  y: number;
  scale: number;
}

const IDENTITY: Transform = { x: 0, y: 0, scale: 1 };

interface ZoomPanViewportProps {
  children: ReactNode;
  /** Remounts the transform to identity when this changes (e.g. new diagram source). */
  resetKey?: string;
  className?: string;
  /** Renders the fullscreen control; omit to hide it (e.g. already inside a lightbox). */
  onFullscreen?: () => void;
  minHeightClassName?: string;
  maxHeightClassName?: string;
}

/**
 * Wraps arbitrary content (mermaid SVG, chart images) with pinch/drag/wheel
 * zoom-and-pan, driven by a CSS transform on a wrapper div — never touching
 * the children's own DOM. This matters because the mermaid SVG rendered
 * here is LLM output over untrusted meeting content (see MermaidBlock's
 * `securityLevel: 'strict'`); a library that reaches into the SVG's own
 * event handlers would widen that trust boundary. A plain transform does not.
 *
 * Plain wheel scroll is left alone (page scroll keeps working); only
 * ctrl/cmd+wheel zooms, matching map/diagram UI convention and avoiding a
 * scroll trap while reading a note that contains a diagram.
 */
export function ZoomPanViewport({
  children,
  resetKey,
  className = '',
  onFullscreen,
  minHeightClassName = 'min-h-[220px]',
  maxHeightClassName = 'max-h-[70vh]',
}: ZoomPanViewportProps) {
  const [transform, setTransform] = useState<Transform>(IDENTITY);
  const containerRef = useRef<HTMLDivElement>(null);
  const dragState = useRef<{ pointers: Map<number, { x: number; y: number }>; lastMid?: { x: number; y: number }; lastDist?: number } | null>(null);

  const reset = useCallback(() => setTransform(IDENTITY), []);

  // eslint-disable-next-line react-hooks/exhaustive-deps -- intentional: reset on new diagram source
  useEffect(() => { reset(); }, [resetKey]);

  const zoomAt = useCallback((clientX: number, clientY: number, factor: number) => {
    const el = containerRef.current;
    if (!el) return;
    const rect = el.getBoundingClientRect();
    setTransform((prev) => {
      const nextScale = Math.min(MAX_SCALE, Math.max(MIN_SCALE, prev.scale * factor));
      const appliedFactor = nextScale / prev.scale;
      if (appliedFactor === 1) return prev;
      // Keep the point under the cursor/fingers stationary while scaling.
      const originX = clientX - rect.left - rect.width / 2 - prev.x;
      const originY = clientY - rect.top - rect.height / 2 - prev.y;
      return {
        scale: nextScale,
        x: prev.x - originX * (appliedFactor - 1),
        y: prev.y - originY * (appliedFactor - 1),
      };
    });
  }, []);

  const handleWheel = useCallback((e: WheelEvent<HTMLDivElement>) => {
    if (!e.ctrlKey && !e.metaKey) return; // plain wheel: let the page scroll
    e.preventDefault();
    const factor = Math.exp(-e.deltaY * WHEEL_ZOOM_SENSITIVITY);
    zoomAt(e.clientX, e.clientY, factor);
  }, [zoomAt]);

  const handlePointerDown = useCallback((e: ReactPointerEvent<HTMLDivElement>) => {
    if (!dragState.current) dragState.current = { pointers: new Map() };
    dragState.current.pointers.set(e.pointerId, { x: e.clientX, y: e.clientY });
    (e.target as HTMLElement).setPointerCapture?.(e.pointerId);
  }, []);

  const handlePointerMove = useCallback((e: ReactPointerEvent<HTMLDivElement>) => {
    const state = dragState.current;
    if (!state || !state.pointers.has(e.pointerId)) return;
    state.pointers.set(e.pointerId, { x: e.clientX, y: e.clientY });
    const pts = Array.from(state.pointers.values());

    if (pts.length === 1) {
      const [p] = pts;
      const prevMid = state.lastMid ?? p;
      const dx = p.x - prevMid.x;
      const dy = p.y - prevMid.y;
      state.lastMid = p;
      if (dx !== 0 || dy !== 0) {
        setTransform((prev) => ({ ...prev, x: prev.x + dx, y: prev.y + dy }));
      }
    } else if (pts.length >= 2) {
      const [a, b] = pts;
      const mid = { x: (a.x + b.x) / 2, y: (a.y + b.y) / 2 };
      const dist = Math.hypot(a.x - b.x, a.y - b.y);
      if (state.lastDist) {
        zoomAt(mid.x, mid.y, dist / state.lastDist);
      }
      state.lastMid = mid;
      state.lastDist = dist;
    }
  }, [zoomAt]);

  const endPointer = useCallback((e: ReactPointerEvent<HTMLDivElement>) => {
    const state = dragState.current;
    if (!state) return;
    state.pointers.delete(e.pointerId);
    if (state.pointers.size < 2) {
      state.lastDist = undefined;
    }
    if (state.pointers.size === 0) {
      state.lastMid = undefined;
    }
  }, []);

  const handleKeyDown = useCallback((e: ReactKeyboardEvent<HTMLDivElement>) => {
    const el = containerRef.current;
    if (!el) return;
    const rect = el.getBoundingClientRect();
    const cx = rect.left + rect.width / 2;
    const cy = rect.top + rect.height / 2;
    if (e.key === '+' || e.key === '=') { e.preventDefault(); zoomAt(cx, cy, 1.2); }
    else if (e.key === '-' || e.key === '_') { e.preventDefault(); zoomAt(cx, cy, 1 / 1.2); }
    else if (e.key === '0') { e.preventDefault(); reset(); }
    else if (e.key === 'ArrowUp') { e.preventDefault(); setTransform((p) => ({ ...p, y: p.y + 40 })); }
    else if (e.key === 'ArrowDown') { e.preventDefault(); setTransform((p) => ({ ...p, y: p.y - 40 })); }
    else if (e.key === 'ArrowLeft') { e.preventDefault(); setTransform((p) => ({ ...p, x: p.x + 40 })); }
    else if (e.key === 'ArrowRight') { e.preventDefault(); setTransform((p) => ({ ...p, x: p.x - 40 })); }
  }, [zoomAt, reset]);

  const isZoomed = transform.scale !== 1 || transform.x !== 0 || transform.y !== 0;

  return (
    <div
      ref={containerRef}
      role="group"
      aria-label="확대 및 이동 가능한 다이어그램"
      tabIndex={0}
      onWheel={handleWheel}
      onPointerDown={handlePointerDown}
      onPointerMove={handlePointerMove}
      onPointerUp={endPointer}
      onPointerCancel={endPointer}
      onPointerLeave={endPointer}
      onKeyDown={handleKeyDown}
      className={`group relative overflow-hidden select-none ${minHeightClassName} ${maxHeightClassName} ${className}`}
      style={{ touchAction: transform.scale > 1 ? 'none' : 'auto', cursor: isZoomed ? 'grab' : 'default' }}
    >
      <div
        className="flex items-center justify-center w-full h-full [&>svg]:max-w-none [&>img]:max-w-none"
        style={{
          transform: `translate(${transform.x}px, ${transform.y}px) scale(${transform.scale})`,
          transformOrigin: 'center center',
          transition: dragState.current?.pointers.size ? 'none' : 'transform 60ms ease-out',
        }}
      >
        {children}
      </div>

      {/* Controls: visible on hover (desktop) or always (touch, via group-focus-within too) */}
      <div className="absolute bottom-2 right-2 flex gap-1 opacity-0 group-hover:opacity-100 group-focus-within:opacity-100 transition-opacity">
        <button
          type="button"
          aria-label="축소"
          onClick={() => { const r = containerRef.current?.getBoundingClientRect(); if (r) zoomAt(r.left + r.width / 2, r.top + r.height / 2, 1 / 1.4); }}
          className="w-8 h-8 flex items-center justify-center rounded-md bg-black/60 text-white hover:bg-black/80 backdrop-blur-sm"
        >
          <span className="material-symbols-outlined text-lg">zoom_out</span>
        </button>
        <button
          type="button"
          aria-label="확대"
          onClick={() => { const r = containerRef.current?.getBoundingClientRect(); if (r) zoomAt(r.left + r.width / 2, r.top + r.height / 2, 1.4); }}
          className="w-8 h-8 flex items-center justify-center rounded-md bg-black/60 text-white hover:bg-black/80 backdrop-blur-sm"
        >
          <span className="material-symbols-outlined text-lg">zoom_in</span>
        </button>
        <button
          type="button"
          aria-label="맞춤"
          onClick={reset}
          className="w-8 h-8 flex items-center justify-center rounded-md bg-black/60 text-white hover:bg-black/80 backdrop-blur-sm"
        >
          <span className="material-symbols-outlined text-lg">fit_screen</span>
        </button>
        {onFullscreen && (
          <button
            type="button"
            aria-label="전체화면"
            onClick={onFullscreen}
            className="w-8 h-8 flex items-center justify-center rounded-md bg-black/60 text-white hover:bg-black/80 backdrop-blur-sm"
          >
            <span className="material-symbols-outlined text-lg">fullscreen</span>
          </button>
        )}
      </div>
    </div>
  );
}
