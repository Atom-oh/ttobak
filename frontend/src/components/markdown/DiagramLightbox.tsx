'use client';

import { useEffect, type ReactNode } from 'react';
import { ZoomPanViewport } from './ZoomPanViewport';

interface DiagramLightboxProps {
  onClose: () => void;
  children: ReactNode;
  resetKey?: string;
}

/**
 * Fullscreen diagram overlay. Follows the same visual language as
 * AttachmentGallery's image modal (fixed inset-0 z-50, black/80 backdrop,
 * backdrop-click-to-close, top-right close button) rather than inventing a
 * new one. This is the primary path on mobile, where the inline card is too
 * narrow for zoom to reveal anything new.
 */
export function DiagramLightbox({ onClose, children, resetKey }: DiagramLightboxProps) {
  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose();
    };
    document.addEventListener('keydown', onKeyDown);
    const prevOverflow = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    return () => {
      document.removeEventListener('keydown', onKeyDown);
      document.body.style.overflow = prevOverflow;
    };
  }, [onClose]);

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/80 backdrop-blur-sm"
      onClick={onClose}
    >
      <div
        className="relative w-[95vw] h-[90vh] m-4"
        onClick={(e) => e.stopPropagation()}
      >
        <button
          onClick={onClose}
          aria-label="닫기"
          className="absolute -top-12 right-0 text-white hover:text-slate-300 transition-colors"
        >
          <span className="material-symbols-outlined text-3xl">close</span>
        </button>

        <ZoomPanViewport
          resetKey={resetKey}
          className="w-full h-full rounded-lg bg-[#0a0a0f]"
          minHeightClassName="min-h-0"
          maxHeightClassName="max-h-none"
        >
          {children}
        </ZoomPanViewport>
      </div>
    </div>
  );
}
