'use client';

interface QASuggestedQuestionsProps {
  questions: string[];
  isDetected?: boolean;
  onAsk: (question: string) => void;
  disabled?: boolean;
}

export function QASuggestedQuestions({ questions, isDetected = false, onAsk, disabled = false }: QASuggestedQuestionsProps) {
  if (questions.length === 0) return null;

  return (
    <div className={isDetected ? 'animate-fade-in' : ''}>
      {/* Label for detected questions */}
      {isDetected && (
        <div className="flex items-center gap-1.5 mb-2">
          <span className="material-symbols-outlined text-amber-500 text-base animate-pulse">
            psychology
          </span>
          <span className="text-xs font-semibold text-amber-600 dark:text-amber-400">
            AI가 감지한 질문
          </span>
        </div>
      )}

      <div className="flex flex-col gap-2 w-full">
        {questions.map((q) => (
          <button
            key={q}
            onClick={() => onAsk(q)}
            disabled={disabled}
            className={`
              flex items-center gap-2 text-left text-sm px-4 py-3 rounded-xl
              transition-all duration-150
              disabled:opacity-50 disabled:cursor-not-allowed
              hover:scale-[1.01] active:scale-[0.99]
              ${isDetected
                ? 'bg-amber-50 dark:bg-amber-900/20 text-amber-700 dark:text-amber-300 border border-amber-200 dark:border-amber-800 hover:bg-amber-100 dark:hover:bg-amber-900/40'
                : 'bg-[var(--color-primary)]/5 text-[var(--color-primary)]/80 hover:bg-[var(--color-primary)]/10'
              }
            `}
          >
            <span className={`material-symbols-outlined text-lg flex-shrink-0 ${isDetected ? 'text-amber-500' : 'text-[var(--color-primary)]/60'}`}>
              {isDetected ? 'psychology' : 'chat_bubble'}
            </span>
            <span className="flex-1">{q}</span>
          </button>
        ))}
      </div>
    </div>
  );
}
