'use client';

import { useCallback, useEffect, useState } from 'react';
import { BrowserRecordingBackup, listBrowserRecordings, type BrowserRecordingMetadata } from '@/lib/browserRecordingBackup';
import { formatFileSize } from '@/lib/upload';

export function BrowserRecordingsCard({ userId, busy, onRestore }: {
  userId: string;
  busy: boolean;
  onRestore: (backup: BrowserRecordingBackup) => Promise<void>;
}) {
  const [items, setItems] = useState<BrowserRecordingMetadata[]>([]);
  const [error, setError] = useState('');
  const [working, setWorking] = useState(false);
  const refresh = useCallback(() => {
    void listBrowserRecordings(userId).then(setItems).catch(() => setError('이 브라우저에 보관된 녹음을 불러오지 못했습니다.'));
  }, [userId]);
  useEffect(() => { refresh(); }, [refresh]);

  const act = async (item: BrowserRecordingMetadata, action: 'resume-upload' | 'download' | 'delete') => {
    if (busy || working) return;
    if (action === 'delete' && !window.confirm(`"${item.title || '미팅'}"의 기기 보관본을 삭제할까요? 삭제한 녹음은 복구할 수 없습니다.`)) return;
    setWorking(true);
    setError('');
    let backup: BrowserRecordingBackup | undefined;
    try {
      backup = await BrowserRecordingBackup.open(userId, item.id);
      if (action === 'resume-upload') {
        await onRestore(backup);
        backup = undefined;
      } else if (action === 'delete') {
        await backup.remove();
      } else {
        const blob = await backup.readBlob();
        const url = URL.createObjectURL(blob);
        const anchor = document.createElement('a');
        anchor.href = url;
        anchor.download = `recording-${item.id}.${item.mimeType.includes('mp4') ? 'm4a' : item.mimeType.includes('ogg') ? 'ogg' : 'webm'}`;
        anchor.click();
        setTimeout(() => URL.revokeObjectURL(url), 60_000);
      }
      refresh();
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : '녹음을 복구하지 못했습니다.');
    } finally {
      backup?.release();
      setWorking(false);
    }
  };

  if (!items.length && !error) return null;
  return (
    <section aria-label="브라우저에 보관된 녹음" className="mb-6 rounded-xl border border-amber-200 bg-amber-50 p-4 dark:border-amber-500/30 dark:bg-amber-500/10">
      <h2 className="text-sm font-semibold">이 브라우저에 보관된 녹음</h2>
      <p className="mt-1 text-xs text-slate-600 dark:text-slate-300">마지막 기기 저장 시점까지 복구합니다. 같은 브라우저와 계정에서만 표시되며, 업로드 완료 후 자동 삭제됩니다. 로그아웃해도 기기에 남으므로 공용 기기에서는 업로드하거나 삭제해 주세요. 브라우저 데이터를 지우면 보관본도 사라집니다.</p>
      {error && <p role="alert" className="mt-2 text-sm text-red-600 dark:text-red-300">{error}</p>}
      <ul className="mt-3 space-y-3">
        {items.map((item) => (
          <li key={item.id} className="flex flex-wrap items-center gap-2">
            <div className="min-w-0 flex-1">
              <p className="truncate text-sm font-medium">{item.title || '미팅'}</p>
              <p className="text-xs text-slate-500">{new Date(item.savedAt).toLocaleString('ko-KR')} · {formatFileSize(item.byteSize)}</p>
            </div>
            <button type="button" disabled={busy || working} onClick={() => void act(item, 'download')} className="px-2 py-1 text-xs text-primary disabled:opacity-50">다운로드</button>
            <button type="button" disabled={busy || working} onClick={() => void act(item, 'delete')} className="px-2 py-1 text-xs text-red-600 disabled:opacity-50">삭제</button>
            <button type="button" disabled={busy || working} onClick={() => void act(item, 'resume-upload')} className="rounded-lg bg-primary px-3 py-2 text-xs text-white disabled:opacity-50">복구하여 마무리</button>
          </li>
        ))}
      </ul>
    </section>
  );
}
