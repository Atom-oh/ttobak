'use client';

/**
 * Notion push section embedded inside the share modal.
 *
 * When the user has a Notion integration registered (saved API key via
 * Settings → Integrations, see `IntegrationSettings.tsx`), this surfaces a
 * "Notion 페이지로 보내기" button alongside the user-share flow. Clicking it
 * calls the existing `POST /api/meetings/{id}/export` Notion exporter, which
 * uses `NotionService.CreatePage` server-side and returns the new page's URL.
 *
 * If Notion is not yet registered, the section becomes a CTA pointing to
 * Settings so the user can connect their workspace first.
 *
 * Design note: Notion push is meeting-specific (creates a Notion page with
 * the meeting transcript + summary), so it lives in `meeting/` rather than
 * in the generic `ShareButton`. The share button injects it via an
 * `extraSection` slot to keep cross-entity reuse clean.
 */

import { useEffect, useState } from 'react';
import { useRouter } from 'next/navigation';
import { exportApi, settingsApi } from '@/lib/api';

interface NotionPushSectionProps {
  meetingId: string;
}

type LoadState = 'loading' | 'connected' | 'not-connected' | 'error';

export function NotionPushSection({ meetingId }: NotionPushSectionProps) {
  const router = useRouter();
  const [state, setState] = useState<LoadState>('loading');
  const [pushing, setPushing] = useState(false);
  const [pushedUrl, setPushedUrl] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  // One-shot integration check; cached on the server-side response so this
  // doesn't add noticeable latency to opening the share modal.
  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const integrations = await settingsApi.getIntegrations();
        if (cancelled) return;
        setState(integrations.notion?.configured ? 'connected' : 'not-connected');
      } catch {
        if (!cancelled) setState('error');
      }
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  const handlePush = async () => {
    setPushing(true);
    setError(null);
    setPushedUrl(null);
    try {
      const response = await exportApi.export(meetingId, 'notion');
      if (response.url) {
        setPushedUrl(response.url);
        // Open immediately for users who want to jump straight to the page.
        // We also keep the URL visible so they can re-open later from the
        // modal without re-pushing (which would create a duplicate page).
        window.open(response.url, '_blank', 'noopener,noreferrer');
      } else {
        setError('Notion에서 페이지 URL을 반환하지 않았습니다');
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Notion 푸시 실패');
    } finally {
      setPushing(false);
    }
  };

  return (
    <div className="border-t border-slate-200 dark:border-slate-700">
      <p className="px-4 pt-3 pb-2 text-xs font-bold text-slate-500 uppercase tracking-wider flex items-center gap-1.5">
        <span className="material-symbols-outlined text-sm">workspaces</span>
        외부로 보내기
      </p>
      <div className="px-4 pb-3">
        {state === 'loading' && (
          <div className="flex items-center gap-2 text-xs text-slate-400 py-2">
            <div className="animate-spin rounded-full h-3 w-3 border-2 border-slate-300 border-t-transparent" />
            연결 상태 확인 중…
          </div>
        )}

        {state === 'error' && (
          <div className="text-xs text-amber-600 dark:text-amber-400 py-2">
            연동 상태를 확인할 수 없습니다. 새로고침 후 다시 시도해 주세요.
          </div>
        )}

        {state === 'not-connected' && (
          <button
            onClick={() => router.push('/settings')}
            className="w-full flex items-center gap-3 px-3 py-2.5 rounded-lg border border-dashed border-slate-300 dark:border-white/15 text-left hover:border-primary/40 hover:bg-primary/5 dark:hover:border-primary/40 dark:hover:bg-primary/5 transition-colors"
          >
            <div className="size-8 rounded-md bg-slate-100 dark:bg-white/5 flex items-center justify-center text-slate-500">
              <span className="material-symbols-outlined text-base">link_off</span>
            </div>
            <div className="flex-1 min-w-0">
              <p className="text-sm font-medium text-slate-700 dark:text-slate-200">
                Notion 연결하기
              </p>
              <p className="text-xs text-slate-500 dark:text-text-muted">
                Settings에서 API 키를 등록하면 미팅을 Notion 페이지로 바로 보낼 수 있습니다
              </p>
            </div>
            <span className="material-symbols-outlined text-slate-400 text-base">arrow_forward</span>
          </button>
        )}

        {state === 'connected' && (
          <div className="space-y-2">
            <button
              onClick={handlePush}
              disabled={pushing}
              className="w-full flex items-center gap-3 px-3 py-2.5 rounded-lg bg-slate-50 dark:bg-white/[0.03] hover:bg-slate-100 dark:hover:bg-white/[0.06] transition-colors disabled:opacity-50"
            >
              <div className="size-8 rounded-md bg-slate-900 dark:bg-white flex items-center justify-center text-white dark:text-slate-900 text-sm font-bold">
                N
              </div>
              <div className="flex-1 min-w-0 text-left">
                <p className="text-sm font-medium text-slate-900 dark:text-white">
                  {pushing ? 'Notion으로 보내는 중…' : 'Notion 페이지로 보내기'}
                </p>
                <p className="text-xs text-slate-500 dark:text-text-muted">
                  요약 · 트랜스크립트 · 액션 아이템을 새 페이지로 생성
                </p>
              </div>
              {pushing ? (
                <div className="animate-spin rounded-full h-4 w-4 border-2 border-primary border-t-transparent" />
              ) : (
                <span className="material-symbols-outlined text-slate-400 text-base">north_east</span>
              )}
            </button>

            {pushedUrl && (
              <a
                href={pushedUrl}
                target="_blank"
                rel="noopener noreferrer"
                className="flex items-center gap-2 px-3 py-2 rounded-lg bg-green-50 dark:bg-green-500/10 text-green-700 dark:text-green-300 text-xs font-medium hover:bg-green-100 dark:hover:bg-green-500/20 transition-colors"
              >
                <span className="material-symbols-outlined text-base">check_circle</span>
                <span className="flex-1 truncate">Notion 페이지가 생성되었습니다 — 클릭하여 열기</span>
                <span className="material-symbols-outlined text-base">open_in_new</span>
              </a>
            )}

            {error && (
              <div className="text-xs text-red-600 dark:text-red-400 px-1">{error}</div>
            )}
          </div>
        )}
      </div>
    </div>
  );
}
