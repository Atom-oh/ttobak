'use client';

export type PostRecordingStep = 'creating' | 'saving' | 'uploading' | 'redirecting' | 'error';

interface PostRecordingBannerProps {
  step: PostRecordingStep;
  errorMessage?: string | null;
  onRetry: () => void;
  onDismiss: () => void;
}

const STEP_LABELS: Record<string, string> = {
  creating: 'Creating meeting...',
  saving: 'Saving transcript...',
  uploading: 'Uploading audio...',
  redirecting: 'Opening meeting...',
};

export function PostRecordingBanner({ step, errorMessage, onRetry, onDismiss }: PostRecordingBannerProps) {
  const isError = step === 'error';

  return (
    <div className="fixed top-[64px] left-0 right-0 z-40 mx-4 mt-2 animate-slide-up">
      <div
        className={`rounded-xl shadow-lg px-4 py-3 flex items-center gap-3 ${
          isError
            ? 'bg-red-50 dark:bg-red-900/30 border border-red-200 dark:border-red-800'
            : 'bg-[var(--color-surface)] border border-[var(--color-border)]'
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
              className="px-3 py-1.5 rounded-lg text-xs font-medium bg-[var(--color-primary)] text-white hover:bg-[var(--color-primary)]/90 transition-colors shrink-0"
            >
              Home
            </button>
          </>
        ) : (
          <>
            <div className="animate-spin rounded-full h-5 w-5 border-2 border-[var(--color-primary)] border-t-transparent shrink-0" />
            <p className="flex-1 text-sm font-medium text-[var(--color-text-primary)]">
              {STEP_LABELS[step]}
            </p>
            <button
              onClick={onDismiss}
              className="p-1.5 hover:bg-slate-100 dark:hover:bg-slate-700 rounded-md transition-colors shrink-0"
              title="Dismiss"
            >
              <span className="material-symbols-outlined text-[var(--color-text-muted)] text-lg">close</span>
            </button>
          </>
        )}
      </div>
    </div>
  );
}
