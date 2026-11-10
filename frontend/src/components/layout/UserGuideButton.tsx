'use client';

import { useCallback, useEffect, useId, useRef, useState } from 'react';

type Variant = 'sidebar' | 'header' | 'sheet' | 'profile';

const buttonClasses: Record<Variant, string> = {
  sidebar: 'flex items-center gap-3 px-3 py-2 rounded-lg text-sm font-medium text-slate-600 dark:text-text-secondary hover:bg-slate-100 dark:hover:bg-white/5 dark:hover:text-text-main transition-colors',
  header: 'p-2 text-slate-500 dark:text-text-muted hover:text-primary dark:hover:text-primary transition-colors',
  sheet: 'min-h-11 px-3.5 rounded-lg border border-slate-200 dark:border-white/10 text-sm text-slate-700 dark:text-text-secondary flex items-center gap-2',
  profile: 'w-full flex items-center gap-3 px-4 py-3.5 hover:bg-slate-50 dark:hover:bg-slate-800 transition-colors text-left',
};

function UserGuideDialog({ onClose }: { onClose: () => void }) {
  const dialogRef = useRef<HTMLDialogElement>(null);
  const titleId = useId();

  useEffect(() => {
    const dialog = dialogRef.current;
    const previousFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const overflow = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    dialog?.showModal();
    return () => {
      dialog?.close();
      document.body.style.overflow = overflow;
      previousFocus?.focus();
    };
  }, []);

  return (
    <dialog
      ref={dialogRef}
      aria-labelledby={titleId}
      onCancel={(event) => { event.preventDefault(); onClose(); }}
      onKeyDown={(event) => {
        // Escape closes this top-layer dialog, not the mobile menu beneath it.
        if (event.key === 'Escape') event.stopPropagation();
      }}
      className="m-auto p-0 w-screen h-dvh max-w-none max-h-none bg-white dark:bg-background-dark text-slate-900 dark:text-text-main backdrop:bg-slate-950/60 sm:w-[calc(100vw-2rem)] sm:h-[calc(100dvh-2rem)] sm:rounded-2xl sm:shadow-2xl"
    >
      <div className="flex h-full min-h-0 flex-col">
        <header className="flex shrink-0 items-center justify-between gap-3 border-b border-slate-200 dark:border-white/10 px-4 py-3">
          <div className="min-w-0">
            <h2 id={titleId} className="font-bold">사용 설명서</h2>
            <p className="text-xs text-slate-500 dark:text-text-muted">화면별 사용법 · AI 동작 원리 · 문제 해결</p>
          </div>
          <button
            type="button"
            autoFocus
            aria-label="설명서 닫기"
            onClick={onClose}
            className="flex min-h-11 shrink-0 items-center gap-1 rounded-lg px-3 text-sm hover:bg-slate-100 dark:hover:bg-white/10 transition-colors"
          >
            <span className="material-symbols-outlined text-lg" aria-hidden="true">close</span>
            닫기
          </button>
        </header>
        <iframe
          title="TTOBAK 사용 설명서"
          src="/user-guide/index.html"
          sandbox="allow-scripts allow-modals"
          referrerPolicy="no-referrer"
          className="w-full min-h-0 flex-1 border-0 bg-white"
        />
      </div>
    </dialog>
  );
}

/** Opens the same bundled manual in browsers and the Mac app's WebView. */
export function UserGuideButton({ variant }: { variant: Variant }) {
  const [open, setOpen] = useState(false);
  const close = useCallback(() => setOpen(false), []);

  return (
    <>
      <button
        type="button"
        onClick={(event) => {
          // Safari may leave focus on the previous field after a pointer click.
          event.currentTarget.focus();
          setOpen(true);
        }}
        aria-label="사용 설명서 열기"
        aria-haspopup="dialog"
        title="사용 설명서"
        className={buttonClasses[variant]}
      >
        <span className={`material-symbols-outlined ${variant === 'sidebar' ? 'text-xl' : variant === 'sheet' ? 'text-lg' : ''} ${variant === 'profile' ? 'text-slate-500' : ''}`} aria-hidden="true">help_outline</span>
        {variant !== 'header' && <span className={variant === 'profile' ? 'flex-1 text-sm font-medium text-slate-900 dark:text-white' : ''}>사용 설명서</span>}
        {variant === 'profile' && <span className="material-symbols-outlined text-slate-400 text-lg" aria-hidden="true">chevron_right</span>}
      </button>
      {open && <UserGuideDialog onClose={close} />}
    </>
  );
}
