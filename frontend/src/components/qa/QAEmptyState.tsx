'use client';

interface QAEmptyStateProps {
  isLive?: boolean;
  meetingTitle?: string;
  automaticAnswersEnabled?: boolean;
}

export function QAEmptyState({ isLive = false, meetingTitle, automaticAnswersEnabled = false }: QAEmptyStateProps) {
  return (
    <div className="flex flex-col items-center justify-center py-6 text-slate-400">
      {/* Large icon in tinted square */}
      <div className="size-16 rounded-2xl bg-primary/10 flex items-center justify-center mb-4">
        <span className="material-symbols-outlined text-3xl text-primary">
          assistant
        </span>
      </div>

      {/* Title and description */}
      <h4 className="text-base font-semibold text-slate-700 dark:text-slate-200 mb-1 text-center break-words max-w-full">
        {isLive ? 'AI 어시스턴트 준비 완료' : 'Ask about this meeting'}
      </h4>
      {isLive && meetingTitle?.trim() && (
        <p className="mb-2 max-w-full truncate text-xs font-medium text-primary">
          {meetingTitle.trim().slice(0, 120)}
        </p>
      )}
      <p className="text-sm text-slate-400 dark:text-slate-500 text-center max-w-[200px]">
        {isLive
          ? automaticAnswersEnabled
            ? '대화에서 질문이 나오면 미팅 맥락에 맞춰 자동으로 답변합니다. 직접 질문할 수도 있어요.'
            : '현재 미팅의 맥락에 맞춰 답변합니다. 궁금한 점을 질문하거나 자동 답변을 켜세요.'
          : '회의 내용에 대해 질문하세요. AI가 회의록을 분석하여 답변해 드립니다.'
        }
      </p>
    </div>
  );
}
