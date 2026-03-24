'use client';

interface ProcessingStatusProps {
  status: string;
}

const statusConfig: Record<string, { label: string; detail: string; progress: string }> = {
  recording: {
    label: '오디오 업로드 준비 중...',
    detail: '잠시만 기다려주세요',
    progress: '25%',
  },
  transcribing: {
    label: 'AI 음성 인식 중... (화자 분리 포함)',
    detail: '음성을 텍스트로 변환하고 있습니다',
    progress: '50%',
  },
  summarizing: {
    label: 'AI 회의록 생성 중...',
    detail: '화자별 요약을 작성하고 있습니다',
    progress: '75%',
  },
};

export function ProcessingStatus({ status }: ProcessingStatusProps) {
  const config = statusConfig[status] || {
    label: '처리 중...',
    detail: '잠시만 기다려주세요',
    progress: '90%',
  };

  return (
    <div className="mb-8 animate-fade-in">
      <div className="flex items-center gap-3 p-4 bg-[var(--color-primary)]/5 border border-[var(--color-primary)]/20 rounded-xl">
        <div className="animate-spin rounded-full h-5 w-5 border-2 border-[var(--color-primary)] border-t-transparent shrink-0" />
        <div className="flex-1">
          <span className="text-sm font-medium text-[var(--color-primary)] block">
            {config.label}
          </span>
          <span className="text-xs text-[var(--color-primary)]/60 mt-0.5 block">
            {config.detail}
          </span>
        </div>
      </div>
      <div className="mt-2 h-1 bg-slate-100 dark:bg-slate-800 rounded-full overflow-hidden">
        <div
          className="h-full bg-[var(--color-primary)] rounded-full animate-pulse"
          style={{ width: config.progress }}
        />
      </div>
    </div>
  );
}
