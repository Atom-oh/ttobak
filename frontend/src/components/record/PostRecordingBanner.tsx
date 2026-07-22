'use client';

import { useState } from 'react';
import { formatFileSize } from '@/lib/upload';

export type PostRecordingStep = 'creating' | 'notes' | 'saving' | 'uploading' | 'redirecting' | 'error';

export interface PostRecordingUploadProgress {
  loaded: number;
  total: number;
  percentage: number;
}

interface PostRecordingBannerProps {
  step: PostRecordingStep;
  errorMessage?: string | null;
  /** Live progress during the 'uploading' step (native System Audio and
   * browser mic/tab modes both report this now — see
   * `usePostRecording`'s `uploadProgress`). Null before the first progress
   * event arrives, in which case the plain "Uploading audio..." label is
   * shown instead. */
  uploadProgress?: PostRecordingUploadProgress | null;
  onRetry: () => void;
  onDismiss: () => void;
  onNotesSubmit?: (notes: string) => void;
  onNotesSkip?: () => void;
  /** Notes taken during the meeting — prefills the notes step */
  initialNotes?: string;
}

const STEP_LABELS: Record<string, string> = {
  creating: 'Creating meeting...',
  saving: 'Saving transcript...',
  uploading: 'Uploading audio...',
  redirecting: 'Opening meeting...',
};

export function PostRecordingBanner({ step, errorMessage, uploadProgress, onRetry, onDismiss, onNotesSubmit, onNotesSkip, initialNotes }: PostRecordingBannerProps) {
  const [notes, setNotes] = useState(initialNotes || '');
  const isError = step === 'error';
  const isNotes = step === 'notes';

  if (isNotes) {
    return (
      <div className="fixed top-[64px] left-0 right-0 z-40 mx-4 mt-2 animate-slide-up">
        <div className="rounded-xl shadow-lg px-4 py-4 bg-white dark:bg-surface-lowest border border-slate-200 dark:border-white/10">
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
            rows={6}
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
            : 'bg-white dark:bg-surface-lowest border border-slate-200 dark:border-white/10'
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
            <div className="flex-1 min-w-0">
              <p className="text-sm font-medium text-slate-900 dark:text-gray-100">
                {step === 'uploading' && uploadProgress
                  ? `업로드 중... ${uploadProgress.percentage}% (${formatFileSize(uploadProgress.loaded)} / ${formatFileSize(uploadProgress.total)})`
                  : STEP_LABELS[step]}
              </p>
              {step === 'uploading' && uploadProgress && (
                <div className="mt-1.5 h-1.5 w-full rounded-full bg-slate-100 dark:bg-slate-700 overflow-hidden">
                  <div
                    className="h-full rounded-full bg-primary transition-[width] duration-300"
                    style={{ width: `${Math.min(100, Math.max(0, uploadProgress.percentage))}%` }}
                  />
                </div>
              )}
            </div>
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
