'use client';

interface QAEmptyStateProps {
  isLive?: boolean;
}

export function QAEmptyState({ isLive = false }: QAEmptyStateProps) {
  return (
    <div className="flex flex-col items-center justify-center py-6 text-slate-400">
      {/* Large icon in tinted square */}
      <div className="size-16 rounded-2xl bg-primary/10 flex items-center justify-center mb-4">
        <span className="material-symbols-outlined text-3xl text-primary">
          assistant
        </span>
      </div>

      {/* Title and description */}
      <h4 className="text-base font-semibold text-slate-700 dark:text-slate-200 mb-1">
        {isLive ? 'AI 어시스턴트 준비 완료' : 'Ask about this meeting'}
      </h4>
      <p className="text-sm text-slate-400 dark:text-slate-500 text-center max-w-[200px]">
        {isLive
          ? '미팅 중 궁금한 점을 물어보세요. AI가 실시간으로 답변해 드립니다.'
          : '회의 내용에 대해 질문하세요. AI가 회의록을 분석하여 답변해 드립니다.'
        }
      </p>
    </div>
  );
}
