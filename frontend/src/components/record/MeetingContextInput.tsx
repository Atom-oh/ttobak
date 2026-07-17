'use client';

interface MeetingContextInputProps {
  value: string;
  onChange: (value: string) => void;
  /** Show "(선택)" suffix on the label (pre-recording setup) */
  optional?: boolean;
  rows?: number;
}

/**
 * Meeting background context (agenda, customer info, attendees).
 * Fed into the AI Q&A prompt during the meeting. Rendered both in the
 * pre-recording setup and in the in-recording resources section —
 * single source so placeholder/label stay consistent.
 */
export function MeetingContextInput({ value, onChange, optional = false, rows = 3 }: MeetingContextInputProps) {
  return (
    <div className="w-full">
      <label className="flex items-center gap-1.5 text-xs font-semibold text-slate-500 dark:text-text-muted uppercase tracking-wide mb-1.5">
        <span className="material-symbols-outlined text-sm">info</span>
        미팅 컨텍스트{optional ? ' (선택)' : ''}
      </label>
      <textarea
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder="아젠다, 고객사 배경, 참석자 등 미팅 배경 정보를 입력하면 AI 어시스턴트가 답변에 활용합니다."
        rows={rows}
        className="w-full rounded-lg border border-slate-200 dark:border-white/10 bg-white dark:bg-surface-lowest px-3 py-2 text-sm text-slate-900 dark:text-text-main placeholder:text-slate-400 dark:placeholder:text-text-muted/60 focus:outline-none focus:ring-2 focus:ring-primary/30 resize-none"
      />
    </div>
  );
}
