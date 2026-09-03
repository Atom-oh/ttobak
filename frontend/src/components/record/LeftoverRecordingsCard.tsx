'use client';

import { formatFileSize } from '@/lib/upload';
import type { TauriLeftoverRecording } from '@/lib/tauri';

interface LeftoverRecordingsCardProps {
  items: TauriLeftoverRecording[];
  onUpload: (item: TauriLeftoverRecording) => void;
  onDelete: (item: TauriLeftoverRecording) => void;
  /** "나중에" — hide the card for this page visit only (no persistence: the
   * files stay on disk and are offered again next time). */
  onDismiss: () => void;
  /** Disables the action buttons while an upload/delete is in flight. */
  busy?: boolean;
}

export function formatLeftoverTime(modifiedMs: number): string {
  if (!modifiedMs) return '시간 정보 없음';
  return new Date(modifiedMs).toLocaleString('ko-KR', {
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  });
}

/**
 * Mac app only (Tauri System Audio mode). Lists temp WAVs the Rust side
 * adopted at startup from a previous run — a crash or force quit whose
 * recording never got uploaded — and lets the user upload or delete each
 * one explicitly. Cross-account caveat (ADR-024): adoption is scoped to
 * the macOS user's temp directory, not to a ttobak login, so a file here
 * may belong to a DIFFERENT account that used this Mac earlier. The copy
 * says so, and the page-level handlers add a per-file confirm before any
 * upload — this card is deliberately never a one-click path.
 */
export function LeftoverRecordingsCard({ items, onUpload, onDelete, onDismiss, busy }: LeftoverRecordingsCardProps) {
  if (items.length === 0) return null;

  return (
    <div
      role="region"
      aria-label="이전 세션에서 남은 녹음 파일"
      className="w-full max-w-xl mb-4 rounded-xl border border-amber-300/70 dark:border-amber-400/30 bg-amber-50 dark:bg-amber-950/30 shadow-sm px-4 py-3 text-left"
    >
      <div className="flex items-start gap-2">
        <span className="material-symbols-outlined text-amber-600 dark:text-amber-400 text-xl mt-0.5">history</span>
        <div className="flex-1 min-w-0">
          <p className="text-sm font-semibold text-slate-900 dark:text-gray-100">
            이전 세션에서 남은 녹음 파일 {items.length}개
          </p>
          <p className="text-xs text-slate-600 dark:text-slate-300 mt-1">
            앱이 예기치 않게 종료되어 업로드되지 않은 녹음입니다. 이 파일은 이 Mac에서 이전에 로그인한{' '}
            <strong>다른 사용자</strong>의 녹음일 수 있으니, 본인의 녹음인지 확인한 후 업로드하세요.
            마지막 저장 지점까지의 내용만 담겨 있을 수 있습니다.
          </p>
        </div>
        <button
          type="button"
          onClick={onDismiss}
          disabled={busy}
          className="text-xs font-medium text-slate-500 dark:text-slate-400 hover:text-slate-700 dark:hover:text-slate-200 disabled:opacity-50 whitespace-nowrap"
        >
          나중에
        </button>
      </div>
      <ul className="mt-3 divide-y divide-amber-200/70 dark:divide-amber-400/20">
        {items.map((item) => (
          <li key={item.path} className="flex items-center gap-3 py-2">
            <div className="flex-1 min-w-0">
              <p className="text-sm text-slate-900 dark:text-gray-100 truncate" title={item.path}>
                {item.file_name}
              </p>
              <p className="text-xs text-slate-500 dark:text-slate-400">
                {formatFileSize(item.byte_size)} · {formatLeftoverTime(item.modified_ms)}
              </p>
            </div>
            <button
              type="button"
              onClick={() => onDelete(item)}
              disabled={busy}
              className="px-3 py-1.5 rounded-lg text-xs font-medium text-red-600 dark:text-red-400 hover:bg-red-50 dark:hover:bg-red-950/40 disabled:opacity-50 transition-colors"
            >
              삭제
            </button>
            <button
              type="button"
              onClick={() => onUpload(item)}
              disabled={busy}
              className="px-3 py-1.5 rounded-lg text-xs font-medium bg-primary text-white hover:bg-primary/90 disabled:opacity-50 transition-colors"
            >
              업로드
            </button>
          </li>
        ))}
      </ul>
    </div>
  );
}
