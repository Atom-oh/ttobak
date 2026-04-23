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
          <span className="material-symbols-outlined text-amber-500 dark:text-purple-400 text-base animate-pulse">
            psychology
          </span>
          <span className="text-xs font-semibold text-amber-600 dark:text-purple-300 uppercase tracking-wider">
            AI가 감지한 질문 · {questions.length}
          </span>
        </div>
      )}

      <div className="flex flex-col gap-2 w-full">
        {questions.map((q, i) => (
          <button
            key={q}
            onClick={() => onAsk(q)}
            disabled={disabled}
            className={`
              group flex items-center gap-2 text-left text-sm px-4 py-3 rounded-xl
              transition-all duration-150
              disabled:opacity-50 disabled:cursor-not-allowed
              hover:scale-[1.01] active:scale-[0.99]
              ${isDetected
                ? 'bg-amber-50 dark:bg-purple-500/10 text-amber-700 dark:text-purple-200 border border-amber-200 dark:border-purple-500/20 hover:bg-amber-100 dark:hover:bg-purple-500/20 dark:hover:border-purple-500/40'
                : 'bg-primary/5 text-primary/80 hover:bg-primary/10 dark:bg-cyan-500/5 dark:text-cyan-300 dark:hover:bg-cyan-500/10'
              }
            `}
          >
            <span className={`flex-shrink-0 w-2 h-2 rounded-full ${i === 0 && isDetected ? 'animate-pulse' : ''}`}
              style={{ background: isDetected ? '#B026FF' : '#00E5FF' }} />
            <span className="flex-1 truncate">{q}</span>
            <span className="material-symbols-outlined text-sm opacity-40 group-hover:opacity-100 transition-opacity flex-shrink-0">north_east</span>
          </button>
        ))}
      </div>
    </div>
  );
}
