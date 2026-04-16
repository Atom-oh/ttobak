'use client';

import { useState } from 'react';

export type PostRecordingStep = 'creating' | 'notes' | 'saving' | 'uploading' | 'redirecting' | 'error';

interface PostRecordingBannerProps {
  step: PostRecordingStep;
  errorMessage?: string | null;
  onRetry: () => void;
  onDismiss: () => void;
  onNotesSubmit?: (notes: string) => void;
  onNotesSkip?: () => void;
}

const STEP_LABELS: Record<string, string> = {
  creating: 'Creating meeting...',
  saving: 'Saving transcript...',
  uploading: 'Uploading audio...',
  redirecting: 'Opening meeting...',
};

export function PostRecordingBanner({ step, errorMessage, onRetry, onDismiss, onNotesSubmit, onNotesSkip }: PostRecordingBannerProps) {
  const [notes, setNotes] = useState('');
  const isError = step === 'error';
  const isNotes = step === 'notes';

  if (isNotes) {
    return (
      <div className="fixed top-[64px] left-0 right-0 z-40 mx-4 mt-2 animate-slide-up">
        <div className="rounded-xl shadow-lg px-4 py-4 bg-white dark:bg-[#0e0e13] border border-slate-200 dark:border-white/10">
          <div className="flex items-center gap-2 mb-3">
            <span className="material-symbols-outlined text-primary text-xl">edit_note</span>
            <p className="text-sm font-semibold text-slate-900 dark:text-gray-100">
              미팅 노트
            </p>
          </div>
          <textarea
            value={notes}
            onChange={(e) => setNotes(e.target.value)}
            placeholder="회의 중 주요 내용을 간략히 적어주세요..."
            rows={4}
            className="w-full rounded-lg border border-slate-200 dark:border-white/10 bg-slate-50 dark:bg-slate-800/50 px-3 py-2 text-sm text-slate-900 dark:text-gray-100 placeholder:text-slate-400 focus:outline-none focus:ring-2 focus:ring-primary/40 resize-none"
            autoFocus
          />
          <div className="flex justify-end gap-2 mt-3">
            <button
              onClick={() => onNotesSkip?.()}
              className="px-4 py-1.5 rounded-lg text-xs font-medium text-slate-500 dark:text-slate-400 hover:bg-slate-100 dark:hover:bg-slate-700 transition-colors"
            >
              건너뛰기
            </button>
            <button
              onClick={() => onNotesSubmit?.(notes)}
              className="px-4 py-1.5 rounded-lg text-xs font-medium bg-primary text-white hover:bg-primary/90 transition-colors"
            >
              완료
            </button>
          </div>
        </div>
      </div>
    );
  }

  return (
    <div className="fixed top-[64px] left-0 right-0 z-40 mx-4 mt-2 animate-slide-up">
      <div
        className={`rounded-xl shadow-lg px-4 py-3 flex items-center gap-3 ${
          isError
            ? 'bg-red-50 dark:bg-red-900/30 border border-red-200 dark:border-red-800'
            : 'bg-white dark:bg-[#0e0e13] border border-slate-200 dark:border-white/10'
        }`}
      >
        {isError ? (
          <>
            <span className="material-symbols-outlined text-red-500">error</span>
            <p className="flex-1 text-sm text-red-700 dark:text-red-300 truncate">
              {errorMessage || 'An unexpected error occurred.'}
            </p>
            <button
              onClick={onRetry}
              className="px-3 py-1.5 rounded-lg text-xs font-medium border border-red-200 dark:border-red-700 text-red-600 dark:text-red-400 hover:bg-red-100 dark:hover:bg-red-900/40 transition-colors shrink-0"
            >
              Try Again
            </button>
            <button
              onClick={onDismiss}
              className="px-3 py-1.5 rounded-lg text-xs font-medium bg-primary text-white hover:bg-primary/90 transition-colors shrink-0"
            >
              Home
            </button>
          </>
        ) : (
          <>
            <div className="animate-spin rounded-full h-5 w-5 border-2 border-primary border-t-transparent shrink-0" />
            <p className="flex-1 text-sm font-medium text-slate-900 dark:text-gray-100">
              {STEP_LABELS[step]}
            </p>
            <button
              onClick={onDismiss}
              className="p-1.5 hover:bg-slate-100 dark:hover:bg-slate-700 rounded-md transition-colors shrink-0"
              title="Dismiss"
            >
              <span className="material-symbols-outlined text-slate-400 text-lg">close</span>
            </button>
          </>
        )}
      </div>
    </div>
  );
}
